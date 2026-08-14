package engine_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/engine"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/policy"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/runtime/runtimetest"
	"github.com/concourse/concourse/atc/worker"
	"github.com/concourse/concourse/vars"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type runtimeWorkerFactory map[string]runtime.Worker

func (factory runtimeWorkerFactory) NewWorker(_ lager.Logger, dbWorker db.Worker) runtime.Worker {
	return factory[dbWorker.Name()]
}

func newRealStepperFactory(fixture *engineDBFixture, runtimeWorkers runtimeWorkerFactory) engine.StepperFactory {
	pool := worker.NewPool(
		runtimeWorkers,
		worker.DB{
			WorkerFactory: fixture.WorkerFactory,
			TeamFactory:   fixture.TeamFactory,
		},
	)
	coreFactory := engine.NewCoreStepFactory(
		pool,
		worker.Streamer{},
		fixture.LockFactory,
		fixture.TeamFactory,
		fixture.BuildFactory,
		fixture.ResourceCacheFactory,
		fixture.ResourceConfigFactory,
		atc.ContainerLimits{},
		atc.ContainerLimits{},
		0,
		0,
		0,
		0,
	)
	return engine.NewStepperFactory(
		coreFactory,
		"http://example.com",
		newCheckRateLimiter(),
		policy.NoopChecker{},
		fixture.WorkerFactory,
		fixture.LockFactory,
		fixture.ResourceConfigFactory,
		fixture.ResourceCacheFactory,
		nil,
	)
}

var _ = Describe("Builder", func() {
	It("rejects builds without a supported schema", func() {
		fixture := useEngineDB()
		_, _, _, build := createEngineJobBuild(
			fixture,
			"some-team",
			atc.PipelineRef{Name: "some-pipeline"},
			atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}},
			"some-user",
		)
		Expect(build.Schema()).To(BeEmpty())

		_, err := newRealStepperFactory(fixture, nil).StepperForBuild(build)
		Expect(err).To(MatchError("schema not supported"))
	})

	It("returns an identity step for an unknown plan", func() {
		fixture := useEngineDB()
		_, _, _, build := createEngineJobBuild(
			fixture,
			"some-team",
			atc.PipelineRef{Name: "some-pipeline"},
			atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}},
			"some-user",
		)
		unknownPlan := atc.Plan{ID: "unknown-plan-id"}
		started, err := build.Start(unknownPlan)
		Expect(err).NotTo(HaveOccurred())
		Expect(started).To(BeTrue())
		Expect(build.Reload()).To(BeTrue())

		stepper, err := newRealStepperFactory(fixture, nil).StepperForBuild(build)
		Expect(err).NotTo(HaveOccurred())
		Expect(stepper(unknownPlan)).To(Equal(exec.IdentityStep{}))
	})

	It("propagates build and attempt metadata into the runtime task container", func() {
		fixture := useEngineDB()
		team, pipeline, job, build := createEngineJobBuild(
			fixture,
			"some-team",
			atc.PipelineRef{
				Name:         "some-pipeline",
				InstanceVars: atc.InstanceVars{"branch": "master"},
			},
			atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}},
			"some-user",
		)

		planFactory := atc.NewPlanFactory(123)
		taskPlan := planFactory.NewPlan(atc.TaskPlan{
			Name: "some-task",
			Config: &atc.TaskConfig{
				Platform: "linux",
				Run: atc.TaskRunConfig{
					Path: "echo",
					Args: []string{"hello"},
				},
			},
		})
		taskPlan.Attempts = []int{2, 1}

		taskNameHash := sha256.Sum256([]byte("some-task"))
		expectedWorkingDirectory := filepath.Join("/tmp", "build", fmt.Sprintf("%x", taskNameHash[:4]))
		runtimeContainer := runtimetest.NewContainer().WithProcess(
			runtime.ProcessSpec{
				ID:   "task",
				Path: "echo",
				Args: []string{"hello"},
				Dir:  expectedWorkingDirectory,
				TTY: &runtime.TTYSpec{
					WindowSize: runtime.WindowSize{Columns: 500, Rows: 500},
				},
			},
			runtimetest.ProcessStub{ExitStatus: 0},
		)
		runtimeWorker := runtimetest.NewWorker("runtime-worker").WithContainer(
			db.NewBuildStepContainerOwner(build.ID(), taskPlan.ID, team.ID()),
			runtimeContainer,
			nil,
		)
		workerContainer := runtimeWorker.Containers[0]

		dbWorker, err := fixture.WorkerFactory.SaveWorker(
			atc.Worker{Name: runtimeWorker.Name(), Platform: "linux"},
			0,
		)
		Expect(err).NotTo(HaveOccurred())

		started, err := build.Start(taskPlan)
		Expect(err).NotTo(HaveOccurred())
		Expect(started).To(BeTrue())
		Expect(build.Reload()).To(BeTrue())

		stepper, err := newRealStepperFactory(
			fixture,
			runtimeWorkerFactory{dbWorker.Name(): runtimeWorker},
		).StepperForBuild(build)
		Expect(err).NotTo(HaveOccurred())
		step := stepper(taskPlan)
		state := exec.NewRunState(stepper, vars.StaticVariables{})
		succeeded, err := step.Run(context.Background(), state)
		Expect(err).NotTo(HaveOccurred())
		Expect(succeeded).To(BeTrue())

		Expect(workerContainer.Metadata).To(Equal(db.ContainerMetadata{
			Type:                 db.ContainerTypeTask,
			PipelineID:           pipeline.ID(),
			JobID:                job.ID(),
			BuildID:              build.ID(),
			PipelineName:         pipeline.Name(),
			PipelineInstanceVars: `{"branch":"master"}`,
			JobName:              job.Name(),
			BuildName:            build.Name(),
			StepName:             "some-task",
			Attempt:              "2.1",
			WorkingDirectory:     expectedWorkingDirectory,
		}))
		Expect(workerContainer.Spec.TeamName).To(Equal("some-team"))
	})
})
