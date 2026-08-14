package exec_test

import (
	"context"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/exec/build"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/runtime/runtimetest"
	"github.com/concourse/concourse/vars"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// runtimetest.NewVolume supplies the runtime behavior while this adapter keeps
// the DB side attached to the clone-local persisted volume.
type runtimeVolumeWithDB struct {
	runtime.Volume
	databaseVolume db.CreatedVolume
}

func (volume runtimeVolumeWithDB) DBVolume() db.CreatedVolume {
	return volume.databaseVolume
}

var _ = Describe("ArtifactOutputStep", func() {
	Describe("persisted PostgreSQL state", func() {
		var (
			ctx    context.Context
			cancel func()

			state exec.RunState

			step    exec.Step
			stepOk  bool
			stepErr error
			plan    atc.Plan

			fixture       *execDBFixture
			realBuild     db.Build
			created       db.CreatedVolume
			runtimeVolume *runtimetest.Volume
			workerPool    exec.Pool
		)

		BeforeEach(func() {
			ctx, cancel = context.WithCancel(context.Background())
			state = exec.NewRunState(noopStepper, vars.StaticVariables{})
			fixture = useExecDB()

			team, err := fixture.TeamFactory.CreateTeam(atc.Team{Name: "some-team"})
			Expect(err).NotTo(HaveOccurred())
			realBuild, err = team.CreateOneOffBuild()
			Expect(err).NotTo(HaveOccurred())
			runtimeVolume = runtimetest.NewVolume("some-volume")
			runtimeWorker := runtimetest.NewWorker("worker").WithVolumes(runtimeVolume)
			workerPool = saveRuntimeWorkerPool(fixture, runtimeWorkerSeed{
				Model: runtimeWorker,
				Team:  team,
			})
			creating, err := db.NewVolumeRepository(fixture.Conn).CreateVolumeWithHandle(
				"some-volume", team.ID(), runtimeWorker.Name(), db.VolumeTypeArtifact,
			)
			Expect(err).NotTo(HaveOccurred())
			created, err = creating.Created()
			Expect(err).NotTo(HaveOccurred())

			state.ArtifactRepository().RegisterArtifact(
				build.ArtifactName("some-artifact-name"),
				runtimeVolumeWithDB{Volume: runtimeVolume, databaseVolume: created},
				false,
			)
			plan = atc.Plan{ArtifactOutput: &atc.ArtifactOutputPlan{Name: "some-artifact-name"}}
			step = exec.NewArtifactOutputStep(plan, realBuild, workerPool)
		})

		AfterEach(func() {
			cancel()
		})

		JustBeforeEach(func() {
			stepOk, stepErr = step.Run(ctx, state)
		})

		Context("when the source does not exist", func() {
			BeforeEach(func() {
				state = exec.NewRunState(noopStepper, vars.StaticVariables{})
			})

			It("returns the error", func() {
				Expect(stepErr).To(MatchError(exec.ArtifactNotFoundError{
					ArtifactName: "some-artifact-name",
				}))
			})
		})

		Context("when initializing the artifact succeeds", func() {
			It("persists the output artifact association", func() {
				Expect(stepErr).NotTo(HaveOccurred())
				Expect(stepOk).To(BeTrue())
				found, err := realBuild.Reload()
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				artifacts, err := realBuild.Artifacts()
				Expect(err).NotTo(HaveOccurred())
				Expect(artifacts).To(HaveLen(1))
				artifact := artifacts[0]
				Expect(artifact.ID()).To(BeNumerically(">", 0))
				Expect(artifact.Name()).To(Equal("some-artifact-name"))
				Expect(artifact.BuildID()).To(Equal(realBuild.ID()))

				artifactVolume, found, err := artifact.Volume(realBuild.TeamID())
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(artifactVolume.Handle()).To(Equal("some-volume"))
				Expect(artifactVolume.TeamID()).To(Equal(realBuild.TeamID()))
				Expect(artifactVolume.WorkerName()).To(Equal("worker"))

				persistedVolume, found, err := db.NewVolumeRepository(fixture.Conn).FindVolume("some-volume")
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(persistedVolume.Handle()).To(Equal(created.Handle()))
				Expect(persistedVolume.WorkerArtifactID()).To(Equal(artifact.ID()))
			})
		})
	})
})
