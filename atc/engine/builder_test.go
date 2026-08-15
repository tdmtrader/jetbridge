package engine_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/engine"
	"github.com/concourse/concourse/atc/event"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/policy"
	"github.com/concourse/concourse/atc/resource"
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

	It("derives nested retry attempts in runtime task metadata", func() {
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
		taskPlans := []atc.Plan{
			planFactory.NewPlan(atc.TaskPlan{
				Name: "some-task",
				Config: &atc.TaskConfig{
					Platform: "linux",
					Run: atc.TaskRunConfig{
						Path: "echo",
						Args: []string{"hello"},
					},
				},
			}),
			planFactory.NewPlan(atc.TaskPlan{
				Name: "some-task",
				Config: &atc.TaskConfig{
					Platform: "linux",
					Run: atc.TaskRunConfig{
						Path: "echo",
						Args: []string{"hello"},
					},
				},
			}),
			planFactory.NewPlan(atc.TaskPlan{
				Name: "some-task",
				Config: &atc.TaskConfig{
					Platform: "linux",
					Run: atc.TaskRunConfig{
						Path: "echo",
						Args: []string{"hello"},
					},
				},
			}),
		}
		nestedRetryPlan := planFactory.NewPlan(atc.RetryPlan{taskPlans[1], taskPlans[2]})
		retryPlan := planFactory.NewPlan(atc.RetryPlan{taskPlans[0], nestedRetryPlan})

		taskNameHash := sha256.Sum256([]byte("some-task"))
		expectedWorkingDirectory := filepath.Join("/tmp", "build", fmt.Sprintf("%x", taskNameHash[:4]))
		runtimeWorker := runtimetest.NewWorker("runtime-worker")
		for i, taskPlan := range taskPlans {
			runtimeWorker = runtimeWorker.WithContainer(
				db.NewBuildStepContainerOwner(build.ID(), taskPlan.ID, team.ID()),
				runtimetest.NewContainer().WithProcess(
					runtime.ProcessSpec{
						ID:   "task",
						Path: "echo",
						Args: []string{"hello"},
						Dir:  expectedWorkingDirectory,
						TTY: &runtime.TTYSpec{
							WindowSize: runtime.WindowSize{Columns: 500, Rows: 500},
						},
					},
					runtimetest.ProcessStub{ExitStatus: []int{1, 1, 0}[i]},
				),
				nil,
			)
		}

		dbWorker, err := fixture.WorkerFactory.SaveWorker(
			atc.Worker{Name: runtimeWorker.Name(), Platform: "linux"},
			0,
		)
		Expect(err).NotTo(HaveOccurred())

		started, err := build.Start(retryPlan)
		Expect(err).NotTo(HaveOccurred())
		Expect(started).To(BeTrue())
		Expect(build.Reload()).To(BeTrue())

		stepper, err := newRealStepperFactory(
			fixture,
			runtimeWorkerFactory{dbWorker.Name(): runtimeWorker},
		).StepperForBuild(build)
		Expect(err).NotTo(HaveOccurred())
		step := stepper(build.PrivatePlan())
		state := exec.NewRunState(stepper, vars.StaticVariables{})
		succeeded, err := step.Run(context.Background(), state)
		Expect(err).NotTo(HaveOccurred())
		Expect(succeeded).To(BeTrue())

		expectedTaskEnv := []string{
			fmt.Sprintf("BUILD_ID=%d", build.ID()),
			"BUILD_NAME=" + build.Name(),
			"BUILD_TEAM_NAME=some-team",
			"BUILD_JOB_NAME=" + job.Name(),
			"BUILD_PIPELINE_NAME=" + pipeline.Name(),
			"ATC_EXTERNAL_URL=http://example.com",
		}
		for i, expectedAttempt := range []string{"1", "2.1", "2.2"} {
			Expect(runtimeWorker.Containers[i].Metadata).To(Equal(db.ContainerMetadata{
				Type:                 db.ContainerTypeTask,
				PipelineID:           pipeline.ID(),
				JobID:                job.ID(),
				BuildID:              build.ID(),
				PipelineName:         pipeline.Name(),
				PipelineInstanceVars: `{"branch":"master"}`,
				JobName:              job.Name(),
				BuildName:            build.Name(),
				StepName:             "some-task",
				Attempt:              expectedAttempt,
				WorkingDirectory:     expectedWorkingDirectory,
			}))
			Expect(runtimeWorker.Containers[i].Spec).To(SatisfyAll(
				HaveField("TeamID", team.ID()),
				HaveField("TeamName", "some-team"),
				HaveField("JobID", job.ID()),
				HaveField("StepName", "some-task"),
				HaveField("Type", db.ContainerTypeTask),
				HaveField("Dir", expectedWorkingDirectory),
				HaveField("Env", ConsistOf(expectedTaskEnv)),
			))
			Expect(runtimeWorker.Containers[i].RunningProcesses()).To(HaveLen(1))
		}
	})

	It("exposes instance-qualified build metadata to a real put container", func() {
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

		planFactory := atc.NewPlanFactory(456)
		putPlan := planFactory.NewPlan(atc.PutPlan{
			Name: "some-put",
			Type: "some-resource-type",
			TypeImage: atc.TypeImage{
				BaseType: "some-resource-type",
			},
			Source:               atc.Source{"some": "source"},
			Params:               atc.Params{"some": "params"},
			ExposeBuildCreatedBy: true,
		})

		runtimeContainer := runtimetest.NewContainer().WithProcess(
			runtime.ProcessSpec{
				ID:   "resource",
				Path: "/opt/resource/out",
				Args: []string{resource.ResourcesDir("put")},
			},
			runtimetest.ProcessStub{
				Attachable: true,
				Output: resource.VersionResult{
					Version: atc.Version{"version": "one"},
				},
			},
		)
		runtimeWorker := runtimetest.NewWorker("runtime-worker").WithContainer(
			db.NewBuildStepContainerOwner(build.ID(), putPlan.ID, team.ID()),
			runtimeContainer,
			nil,
		)
		workerContainer := runtimeWorker.Containers[0]
		dbWorker, err := fixture.WorkerFactory.SaveWorker(
			atc.Worker{Name: runtimeWorker.Name(), Platform: "linux"},
			0,
		)
		Expect(err).NotTo(HaveOccurred())

		started, err := build.Start(putPlan)
		Expect(err).NotTo(HaveOccurred())
		Expect(started).To(BeTrue())
		Expect(build.Reload()).To(BeTrue())

		stepper, err := newRealStepperFactory(
			fixture,
			runtimeWorkerFactory{dbWorker.Name(): runtimeWorker},
		).StepperForBuild(build)
		Expect(err).NotTo(HaveOccurred())
		step := stepper(build.PrivatePlan())
		succeeded, err := step.Run(
			context.Background(),
			exec.NewRunState(stepper, vars.StaticVariables{}),
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(succeeded).To(BeTrue())

		Expect(workerContainer.Metadata).To(Equal(db.ContainerMetadata{
			Type:                 db.ContainerTypePut,
			PipelineID:           pipeline.ID(),
			JobID:                job.ID(),
			BuildID:              build.ID(),
			PipelineName:         pipeline.Name(),
			PipelineInstanceVars: `{"branch":"master"}`,
			JobName:              job.Name(),
			BuildName:            build.Name(),
			StepName:             "some-put",
			WorkingDirectory:     resource.ResourcesDir("put"),
		}))

		expectedBuildURL := fmt.Sprintf(
			"http://example.com/teams/some-team/pipelines/some-pipeline/jobs/some-job/builds/%s",
			build.Name(),
		) + `?vars.branch=%22master%22`
		Expect(workerContainer.Spec).To(SatisfyAll(
			HaveField("TeamID", team.ID()),
			HaveField("TeamName", "some-team"),
			HaveField("JobID", job.ID()),
			HaveField("Type", db.ContainerTypePut),
			HaveField("Dir", resource.ResourcesDir("put")),
			HaveField("ImageSpec.ResourceType", "some-resource-type"),
			HaveField("Env", ConsistOf(
				fmt.Sprintf("BUILD_ID=%d", build.ID()),
				"BUILD_NAME="+build.Name(),
				fmt.Sprintf("BUILD_TEAM_ID=%d", team.ID()),
				"BUILD_TEAM_NAME=some-team",
				fmt.Sprintf("BUILD_JOB_ID=%d", job.ID()),
				"BUILD_JOB_NAME="+job.Name(),
				fmt.Sprintf("BUILD_PIPELINE_ID=%d", pipeline.ID()),
				"BUILD_PIPELINE_NAME="+pipeline.Name(),
				`BUILD_PIPELINE_INSTANCE_VARS={"branch":"master"}`,
				"ATC_EXTERNAL_URL=http://example.com",
				"BUILD_URL="+expectedBuildURL,
				fmt.Sprintf("BUILD_URL_SHORT=http://example.com/builds/%d", build.ID()),
				"BUILD_CREATED_BY=some-user",
			)),
		))
	})

	It("executes a persisted composite control-flow plan", func() {
		fixture := useEngineDB()
		_, _, _, build := createEngineJobBuild(
			fixture,
			"some-team",
			atc.PipelineRef{Name: "some-pipeline"},
			atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}},
			"some-user",
		)

		planFactory := atc.NewPlanFactory(700)
		identityID := 0
		identityPlan := func() atc.Plan {
			identityID++
			return atc.Plan{ID: atc.PlanID(fmt.Sprintf("identity-%d", identityID))}
		}
		runPlan := planFactory.NewPlan(atc.RunPlan{
			Message: "composite-plan-ran",
			Type:    "some-prototype",
		})
		ensureRunPlan := planFactory.NewPlan(atc.RunPlan{
			Message: "ensure-plan-ran",
			Type:    "some-prototype",
		})
		acrossPlan := planFactory.NewPlan(atc.AcrossPlan{
			Vars: []atc.AcrossVar{{
				Var:    "branch",
				Values: []any{"main"},
			}},
			SubStepTemplate: `{"id":"across-template","run":{"message":"across-((.:branch))","type":"some-prototype","privileged":false}}`,
		})
		parallelPlan := planFactory.NewPlan(atc.InParallelPlan{
			Limit:    2,
			FailFast: true,
			Steps: []atc.Plan{
				planFactory.NewPlan(atc.TimeoutPlan{
					Step: planFactory.NewPlan(atc.TryPlan{Step: planFactory.NewPlan(atc.ArtifactInputPlan{
						ArtifactID: 999_999,
						Name:       "missing-input",
					})}),
					Duration: "1m",
				}),
				planFactory.NewPlan(atc.OnFailurePlan{
					Step: identityPlan(),
					Next: planFactory.NewPlan(atc.ArtifactOutputPlan{Name: "failure-output"}),
				}),
				planFactory.NewPlan(atc.OnErrorPlan{
					Step: identityPlan(),
					Next: planFactory.NewPlan(atc.ArtifactInputPlan{ArtifactID: 123, Name: "error-input"}),
				}),
				planFactory.NewPlan(atc.OnAbortPlan{
					Step: identityPlan(),
					Next: planFactory.NewPlan(atc.ArtifactInputPlan{
						ArtifactID: 999_998,
						Name:       "must-not-run-on-abort",
					}),
				}),
				acrossPlan,
			},
		})
		doPlan := planFactory.NewPlan(atc.DoPlan{
			parallelPlan,
			planFactory.NewPlan(atc.OnSuccessPlan{
				Step: identityPlan(),
				Next: runPlan,
			}),
		})
		compositePlan := planFactory.NewPlan(atc.EnsurePlan{
			Step: doPlan,
			Next: ensureRunPlan,
		})

		started, err := build.Start(compositePlan)
		Expect(err).NotTo(HaveOccurred())
		Expect(started).To(BeTrue())
		Expect(build.Reload()).To(BeTrue())
		Expect(build.PrivatePlan()).To(Equal(compositePlan))

		stepper, err := newRealStepperFactory(fixture, nil).StepperForBuild(build)
		Expect(err).NotTo(HaveOccurred())
		step := stepper(build.PrivatePlan())
		succeeded, err := step.Run(
			context.Background(),
			exec.NewRunState(stepper, vars.StaticVariables{}),
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(succeeded).To(BeTrue())
		Expect(build.Finish(db.BuildStatusSucceeded)).To(Succeed())
		Expect(build.Reload()).To(BeTrue())
		Expect(build.Status()).To(Equal(db.BuildStatusSucceeded))

		rows, err := fixture.Conn.Query(`
			SELECT payload
			FROM build_events
			WHERE build_id = $1 AND type = $2
			ORDER BY event_id ASC
		`, build.ID(), event.EventTypeLog)
		Expect(err).NotTo(HaveOccurred())
		defer func() { Expect(rows.Close()).To(Succeed()) }()

		var stderr string
		for rows.Next() {
			var payload []byte
			Expect(rows.Scan(&payload)).To(Succeed())
			var logged event.Log
			Expect(json.Unmarshal(payload, &logged)).To(Succeed())
			if logged.Origin.Source == event.OriginSourceStderr {
				stderr += logged.Payload
			}
		}
		Expect(rows.Err()).NotTo(HaveOccurred())
		messages := []string{
			"pretending to run across-main on prototype some-prototype",
			"pretending to run composite-plan-ran on prototype some-prototype",
			"pretending to run ensure-plan-ran on prototype some-prototype",
		}
		previous := -1
		for _, message := range messages {
			Expect(stderr).To(ContainSubstring(message))
			position := strings.Index(stderr, message)
			Expect(position).To(BeNumerically(">", previous))
			previous = position
		}
	})

	It("composes run plans into persisted run behavior", func() {
		fixture := useEngineDB()
		_, _, _, build := createEngineJobBuild(
			fixture,
			"some-team",
			atc.PipelineRef{Name: "some-pipeline"},
			atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}},
			"some-user",
		)
		runPlan := atc.NewPlanFactory(789).NewPlan(atc.RunPlan{
			Message: "some-message",
			Type:    "some-prototype",
		})

		started, err := build.Start(runPlan)
		Expect(err).NotTo(HaveOccurred())
		Expect(started).To(BeTrue())
		Expect(build.Reload()).To(BeTrue())

		stepper, err := newRealStepperFactory(fixture, nil).StepperForBuild(build)
		Expect(err).NotTo(HaveOccurred())
		step := stepper(build.PrivatePlan())
		succeeded, err := step.Run(
			context.Background(),
			exec.NewRunState(stepper, vars.StaticVariables{}),
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(succeeded).To(BeTrue())

		rows, err := fixture.Conn.Query(`
			SELECT type, payload
			FROM build_events
			WHERE build_id = $1
			ORDER BY event_id ASC
		`, build.ID())
		Expect(err).NotTo(HaveOccurred())
		defer func() { Expect(rows.Close()).To(Succeed()) }()

		var eventTypes []atc.EventType
		var stderr string
		for rows.Next() {
			var eventType atc.EventType
			var payload []byte
			Expect(rows.Scan(&eventType, &payload)).To(Succeed())
			eventTypes = append(eventTypes, eventType)
			if eventType == event.EventTypeLog {
				var logged event.Log
				Expect(json.Unmarshal(payload, &logged)).To(Succeed())
				if logged.Origin.Source == event.OriginSourceStderr {
					stderr += logged.Payload
				}
			}
		}
		Expect(rows.Err()).NotTo(HaveOccurred())
		Expect(eventTypes).To(Equal([]atc.EventType{
			event.EventTypeStatus,
			event.EventTypeInitialize,
			event.EventTypeLog,
			event.EventTypeStart,
			event.EventTypeLog,
			event.EventTypeFinish,
		}))
		Expect(stderr).To(SatisfyAll(
			ContainSubstring("the run step is not yet implemented"),
			ContainSubstring("pretending to run some-message on prototype some-prototype..."),
		))
	})
})
