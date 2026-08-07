package exec_test

import (
	"context"
	"errors"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/exec/build"
	"github.com/concourse/concourse/atc/exec/execfakes"
	"github.com/concourse/concourse/atc/runtime/runtimetest"
	"github.com/concourse/concourse/vars"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// A healthy clone cannot make Artifact fail on demand while preserving the
// build and artifact rows used by the surrounding success setup.
type artifactErrorBuild struct {
	db.Build
	err error
}

func (build artifactErrorBuild) Artifact(int) (db.WorkerArtifact, error) {
	return nil, build.err
}

// A healthy artifact cannot deterministically return a Volume error/not-found
// result after its persisted volume association has been created.
type volumeResultArtifact struct {
	db.WorkerArtifact
	volume db.CreatedVolume
	found  bool
	err    error
}

func (artifact volumeResultArtifact) Volume(int) (db.CreatedVolume, bool, error) {
	return artifact.volume, artifact.found, artifact.err
}

// artifactResultBuild returns the wrapped artifact at the Build.Artifact seam;
// every other Build method remains backed by the healthy real build.
type artifactResultBuild struct {
	db.Build
	artifact db.WorkerArtifact
}

func (build artifactResultBuild) Artifact(int) (db.WorkerArtifact, error) {
	return build.artifact, nil
}

var _ = Describe("ArtifactInputStep", func() {
	Describe("persisted PostgreSQL state", func() {
		var (
			ctx    context.Context
			cancel func()

			state exec.RunState

			step    exec.Step
			stepOk  bool
			stepErr error
			plan    atc.Plan

			fixture        *execDBFixture
			realBuild      db.Build
			created        db.CreatedVolume
			artifact       db.WorkerArtifact
			runtimeVolume  *runtimetest.Volume
			fakeWorkerPool *execfakes.FakePool
		)

		BeforeEach(func() {
			ctx, cancel = context.WithCancel(context.Background())
			state = exec.NewRunState(noopStepper, vars.StaticVariables{})
			fixture = useExecDB()

			team, err := fixture.TeamFactory.CreateTeam(atc.Team{Name: "some-team"})
			Expect(err).NotTo(HaveOccurred())
			realBuild, err = team.CreateOneOffBuild()
			Expect(err).NotTo(HaveOccurred())
			worker, err := fixture.WorkerFactory.SaveWorker(
				atc.Worker{Name: "worker", Platform: "linux"}, 0,
			)
			Expect(err).NotTo(HaveOccurred())
			creating, err := db.NewVolumeRepository(fixture.Conn).CreateVolumeWithHandle(
				"some-volume", team.ID(), worker.Name(), db.VolumeTypeArtifact,
			)
			Expect(err).NotTo(HaveOccurred())
			created, err = creating.Created()
			Expect(err).NotTo(HaveOccurred())
			artifact, err = created.InitializeArtifact("some-input-artifact-name", realBuild.ID())
			Expect(err).NotTo(HaveOccurred())

			plan = atc.Plan{ArtifactInput: &atc.ArtifactInputPlan{
				ArtifactID: artifact.ID(),
				Name:       "some-input-artifact-name",
			}}
			runtimeVolume = runtimetest.NewVolume(created.Handle())
			fakeWorkerPool = new(execfakes.FakePool)
			fakeWorkerPool.LocateVolumeReturns(
				runtimeVolume, runtimetest.NewWorker(worker.Name()), true, nil,
			)
			step = exec.NewArtifactInputStep(plan, realBuild, fakeWorkerPool)
		})

		AfterEach(func() {
			cancel()
		})

		JustBeforeEach(func() {
			stepOk, stepErr = step.Run(ctx, state)
		})

		Context("when looking up the build artifact errors", func() {
			BeforeEach(func() {
				step = exec.NewArtifactInputStep(
					plan,
					artifactErrorBuild{Build: realBuild, err: errors.New("nope")},
					fakeWorkerPool,
				)
			})

			It("returns the error", func() {
				Expect(stepErr).To(MatchError("nope"))
			})
		})

		Context("when looking up the db volume fails", func() {
			BeforeEach(func() {
				wrappedArtifact := volumeResultArtifact{
					WorkerArtifact: artifact,
					volume:         nil,
					found:          false,
					err:            errors.New("nope"),
				}
				wrappedBuild := artifactResultBuild{Build: realBuild, artifact: wrappedArtifact}
				step = exec.NewArtifactInputStep(plan, wrappedBuild, fakeWorkerPool)
			})

			It("returns the error", func() {
				Expect(stepErr).To(MatchError("nope"))
			})
		})

		Context("when the db volume does not exist", func() {
			BeforeEach(func() {
				wrappedArtifact := volumeResultArtifact{
					WorkerArtifact: artifact,
					volume:         nil,
					found:          false,
					err:            nil,
				}
				wrappedBuild := artifactResultBuild{Build: realBuild, artifact: wrappedArtifact}
				step = exec.NewArtifactInputStep(plan, wrappedBuild, fakeWorkerPool)
			})

			It("returns the error", func() {
				Expect(stepErr).To(MatchError(exec.ArtifactVolumeNotFoundError{
					ArtifactName: "some-input-artifact-name",
				}))
			})
		})

		Context("when looking up the worker volume fails", func() {
			BeforeEach(func() {
				fakeWorkerPool.LocateVolumeReturns(nil, nil, false, errors.New("nope"))
			})

			It("returns the error", func() {
				Expect(stepErr).To(MatchError("nope"))
			})
		})

		Context("when the worker volume does not exist", func() {
			BeforeEach(func() {
				fakeWorkerPool.LocateVolumeReturns(nil, nil, false, nil)
			})

			It("returns an error", func() {
				Expect(stepErr).To(MatchError(exec.ArtifactVolumeNotFoundError{
					ArtifactName: "some-input-artifact-name",
				}))
			})
		})

		It("registers the artifact", func() {
			Expect(stepErr).NotTo(HaveOccurred())
			Expect(artifact.ID()).To(BeNumerically(">", 0))
			Expect(artifact.Name()).To(Equal("some-input-artifact-name"))
			Expect(artifact.BuildID()).To(Equal(realBuild.ID()))
			Expect(plan.ArtifactInput.ArtifactID).To(Equal(artifact.ID()))

			persistedVolume, found, err := artifact.Volume(realBuild.TeamID())
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(persistedVolume.Handle()).To(Equal("some-volume"))
			Expect(persistedVolume.TeamID()).To(Equal(realBuild.TeamID()))
			Expect(persistedVolume.WorkerName()).To(Equal("worker"))

			Expect(fakeWorkerPool.LocateVolumeCallCount()).To(Equal(1))
			_, teamID, handle := fakeWorkerPool.LocateVolumeArgsForCall(0)
			Expect(teamID).To(Equal(created.TeamID()))
			Expect(handle).To(Equal(created.Handle()))

			registered, fromCache, found := state.ArtifactRepository().ArtifactFor(
				build.ArtifactName("some-input-artifact-name"),
			)
			Expect(found).To(BeTrue())
			Expect(registered).To(Equal(runtimeVolume))
			Expect(fromCache).To(BeFalse())
		})

		It("succeeds", func() {
			Expect(stepOk).To(BeTrue())
		})
	})
})
