package exec_test

import (
	"context"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/exec/build"
	"github.com/concourse/concourse/atc/runtime/runtimetest"
	"github.com/concourse/concourse/vars"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

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

			fixture       *execDBFixture
			realBuild     db.Build
			created       db.CreatedVolume
			artifact      db.WorkerArtifact
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
			artifact, err = created.InitializeArtifact("some-input-artifact-name", realBuild.ID())
			Expect(err).NotTo(HaveOccurred())

			plan = atc.Plan{ArtifactInput: &atc.ArtifactInputPlan{
				ArtifactID: artifact.ID(),
				Name:       "some-input-artifact-name",
			}}
			step = exec.NewArtifactInputStep(plan, realBuild, workerPool)
		})

		AfterEach(func() {
			cancel()
		})

		JustBeforeEach(func() {
			stepOk, stepErr = step.Run(ctx, state)
		})

		Context("when the db volume does not exist", func() {
			BeforeEach(func() {
				destroying, err := created.Destroying()
				Expect(err).NotTo(HaveOccurred())
				destroyed, err := destroying.Destroy()
				Expect(err).NotTo(HaveOccurred())
				Expect(destroyed).To(BeTrue())
			})

			It("returns the error", func() {
				Expect(stepErr).To(MatchError(exec.ArtifactVolumeNotFoundError{
					ArtifactName: "some-input-artifact-name",
				}))
			})
		})

		Context("when the worker volume does not exist", func() {
			BeforeEach(func() {
				team := fixture.TeamFactory.GetByID(realBuild.TeamID())
				workerPool = saveRuntimeWorkerPool(fixture, runtimeWorkerSeed{
					Model: runtimetest.NewWorker("worker"),
					Team:  team,
				})
				step = exec.NewArtifactInputStep(plan, realBuild, workerPool)
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
