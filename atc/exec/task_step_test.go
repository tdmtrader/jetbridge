package exec_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"code.cloudfoundry.org/clock"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/engine"
	"github.com/concourse/concourse/atc/event"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/exec/build"
	"github.com/concourse/concourse/atc/imageresolver"
	"github.com/concourse/concourse/atc/imageresolver/imageresolvertesting"
	"github.com/concourse/concourse/atc/policy"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/runtime/runtimetest"
	"github.com/concourse/concourse/atc/worker"
	"github.com/concourse/concourse/tracing"
	"github.com/concourse/concourse/vars"
	"github.com/google/go-containerregistry/pkg/authn"
	"go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func persistedFinishTasks(fixture *execDBFixture, build db.Build) []event.FinishTask {
	GinkgoHelper()
	var finished []event.FinishTask
	for _, e := range execBuildEvents(fixture, build) {
		if finish, ok := e.(event.FinishTask); ok {
			finished = append(finished, finish)
		}
	}
	return finished
}

func persistedStartTasks(fixture *execDBFixture, build db.Build) []event.StartTask {
	GinkgoHelper()
	var started []event.StartTask
	for _, payload := range execBuildEventPayloads(fixture, build, event.EventTypeStartTask) {
		var start event.StartTask
		Expect(json.Unmarshal(payload, &start)).To(Succeed())
		started = append(started, start)
	}
	return started
}

func newTaskStepRegistry() *imageresolvertesting.Registry {
	GinkgoHelper()
	registry := imageresolvertesting.NewRegistry()
	DeferCleanup(registry.Close)
	return registry
}

func pushTaskStepImage(registry *imageresolvertesting.Registry, repository, tag string) string {
	GinkgoHelper()
	digest, err := registry.Push(repository, tag)
	Expect(err).NotTo(HaveOccurred())
	return digest
}

func taskStepHeadRequests(registry *imageresolvertesting.Registry) []imageresolvertesting.Request {
	GinkgoHelper()
	var heads []imageresolvertesting.Request
	for _, request := range registry.DrainRequests() {
		if request.Method == http.MethodHead {
			heads = append(heads, request)
		}
	}
	return heads
}

var _ = Describe("TaskStep", func() {
	var (
		ctx    context.Context
		cancel func()

		workerPool  exec.Pool
		workerSeeds []runtimeWorkerSeed
		streamer    exec.Streamer

		fixture    *execDBFixture
		targetTeam db.Team
		dbBuild    db.Build

		delegateFactory exec.TaskDelegateFactory

		stepper      exec.Stepper
		imageStepper *imageFetchStepper

		taskPlan *atc.TaskPlan

		state exec.RunState
		repo  *build.Repository

		taskStep        exec.Step
		taskStepOptions []exec.TaskStepOption
		stepOk          bool
		stepErr         error

		cpuLimit          = atc.CPULimit(1024)
		memoryLimit       = atc.MemoryLimit(1024)
		containerMetadata = db.ContainerMetadata{
			WorkingDirectory: "some-artifact-root",
			Type:             db.ContainerTypeTask,
			StepName:         "some-step",
		}

		stepMetadata  exec.StepMetadata
		expectedOwner db.ContainerOwner

		planID = atc.PlanID("42")

		defaultTaskTimeout time.Duration = 0
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())

		streamer = worker.NewStreamer(compression.NewGzipCompression())

		fixture = useExecDB()
		var dbJob db.Job
		targetTeam, _, dbJob, dbBuild = createExecJobBuild(
			fixture,
			"some-team",
			atc.PipelineRef{Name: "some-pipeline"},
			atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}},
			"some-user",
		)

		stepMetadata = exec.StepMetadata{
			TeamID:      targetTeam.ID(),
			BuildID:     dbBuild.ID(),
			JobID:       dbJob.ID(),
			ExternalURL: "http://foo.bar",
		}
		expectedOwner = db.NewBuildStepContainerOwner(stepMetadata.BuildID, planID, stepMetadata.TeamID)

		delegateFactory = taskDelegateFactory(func(state exec.RunState) exec.TaskDelegate {
			return engine.NewTaskDelegate(
				dbBuild,
				planID,
				state,
				clock.NewClock(),
				policy.NoopChecker{},
				fixture.WorkerFactory,
				fixture.LockFactory,
			)
		})

		taskStepOptions = nil
		workerPool = nil
		workerSeeds = nil

		imageStepper = new(imageFetchStepper)
		stepper = noopStepper

		state = exec.NewRunState(func(plan atc.Plan) exec.Step {
			return stepper(plan)
		}, vars.StaticVariables{"source-param": "super-secret-source"})
		repo = state.ArtifactRepository()

		taskPlan = &atc.TaskPlan{
			Name:       "some-task",
			Privileged: false,
			ResourceTypes: atc.ResourceTypes{
				{
					Name:   "custom-resource",
					Type:   "custom-type",
					Source: atc.Source{"some-custom": "((source-param))"},
					Params: atc.Params{"some-custom": "param"},
				},
			},
		}
	})

	JustBeforeEach(func() {
		if len(workerSeeds) != 0 {
			workerPool = saveRuntimeWorkerPool(fixture, workerSeeds...)
		}
		plan := atc.Plan{
			ID:   planID,
			Task: taskPlan,
		}

		// stuff stored on task step still
		taskStep = exec.NewTaskStep(
			plan.ID,
			*plan.Task,
			atc.ContainerLimits{},
			atc.ContainerLimits{},
			stepMetadata,
			containerMetadata,
			workerPool,
			streamer,
			delegateFactory,
			defaultTaskTimeout,
			taskStepOptions...,
		)

		stepOk, stepErr = taskStep.Run(ctx, state)
	})

	Context("when the plan has a config", func() {
		var chosenWorker *runtimetest.Worker
		var chosenContainer *runtimetest.WorkerContainer
		var globalWorker *runtimetest.Worker
		var globalContainer *runtimetest.WorkerContainer

		expectExactTeamWorkerRan := func() {
			Expect(chosenContainer.RunningProcesses()).To(HaveLen(1))
			Expect(globalContainer.RunningProcesses()).To(BeEmpty())
		}

		BeforeEach(func() {
			taskPlan.Config = &atc.TaskConfig{
				Platform: "some-platform",
				Limits: &atc.ContainerLimits{
					CPU:    &cpuLimit,
					Memory: &memoryLimit,
				},
				Params: atc.TaskEnv{
					"SECURE": "secret-task-param",
				},
				Run: atc.TaskRunConfig{
					Path: "ls",
					Args: []string{"some", "args"},
				},
			}

			chosenWorker = runtimetest.NewWorker("worker").
				WithContainer(
					expectedOwner,
					runtimetest.NewContainer().WithProcess(
						runtime.ProcessSpec{
							ID:   "task",
							Path: "ls",
							Args: []string{"some", "args"},
							Dir:  "some-artifact-root",
							TTY: &runtime.TTYSpec{
								WindowSize: runtime.WindowSize{
									Columns: 500,
									Rows:    500,
								},
							},
						},
						runtimetest.ProcessStub{Attachable: true},
					),
					nil,
				)
			chosenContainer = chosenWorker.Containers[0]
			globalWorker = runtimetest.NewWorker("global-worker").
				WithContainer(
					expectedOwner,
					runtimetest.NewContainer().WithProcess(
						runtime.ProcessSpec{
							ID:   "task",
							Path: "ls",
							Args: []string{"some", "args"},
							Dir:  "some-artifact-root",
							TTY: &runtime.TTYSpec{
								WindowSize: runtime.WindowSize{
									Columns: 500,
									Rows:    500,
								},
							},
						},
						runtimetest.ProcessStub{Attachable: true},
					),
					nil,
				)
			globalContainer = globalWorker.Containers[0]
			workerSeeds = []runtimeWorkerSeed{
				{Model: chosenWorker, Team: targetTeam},
				{Model: globalWorker},
			}
		})

		It("Task env includes atc external url and build identity", func() {
			Expect(chosenContainer.Spec.Env).To(ConsistOf(
				"ATC_EXTERNAL_URL=http://foo.bar",
				fmt.Sprintf("BUILD_ID=%d", stepMetadata.BuildID),
				"SECURE=secret-task-param",
			))
		})

		Describe("worker selection", func() {
			It("emits a SelectedWorker event", func() {
				Expect(persistedSelectedWorkers(fixture, dbBuild)).To(Equal([]string{"worker"}))
			})

			It("runs on the exact-team worker and leaves the global worker idle", func() {
				expectExactTeamWorkerRan()
			})

			Context("when selecting a worker fails", func() {
				BeforeEach(func() {
					otherTeam, err := fixture.TeamFactory.CreateTeam(atc.Team{Name: "other-team"})
					Expect(err).NotTo(HaveOccurred())
					workerSeeds = []runtimeWorkerSeed{{Model: chosenWorker, Team: otherTeam}}
				})

				It("returns the no-compatible-worker error", func() {
					Expect(stepErr).To(MatchError(worker.ErrNoWorkers))
				})
			})
		})

		It("persists the task config in the start event", func() {
			started := persistedStartTasks(fixture, dbBuild)
			Expect(started).To(HaveLen(1))
			Expect(started[0].Origin).To(Equal(event.Origin{ID: event.OriginID(planID)}))
			Expect(started[0].TaskConfig).To(Equal(event.TaskConfig{
				Platform: "some-platform",
				Run: event.TaskRunConfig{
					Path: "ls",
					Args: []string{"some", "args"},
				},
			}))
		})

		Context("when privileged", func() {
			BeforeEach(func() {
				taskPlan.Privileged = true
			})

			It("marks the container's image spec as privileged", func() {
				Expect(chosenContainer.Spec.ImageSpec.Privileged).To(BeTrue())
			})
		})

		It("uses the correct container limits", func() {
			Expect(atc.CPULimit(*chosenContainer.Spec.Limits.CPU)).To(Equal(atc.CPULimit(1024)))
			Expect(atc.MemoryLimit(*chosenContainer.Spec.Limits.Memory)).To(Equal(atc.MemoryLimit(1024)))
		})

		Context("when toplevel limits are set", func() {
			BeforeEach(func() {
				cpu := atc.CPULimit(2048)
				memory := atc.MemoryLimit(2048)
				taskPlan.Limits = &atc.ContainerLimits{
					CPU:    &cpu,
					Memory: &memory,
				}
			})

			It("overrides the limits from the config", func() {
				Expect(atc.CPULimit(*chosenContainer.Spec.Limits.CPU)).To(Equal(atc.CPULimit(2048)))
				Expect(atc.MemoryLimit(*chosenContainer.Spec.Limits.Memory)).To(Equal(atc.MemoryLimit(2048)))
			})
		})

		Context("when hermetic is configured", func() {
			BeforeEach(func() {
				taskPlan.Hermetic = true
			})

			It("returns the context.Canceled error", func() {
				Expect(chosenContainer.Spec.Hermetic).To(Equal(true))
			})
		})

		Context("when a timeout is configured", func() {
			BeforeEach(func() {
				taskPlan.Timeout = "1ms"

				chosenContainer.ProcessDefs[0].Stub.Do = func(ctx context.Context, _ *runtimetest.Process) error {
					select {
					case <-ctx.Done():
						return fmt.Errorf("wrapped: %w", ctx.Err())
					case <-time.After(100 * time.Millisecond):
						return nil
					}
				}
			})

			It("fails without error", func() {
				Expect(stepOk).To(BeFalse())
				Expect(stepErr).To(BeNil())
			})

			It("emits an Errored event", func() {
				Expect(execBuildErrorMessages(fixture, dbBuild)).To(Equal([]string{exec.TimeoutLogMessage}))
			})

			Context("when the timeout is bogus", func() {
				BeforeEach(func() {
					taskPlan.Timeout = "bogus"
				})

				It("fails miserably", func() {
					Expect(stepErr).To(MatchError("parse timeout: time: invalid duration \"bogus\""))
				})
			})
		})

		Context("when there is default task timeout", func() {
			BeforeEach(func() {
				defaultTaskTimeout = time.Minute * 30
			})

			It("enforces it on the task", func() {
				t, ok := chosenContainer.ContextOfRun().Deadline()
				Expect(ok).To(BeTrue())
				Expect(t).To(BeTemporally("~", time.Now().Add(time.Minute*30), time.Minute))
			})
		})

		Context("when there is default task timeout and the plan specifies a timeout also", func() {
			BeforeEach(func() {
				defaultTaskTimeout = time.Minute * 30
				taskPlan.Timeout = "1h"
			})

			It("enforces the plan's timeout on the task step", func() {
				t, ok := chosenContainer.ContextOfRun().Deadline()
				Expect(ok).To(BeTrue())
				Expect(t).To(BeTemporally("~", time.Now().Add(time.Hour), time.Minute))
			})
		})

		Context("when rootfs uri is set instead of image resource", func() {
			BeforeEach(func() {
				taskPlan.Config.RootfsURI = "some-image"
			})

			It("correctly sets up the image spec", func() {
				Expect(chosenContainer.Spec.ImageSpec).To(Equal(runtime.ImageSpec{
					ImageURL:   "some-image",
					Privileged: false,
				}))
			})
		})

		Context("when tracing is enabled", func() {
			var spanRecorder *tracetest.SpanRecorder
			var runSpanContext oteltrace.SpanContext

			BeforeEach(func() {
				defaultTaskTimeout = 0
				spanRecorder = new(tracetest.SpanRecorder)
				tp := trace.NewTracerProvider(trace.WithSpanProcessor(spanRecorder), trace.WithSyncer(tracetest.NewInMemoryExporter()))
				tracing.ConfigureTraceProvider(tp)

				chosenContainer.ProcessDefs[0].Stub.Do = func(ctx context.Context, _ *runtimetest.Process) error {
					runSpanContext = tracing.FromContext(ctx).SpanContext()
					return nil
				}
			})

			AfterEach(func() {
				tracing.Configured = false
			})

			It("populates the TRACEPARENT env var", func() {
				Expect(chosenContainer.Spec.Env).To(ContainElement(MatchRegexp(`TRACEPARENT=.+`)))
			})

			It("propagates the step span into the container run", func() {
				ended := spanRecorder.Ended()
				Expect(ended).To(HaveLen(1))
				Expect(runSpanContext).To(Equal(ended[0].SpanContext()))
			})

			It("adds state-transition span events", func() {
				ended := spanRecorder.Ended()
				Expect(ended).To(HaveLen(1))
				eventNames := []string{}
				for _, e := range ended[0].Events() {
					eventNames = append(eventNames, e.Name)
				}
				Expect(eventNames).To(ContainElement("step.initializing"))
				Expect(eventNames).To(ContainElement("step.starting"))
				Expect(eventNames).To(ContainElement("step.finished"))
			})
		})

		Context("when the configuration specifies paths for inputs", func() {
			var input1 *runtimetest.Volume
			var input2 *runtimetest.Volume

			BeforeEach(func() {
				input1 = runtimetest.NewVolume("input1")
				input2 = runtimetest.NewVolume("input2")

				taskPlan.Config.Inputs = []atc.TaskInputConfig{
					{Name: "some-input", Path: "some-input-configured-path"},
					{Name: "some-other-input"},
				}
			})

			Context("when all inputs are present", func() {
				var input3 *runtimetest.Volume
				var input4 *runtimetest.Volume

				BeforeEach(func() {
					input3 = runtimetest.NewVolume("input3")
					input4 = runtimetest.NewVolume("input4")

					// If or not framCache when RegisterArtifact, that should not impact task execution.
					repo.RegisterArtifact("some-input", input1, false)
					repo.RegisterArtifact("some-other-input", input2, true)
					repo.RegisterArtifact("absolute-input", input3, false)
					repo.RegisterArtifact("parent-dirs-input", input4, false)

					taskPlan.Config.Inputs = []atc.TaskInputConfig{
						{Name: "some-input", Path: "some-input-configured-path"},
						{Name: "some-other-input"},
						{Name: "absolute-input", Path: "/absolute-input"},
						{Name: "parent-dirs-input", Path: "../parent-dirs/../input"},
					}
				})

				It("configures the inputs for the containerSpec correctly", func() {
					Expect(chosenContainer.Spec.Inputs).To(ConsistOf([]runtime.Input{
						{
							Artifact:        input1,
							DestinationPath: "some-artifact-root/some-input-configured-path",
							FromCache:       false,
						},
						{
							Artifact:        input2,
							DestinationPath: "some-artifact-root/some-other-input",
							FromCache:       true,
						},
						{
							Artifact:        input3,
							DestinationPath: "/absolute-input",
							FromCache:       false,
						},
						{
							Artifact:        input4,
							DestinationPath: "some-artifact-root/parent-dirs/input",
							FromCache:       false,
						},
					}))
					Expect(stepErr).ToNot(HaveOccurred())
				})
			})

			Context("when any of the inputs are missing", func() {
				BeforeEach(func() {
					repo.RegisterArtifact("some-input", input1, false)
				})

				It("returns a MissingInputsError", func() {
					Expect(stepErr).To(BeAssignableToTypeOf(exec.MissingInputsError{}))
					Expect(stepErr.(exec.MissingInputsError).Inputs).To(ConsistOf("some-other-input"))
				})
			})

			Context("when only inputs are cache", func() {
				BeforeEach(func() {
					// If or not framCache when RegisterArtifact, that should not impact task execution.
					repo.RegisterArtifact("some-input", input1, true)
					repo.RegisterArtifact("some-other-input", input2, true)
				})

				It("configures the inputs for the containerSpec correctly", func() {
					Expect(chosenContainer.Spec.Inputs).To(ConsistOf([]runtime.Input{
						{
							Artifact:        input1,
							DestinationPath: "some-artifact-root/some-input-configured-path",
							FromCache:       true,
						},
						{
							Artifact:        input2,
							DestinationPath: "some-artifact-root/some-other-input",
							FromCache:       true,
						},
					}))
					Expect(stepErr).ToNot(HaveOccurred())
				})
			})
		})

		Context("when input is remapped", func() {
			var remappedInputArtifact *runtimetest.Volume

			BeforeEach(func() {
				remappedInputArtifact = runtimetest.NewVolume("input1")
				taskPlan.InputMapping = map[string]string{"remapped-input": "remapped-input-src"}
				taskPlan.Config.Inputs = []atc.TaskInputConfig{
					{Name: "remapped-input"},
				}
			})

			Context("when all inputs are present in the in artifact repository", func() {
				BeforeEach(func() {
					repo.RegisterArtifact("remapped-input-src", remappedInputArtifact, false)
				})

				It("uses remapped input", func() {
					Expect(chosenContainer.Spec.Inputs).To(ConsistOf([]runtime.Input{
						{
							Artifact:        remappedInputArtifact,
							DestinationPath: "some-artifact-root/remapped-input",
						},
					}))
					Expect(stepErr).ToNot(HaveOccurred())
				})
			})
		})

		Context("when some inputs are optional", func() {
			var (
				optionalInputArtifact, optionalInput2Artifact, requiredInputArtifact *runtimetest.Volume
			)

			BeforeEach(func() {
				optionalInputArtifact = runtimetest.NewVolume("optional1")
				optionalInput2Artifact = runtimetest.NewVolume("optional2")
				requiredInputArtifact = runtimetest.NewVolume("required1")
				taskPlan.Config.Inputs = []atc.TaskInputConfig{
					{Name: "optional-input", Optional: true},
					{Name: "optional-input-2", Optional: true},
					{Name: "required-input"},
				}
			})

			Context("when an optional input is missing", func() {
				BeforeEach(func() {
					repo.RegisterArtifact("required-input", requiredInputArtifact, false)
					repo.RegisterArtifact("optional-input-2", optionalInput2Artifact, false)
				})

				It("runs successfully without the optional input", func() {
					Expect(chosenContainer.Spec.Inputs).To(ConsistOf([]runtime.Input{
						{
							Artifact:        requiredInputArtifact,
							DestinationPath: "some-artifact-root/required-input",
						},
						{
							Artifact:        optionalInput2Artifact,
							DestinationPath: "some-artifact-root/optional-input-2",
						},
					}))
					Expect(stepErr).ToNot(HaveOccurred())
				})
			})

			Context("when a required input is missing", func() {
				BeforeEach(func() {
					repo.RegisterArtifact("optional-input", optionalInputArtifact, false)
					repo.RegisterArtifact("optional-input-2", optionalInput2Artifact, false)
				})

				It("returns a MissingInputsError", func() {
					Expect(stepErr).To(BeAssignableToTypeOf(exec.MissingInputsError{}))
					Expect(stepErr.(exec.MissingInputsError).Inputs).To(ConsistOf("required-input"))
				})
			})
		})

		Context("when the configuration specifies paths for caches", func() {
			var (
				volume1 *runtimetest.Volume
				volume2 *runtimetest.Volume
			)

			BeforeEach(func() {
				taskPlan.Config.Caches = []atc.TaskCacheConfig{
					{Path: "some-path-1"},
					{Path: "some-path-2"},
				}

				volume1 = runtimetest.NewVolume("volume1")
				volume2 = runtimetest.NewVolume("volume2")

				chosenContainer.Mounts = []runtime.VolumeMount{
					{
						Volume:    volume1,
						MountPath: "some-artifact-root/some-path-1",
					},
					{
						Volume:    volume2,
						MountPath: "some-artifact-root/some-path-2",
					},
				}
			})

			It("creates the containerSpec with the caches", func() {
				Expect(chosenContainer.Spec.Caches).To(ConsistOf("some-path-1", "some-path-2"))
			})

			itRegistersCaches := func(didRegister bool) {
				It("registers cache volumes as task caches", func() {
					Expect(volume1.TaskCacheInitialized).To(Equal(didRegister))
					Expect(volume2.TaskCacheInitialized).To(Equal(didRegister))
				})
			}

			Context("when task belongs to a job", func() {
				BeforeEach(func() {
					stepMetadata.JobID = 12
				})

				Context("when the task succeeds", func() {
					itRegistersCaches(true)
				})

				Context("when the task exits nonzero", func() {
					BeforeEach(func() {
						chosenContainer.ProcessDefs[0].Stub.ExitStatus = 1
					})

					itRegistersCaches(true)
				})

				Context("when the task errors", func() {
					BeforeEach(func() {
						chosenContainer.ProcessDefs[0].Stub.Err = "blah"
					})

					itRegistersCaches(true)
				})
			})

			Context("when task does not belong to job (one-off build)", func() {
				BeforeEach(func() {
					stepMetadata.JobID = 0
				})

				It("does not error", func() {
					Expect(stepErr).ToNot(HaveOccurred())
				})

				itRegistersCaches(false)
			})
		})

		Context("when the configuration specifies paths for outputs", func() {
			var outputVolume1, outputVolume2, outputVolume3,
				outputVolume4, outputVolume5 *runtimetest.Volume

			BeforeEach(func() {
				taskPlan.Config.Outputs = []atc.TaskOutputConfig{
					{Name: "some-output", Path: "some-output-configured-path"},
					{Name: "some-other-output"},
					{Name: "some-trailing-slash-output", Path: "some-output-configured-path-with-trailing-slash/"},
					{Name: "absolute-output", Path: "/absolute/output"},
					{Name: "parent-dir-output", Path: "../parent/dir/../output"},
				}
				taskPlan.OutputMapping = map[string]string{
					"some-other-output": "some-remapped-output",
				}

				outputVolume1 = runtimetest.NewVolume("output1")
				outputVolume2 = runtimetest.NewVolume("output2")
				outputVolume3 = runtimetest.NewVolume("output3")
				outputVolume4 = runtimetest.NewVolume("output4")
				outputVolume5 = runtimetest.NewVolume("output5")

				chosenContainer.Mounts = []runtime.VolumeMount{
					{
						Volume:    outputVolume1,
						MountPath: "some-artifact-root/some-output-configured-path/",
					},
					{
						Volume:    outputVolume2,
						MountPath: "some-artifact-root/some-other-output/",
					},
					{
						Volume:    outputVolume3,
						MountPath: "some-artifact-root/some-output-configured-path-with-trailing-slash/",
					},
					{
						Volume:    outputVolume4,
						MountPath: "/absolute/output/",
					},
					{
						Volume:    outputVolume5,
						MountPath: "some-artifact-root/parent/dir/output/",
					},
				}
			})

			It("configures them appropriately in the container spec", func() {
				Expect(chosenContainer.Spec.Outputs).To(Equal(runtime.OutputPaths{
					"some-output":                "some-artifact-root/some-output-configured-path/",
					"some-other-output":          "some-artifact-root/some-other-output/",
					"some-trailing-slash-output": "some-artifact-root/some-output-configured-path-with-trailing-slash/",
					"absolute-output":            "/absolute/output/",
					"parent-dir-output":          "some-artifact-root/parent/dir/output/",
				}))
			})

			It("registers the outputs in the build repo", func() {
				Expect(repo.AsMap()).To(Equal(map[build.ArtifactName]build.ArtifactEntry{
					"some-output": {
						Artifact:  outputVolume1,
						FromCache: false,
					},
					"some-remapped-output": {
						Artifact:  outputVolume2,
						FromCache: false,
					},
					"some-trailing-slash-output": {
						Artifact:  outputVolume3,
						FromCache: false,
					},
					"absolute-output": {
						Artifact:  outputVolume4,
						FromCache: false,
					},
					"parent-dir-output": {
						Artifact:  outputVolume5,
						FromCache: false,
					},
				}))
			})
		})

		Context("when missing the platform", func() {
			BeforeEach(func() {
				taskPlan.Config.Platform = ""
			})

			It("returns the error", func() {
				Expect(stepErr).To(HaveOccurred())
			})

			It("is not successful", func() {
				Expect(stepOk).To(BeFalse())
			})
		})

		Context("when missing the path to the executable and ENTRYPOINT/CMD is not defined", func() {
			BeforeEach(func() {
				taskPlan.Config.Run.Path = ""
			})

			It("returns the error", func() {
				Expect(stepErr).To(HaveOccurred())
				Expect(stepErr.Error()).To(ContainSubstring("No executable defined for task. Specify an executable to run in either the task config (run.path) or as an ENTRYPOINT/CMD in the container image."))
			})

			It("is not successful", func() {
				Expect(stepOk).To(BeFalse())
			})

			Context("when the image's metadata.json is malformed", func() {
				BeforeEach(func() {
					taskPlan.Config.Run.Path = ""

					taskPlan.ImageArtifactName = "some-image-artifact"
					imageVolume := runtimetest.NewVolume("image-volume").WithContent(runtimetest.VolumeContent{
						"metadata.json": {Data: []byte("definitely not json")},
					})
					repo.RegisterArtifact("some-image-artifact", imageVolume, false)
				})

				It("returns the error", func() {
					Expect(stepErr).To(HaveOccurred())
					Expect(stepErr.Error()).To(ContainSubstring("error parsing metadata.json from rootfs"))
				})

				It("is not successful", func() {
					Expect(stepOk).To(BeFalse())
				})

			})

			Context("when the image's metadata.json is not present in the image artifact", func() {
				BeforeEach(func() {
					taskPlan.Config.Run.Path = ""

					taskPlan.ImageArtifactName = "some-image-artifact"
					imageVolume := runtimetest.NewVolume("image-volume")
					repo.RegisterArtifact("some-image-artifact", imageVolume, false)
				})

				It("returns the error", func() {
					Expect(stepErr).To(MatchError(exec.FileNotFoundError{
						Name:     "image-volume",
						FilePath: "metadata.json",
					}))
				})

				It("is not successful", func() {
					Expect(stepOk).To(BeFalse())
				})

			})
		})

		Context("the task container has an ENTRYPOINT/CMD defined", func() {
			BeforeEach(func() {
				taskPlan.Config.Run.Path = ""
				taskPlan.Config.Run.Args = []string{}
				rootfsMetadata := `{
  "entrypoint": [
    "/bin/sh",
    "-c"
  ],
  "cmd": [
    "echo hello world"
  ]
}`
				taskPlan.ImageArtifactName = "some-image-artifact"
				imageVolume := runtimetest.NewVolume("image-volume").WithContent(runtimetest.VolumeContent{
					"metadata.json": {Data: []byte(rootfsMetadata)},
				})
				repo.RegisterArtifact("some-image-artifact", imageVolume, false)
			})

			It("persists the ENTRYPOINT/CMD in the start event", func() {
				started := persistedStartTasks(fixture, dbBuild)
				Expect(started).To(HaveLen(1))
				Expect(started[0].TaskConfig.Run.Path).To(Equal("/bin/sh"))
				Expect(started[0].TaskConfig.Run.Args).To(Equal([]string{"-c", "echo hello world"}))
			})

			Context("there are args in the task config", func() {
				BeforeEach(func() {
					taskPlan.Config.Run.Args = []string{"task", "args"}
				})

				It("they are appended to the ENTRYPOINT/CMD args", func() {
					started := persistedStartTasks(fixture, dbBuild)
					Expect(started).To(HaveLen(1))
					Expect(started[0].TaskConfig.Run.Path).To(Equal("/bin/sh"))
					Expect(started[0].TaskConfig.Run.Args).To(Equal([]string{"-c", "echo hello world", "task", "args"}))
				})
			})

			Context("only ENTRYPOINT is specified", func() {
				BeforeEach(func() {
					taskPlan.Config.Run.Path = ""
					taskPlan.Config.Run.Args = []string{}
					rootfsMetadata := `{ "entrypoint": [ "some-program", "some-arg" ] }`
					repo.RegisterArtifact("some-image-artifact", runtimetest.NewVolume("image-volume").WithContent(runtimetest.VolumeContent{
						"metadata.json": {Data: []byte(rootfsMetadata)},
					}), false)
				})

				It("is parsed correctly", func() {
					started := persistedStartTasks(fixture, dbBuild)
					Expect(started).To(HaveLen(1))
					Expect(started[0].TaskConfig.Run.Path).To(Equal("some-program"))
					Expect(started[0].TaskConfig.Run.Args).To(Equal([]string{"some-arg"}))
				})
			})

			Context("only CMD is specified", func() {
				BeforeEach(func() {
					taskPlan.Config.Run.Path = ""
					taskPlan.Config.Run.Args = []string{}
					rootfsMetadata := `{ "cmd": [ "cmd-program", "cmd-arg" ] }`
					repo.RegisterArtifact("some-image-artifact", runtimetest.NewVolume("image-volume").WithContent(runtimetest.VolumeContent{
						"metadata.json": {Data: []byte(rootfsMetadata)},
					}), false)
				})

				It("is parsed correctly", func() {
					started := persistedStartTasks(fixture, dbBuild)
					Expect(started).To(HaveLen(1))
					Expect(started[0].TaskConfig.Run.Path).To(Equal("cmd-program"))
					Expect(started[0].TaskConfig.Run.Args).To(Equal([]string{"cmd-arg"}))
				})
			})
		})

		Context("when an image artifact name is specified", func() {
			BeforeEach(func() {
				taskPlan.ImageArtifactName = "some-image-artifact"
			})

			Context("when the image artifact is registered in the artifact repo", func() {
				var imageVolume *runtimetest.Volume

				BeforeEach(func() {
					imageVolume = runtimetest.NewVolume("image-volume")
					repo.RegisterArtifact("some-image-artifact", imageVolume, false)
				})

				It("configures it in the containerSpec's ImageSpec", func() {
					Expect(chosenContainer.Spec.ImageSpec).To(Equal(runtime.ImageSpec{
						ImageArtifact: imageVolume,
					}))
					expectExactTeamWorkerRan()
				})

				Describe("when task config specifies image and/or image resource as well as image artifact", func() {
					Context("when streaming the metadata from the worker succeeds", func() {
						JustBeforeEach(func() {
							Expect(stepErr).ToNot(HaveOccurred())
						})

						Context("when the task config also specifies a rootfs_uri", func() {
							BeforeEach(func() {
								taskPlan.Config.RootfsURI = "some-image"
							})

							It("still uses the image artifact", func() {
								Expect(chosenContainer.Spec.ImageSpec).To(Equal(runtime.ImageSpec{
									ImageArtifact: imageVolume,
								}))
								expectExactTeamWorkerRan()
							})
						})

						Context("when the task config also specifies image_resource", func() {
							BeforeEach(func() {
								taskPlan.Config.ImageResource = &atc.ImageResource{
									Type:    "docker",
									Source:  atc.Source{"some": "super-secret-source"},
									Params:  atc.Params{"some": "params"},
									Version: atc.Version{"some": "version"},
								}
							})

							It("still uses the image artifact", func() {
								Expect(chosenContainer.Spec.ImageSpec).To(Equal(runtime.ImageSpec{
									ImageArtifact: imageVolume,
								}))
								expectExactTeamWorkerRan()
							})
						})
					})
				})
			})

			Context("when the image artifact is NOT registered in the artifact repo", func() {
				It("returns a MissingTaskImageSourceError", func() {
					Expect(stepErr).To(Equal(exec.MissingTaskImageSourceError{"some-image-artifact"}))
				})

				It("is not successful", func() {
					Expect(stepOk).To(BeFalse())
				})
			})
		})

		Context("when image artifact is from a short-circuited get step (nil artifact with image ref)", func() {
			BeforeEach(func() {
				taskPlan.ImageArtifactName = "some-image-artifact"

				// Simulate what the get step short-circuit does:
				// register nil artifact + image ref URL
				repo.RegisterArtifact("some-image-artifact", nil, false)
				repo.RegisterImageRef("some-image-artifact", "docker:///myrepo/myimage@sha256:abc123")
			})

			It("uses the image ref URL as ImageURL in the containerSpec", func() {
				Expect(stepErr).ToNot(HaveOccurred())
				Expect(stepOk).To(BeTrue())
				Expect(chosenContainer.Spec.ImageSpec).To(Equal(runtime.ImageSpec{
					ImageURL: "docker:///myrepo/myimage@sha256:abc123",
				}))
			})

		})

		Context("when image artifact has both a volume and an image ref (full get of registry-image)", func() {
			BeforeEach(func() {
				taskPlan.ImageArtifactName = "some-image-artifact"
				imageVolume := runtimetest.NewVolume("image-volume")
				repo.RegisterArtifact("some-image-artifact", imageVolume, false)
				repo.RegisterImageRef("some-image-artifact", "docker:///myrepo/myimage@sha256:abc123")
			})

			It("prefers the image ref URL over the artifact volume", func() {
				Expect(chosenContainer.Spec.ImageSpec).To(Equal(runtime.ImageSpec{
					ImageURL: "docker:///myrepo/myimage@sha256:abc123",
				}))
			})
		})

		Context("when image artifact has a volume but no image ref (non-registry-image type)", func() {
			var imageVolume *runtimetest.Volume

			BeforeEach(func() {
				taskPlan.ImageArtifactName = "some-image-artifact"
				imageVolume = runtimetest.NewVolume("image-volume")
				repo.RegisterArtifact("some-image-artifact", imageVolume, false)
				// No image ref registered — this is a non-registry resource type
			})

			It("falls back to using the artifact volume", func() {
				Expect(chosenContainer.Spec.ImageSpec).To(Equal(runtime.ImageSpec{
					ImageArtifact: imageVolume,
				}))
			})
		})

		Context("when the image_resource is specified (even if rootfs_uri is configured)", func() {
			var fetchedImageArtifact *runtimetest.Volume

			BeforeEach(func() {
				taskPlan.Config.RootfsURI = "some-image"
				taskPlan.Config.ImageResource = &atc.ImageResource{
					Type:   "docker",
					Source: atc.Source{"some": "super-secret-source"},
					Params: atc.Params{"some": "params"},
				}
				taskPlan.Tags = atc.Tags{"some", "tags"}

				fetchedImageArtifact = runtimetest.NewVolume("some-volume")
				imageStepper.artifact = fetchedImageArtifact
				stepper = imageStepper.step
			})

			It("succeeds with the fetched image and persists its fetch event", func() {
				Expect(stepErr).ToNot(HaveOccurred())
				Expect(stepOk).To(BeTrue())
				Expect(execBuildEventTypes(fixture, dbBuild)).To(ContainElement(event.EventTypeImageGet))
				Expect(chosenContainer.Spec.ImageSpec).To(Equal(runtime.ImageSpec{
					ImageArtifact: fetchedImageArtifact,
					ResourceType:  "image",
				}))
			})

			Context("when privileged", func() {
				BeforeEach(func() {
					taskPlan.Privileged = true
				})

				It("fetches a privileged image", func() {
					Expect(chosenContainer.Spec.ImageSpec.Privileged).To(BeTrue())
				})
			})

			Context("when check skip interval is true", func() {
				BeforeEach(func() {
					taskPlan.CheckSkipInterval = true
					stepper = func(plan atc.Plan) exec.Step {
						return stepFunc(func(_ context.Context, fetchState exec.RunState) (bool, error) {
							if plan.Check != nil {
								if !plan.Check.SkipInterval {
									return false, nil
								}
								fetchState.StoreResult(plan.ID, true)
								return true, nil
							}

							if plan.Get != nil {
								if plan.Get.VersionFrom == nil {
									return false, nil
								}
								var forcedCheck bool
								if !fetchState.Result(*plan.Get.VersionFrom, &forcedCheck) || !forcedCheck {
									return false, nil
								}
								fetchState.StoreResult(plan.ID, exec.GetResult{Name: plan.Get.Name})
								fetchState.ArtifactRepository().RegisterArtifact(
									build.ArtifactName(plan.Get.Name),
									fetchedImageArtifact,
									false,
								)
							}

							return true, nil
						})
					}
				})

				It("runs with the image fetched by the forced check plan", func() {
					Expect(stepErr).NotTo(HaveOccurred())
					Expect(stepOk).To(BeTrue())
					Expect(execBuildEventTypes(fixture, dbBuild)).To(ContainElements(
						event.EventTypeImageCheck,
						event.EventTypeImageGet,
					))
					Expect(chosenContainer.Spec.ImageSpec).To(Equal(runtime.ImageSpec{
						ImageArtifact: fetchedImageArtifact,
						ResourceType:  "image",
					}))
				})
			})
		})

		Context("when a run dir and user are specified", func() {
			BeforeEach(func() {
				taskPlan.Config.Run.Dir = "/some/dir"
				taskPlan.Config.Run.User = "some-user"

				chosenContainer.ProcessDefs[0].Spec.Dir = "/some/dir"
				chosenContainer.ProcessDefs[0].Spec.User = "some-user"
			})

			It("runs with the specified dir and user", func() {
				Expect(stepErr).ToNot(HaveOccurred())
				Expect(chosenContainer.RunningProcesses()).To(HaveLen(1))
			})
		})

		Context("when running the task exits with a non-zero status", func() {
			BeforeEach(func() {
				chosenContainer.ProcessDefs[0].Stub.ExitStatus = 1
			})

			It("doesn't error", func() {
				Expect(stepErr).ToNot(HaveOccurred())
			})

			It("is not successful", func() {
				Expect(stepOk).To(BeFalse())
			})

			It("finishes the step", func() {
				finished := persistedFinishTasks(fixture, dbBuild)
				Expect(finished).To(HaveLen(1))
				Expect(finished[0].ExitStatus).To(Equal(1))
			})
		})

		Context("when running the task fails", func() {
			BeforeEach(func() {
				chosenContainer.ProcessDefs[0].Stub.Err = "failed to run the task"
			})

			It("returns the error", func() {
				Expect(stepErr).To(HaveOccurred())
			})

			It("is not successful", func() {
				Expect(stepOk).To(BeFalse())
			})
		})

		Context("when the task step is interrupted", func() {
			BeforeEach(func() {
				cancel()
				chosenContainer.ProcessDefs[0].Stub.Do = func(ctx context.Context, _ *runtimetest.Process) error {
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-time.After(1 * time.Second):
						Fail("didn't return context error")
						panic("unreachable")
					}
				}
			})

			It("returns the context.Canceled error", func() {
				Expect(stepErr).To(Equal(context.Canceled))
			})

			It("is not successful", func() {
				Expect(stepOk).To(BeFalse())
			})

			It("doesn't register a artifact", func() {
				Expect(repo.AsMap()).To(BeEmpty())
			})
		})

		Context("when sidecars are configured", func() {
			BeforeEach(func() {
				sidecarYAML := `
- name: postgres
  image: postgres:15
  env:
  - name: POSTGRES_PASSWORD
    value: test
  ports:
  - containerPort: 5432
`
				sidecarVolume := runtimetest.NewVolume("sidecar-source").WithContent(runtimetest.VolumeContent{
					"ci/sidecars/postgres.yml": {Data: []byte(sidecarYAML)},
				})
				repo.RegisterArtifact("my-repo", sidecarVolume, false)

				taskPlan.Sidecars = []atc.SidecarSource{{File: "my-repo/ci/sidecars/postgres.yml"}}
			})

			It("loads sidecars into the container spec", func() {
				Expect(stepErr).ToNot(HaveOccurred())
				Expect(chosenContainer.Spec.Sidecars).To(HaveLen(1))
				Expect(chosenContainer.Spec.Sidecars[0].Name).To(Equal("postgres"))
				Expect(chosenContainer.Spec.Sidecars[0].Image).To(Equal("postgres:15"))
			})
		})

		Context("when sidecars are defined inline", func() {
			BeforeEach(func() {
				taskPlan.Sidecars = []atc.SidecarSource{
					{Config: &atc.SidecarConfig{
						Name:  "redis",
						Image: "redis:7",
						Ports: []atc.SidecarPort{{ContainerPort: 6379}},
					}},
				}
			})

			It("includes inline sidecars in the container spec", func() {
				Expect(stepErr).ToNot(HaveOccurred())
				Expect(chosenContainer.Spec.Sidecars).To(HaveLen(1))
				Expect(chosenContainer.Spec.Sidecars[0].Name).To(Equal("redis"))
				Expect(chosenContainer.Spec.Sidecars[0].Image).To(Equal("redis:7"))
			})
		})

		Context("when sidecars are a mix of file and inline", func() {
			BeforeEach(func() {
				sidecarYAML := `
- name: postgres
  image: postgres:15
  ports:
  - containerPort: 5432
`
				sidecarVolume := runtimetest.NewVolume("sidecar-source").WithContent(runtimetest.VolumeContent{
					"ci/sidecars/postgres.yml": {Data: []byte(sidecarYAML)},
				})
				repo.RegisterArtifact("my-repo", sidecarVolume, false)

				taskPlan.Sidecars = []atc.SidecarSource{
					{File: "my-repo/ci/sidecars/postgres.yml"},
					{Config: &atc.SidecarConfig{
						Name:  "redis",
						Image: "redis:7",
					}},
				}
			})

			It("loads both file and inline sidecars", func() {
				Expect(stepErr).ToNot(HaveOccurred())
				Expect(chosenContainer.Spec.Sidecars).To(HaveLen(2))
				Expect(chosenContainer.Spec.Sidecars[0].Name).To(Equal("postgres"))
				Expect(chosenContainer.Spec.Sidecars[1].Name).To(Equal("redis"))
			})
		})

		Context("when inline sidecar has a duplicate name with a file sidecar", func() {
			BeforeEach(func() {
				sidecarYAML := `
- name: postgres
  image: postgres:15
`
				sidecarVolume := runtimetest.NewVolume("sidecar-source").WithContent(runtimetest.VolumeContent{
					"ci/sidecars/postgres.yml": {Data: []byte(sidecarYAML)},
				})
				repo.RegisterArtifact("my-repo", sidecarVolume, false)

				taskPlan.Sidecars = []atc.SidecarSource{
					{File: "my-repo/ci/sidecars/postgres.yml"},
					{Config: &atc.SidecarConfig{
						Name:  "postgres",
						Image: "postgres:16",
					}},
				}
			})

			It("returns a duplicate name error", func() {
				Expect(stepErr).To(HaveOccurred())
				Expect(stepErr.Error()).To(ContainSubstring("duplicate sidecar name"))
			})
		})

		Context("when inline sidecar uses a reserved name", func() {
			BeforeEach(func() {
				taskPlan.Sidecars = []atc.SidecarSource{
					{Config: &atc.SidecarConfig{
						Name:  "main",
						Image: "redis:7",
					}},
				}
			})

			It("returns a reserved name error", func() {
				Expect(stepErr).To(HaveOccurred())
				Expect(stepErr.Error()).To(ContainSubstring("reserved container name"))
			})
		})

		Context("when inline sidecar is missing required fields", func() {
			BeforeEach(func() {
				taskPlan.Sidecars = []atc.SidecarSource{
					{Config: &atc.SidecarConfig{
						Name: "redis",
					}},
				}
			})

			It("returns a validation error", func() {
				Expect(stepErr).To(HaveOccurred())
				Expect(stepErr.Error()).To(ContainSubstring("missing 'image'"))
			})
		})

		Context("when a sidecar uses image_artifact reference", func() {
			BeforeEach(func() {
				// Register an image ref in the artifact repository (simulates a prior build step)
				repo.RegisterImageRef("my-db-image", "docker:///myrepo/mydb@sha256:abc123def456")

				taskPlan.Sidecars = []atc.SidecarSource{
					{Config: &atc.SidecarConfig{
						Name:          "postgres",
						ImageArtifact: "my-db-image",
						Ports:         []atc.SidecarPort{{ContainerPort: 5432}},
					}},
				}
			})

			It("resolves the artifact ref and uses it as the sidecar image", func() {
				Expect(stepErr).ToNot(HaveOccurred())
				Expect(chosenContainer.Spec.Sidecars).To(HaveLen(1))
				Expect(chosenContainer.Spec.Sidecars[0].Name).To(Equal("postgres"))
				Expect(chosenContainer.Spec.Sidecars[0].Image).To(Equal("docker:///myrepo/mydb@sha256:abc123def456"))
				Expect(chosenContainer.Spec.Sidecars[0].ImageArtifact).To(Equal(""))
			})
		})

		Context("when a sidecar uses image_artifact that does not exist", func() {
			BeforeEach(func() {
				taskPlan.Sidecars = []atc.SidecarSource{
					{Config: &atc.SidecarConfig{
						Name:          "postgres",
						ImageArtifact: "nonexistent-image",
						Ports:         []atc.SidecarPort{{ContainerPort: 5432}},
					}},
				}
			})

			It("returns an error", func() {
				Expect(stepErr).To(HaveOccurred())
				Expect(stepErr.Error()).To(ContainSubstring(`image_artifact "nonexistent-image" not found`))
			})
		})

		Context("when a sidecar uses image_artifact mixed with regular sidecars", func() {
			BeforeEach(func() {
				repo.RegisterImageRef("my-db-image", "docker:///myrepo/mydb@sha256:abc123def456")

				taskPlan.Sidecars = []atc.SidecarSource{
					{Config: &atc.SidecarConfig{
						Name:          "postgres",
						ImageArtifact: "my-db-image",
						Ports:         []atc.SidecarPort{{ContainerPort: 5432}},
					}},
					{Config: &atc.SidecarConfig{
						Name:  "redis",
						Image: "redis:7",
						Ports: []atc.SidecarPort{{ContainerPort: 6379}},
					}},
				}
			})

			It("resolves the artifact ref and keeps the regular sidecar unchanged", func() {
				Expect(stepErr).ToNot(HaveOccurred())
				Expect(chosenContainer.Spec.Sidecars).To(HaveLen(2))
				Expect(chosenContainer.Spec.Sidecars[0].Name).To(Equal("postgres"))
				Expect(chosenContainer.Spec.Sidecars[0].Image).To(Equal("docker:///myrepo/mydb@sha256:abc123def456"))
				Expect(chosenContainer.Spec.Sidecars[1].Name).To(Equal("redis"))
				Expect(chosenContainer.Spec.Sidecars[1].Image).To(Equal("redis:7"))
			})
		})

		Context("when sidecar images are resolved to pinned digests", func() {
			var (
				registry *imageresolvertesting.Registry
				digest   string
			)

			BeforeEach(func() {
				registry = newTaskStepRegistry()
				digest = pushTaskStepImage(registry, "redis", "7")
				registry.DrainRequests()
				taskStepOptions = []exec.TaskStepOption{
					exec.WithImageResolver(imageresolver.NewResolver(authn.DefaultKeychain)),
				}

				taskPlan.Sidecars = []atc.SidecarSource{
					{Config: &atc.SidecarConfig{
						Name:  "redis",
						Image: registry.Host() + "/redis:7",
						Ports: []atc.SidecarPort{{ContainerPort: 6379}},
					}},
				}
			})

			It("resolves bare image tags to pinned digests", func() {
				Expect(stepErr).ToNot(HaveOccurred())
				Expect(chosenContainer.Spec.Sidecars).To(HaveLen(1))
				Expect(chosenContainer.Spec.Sidecars[0].Image).To(Equal(registry.Host() + "/redis@" + digest))
			})
		})

		Context("when sidecar image is already digest-pinned", func() {
			var registry *imageresolvertesting.Registry

			BeforeEach(func() {
				registry = newTaskStepRegistry()
				registry.DrainRequests()
				taskStepOptions = []exec.TaskStepOption{
					exec.WithImageResolver(imageresolver.NewResolver(authn.DefaultKeychain)),
				}

				taskPlan.Sidecars = []atc.SidecarSource{
					{Config: &atc.SidecarConfig{
						Name:  "redis",
						Image: registry.Host() + "/redis@sha256:abc123",
						Ports: []atc.SidecarPort{{ContainerPort: 6379}},
					}},
				}
			})

			It("skips resolution for already-pinned digests", func() {
				Expect(stepErr).ToNot(HaveOccurred())
				Expect(chosenContainer.Spec.Sidecars).To(HaveLen(1))
				Expect(chosenContainer.Spec.Sidecars[0].Image).To(Equal(registry.Host() + "/redis@sha256:abc123"))
				Expect(taskStepHeadRequests(registry)).To(BeEmpty())
			})
		})

		Context("when sidecar image resolution fails", func() {
			var registry *imageresolvertesting.Registry

			BeforeEach(func() {
				registry = newTaskStepRegistry()
				registry.DrainRequests()
				taskStepOptions = []exec.TaskStepOption{
					exec.WithImageResolver(imageresolver.NewResolver(authn.DefaultKeychain)),
				}

				taskPlan.Sidecars = []atc.SidecarSource{
					{Config: &atc.SidecarConfig{
						Name:  "redis",
						Image: registry.Host() + "/redis:7",
						Ports: []atc.SidecarPort{{ContainerPort: 6379}},
					}},
				}
			})

			It("falls through to the original tag-based image (best-effort)", func() {
				Expect(stepErr).ToNot(HaveOccurred())
				Expect(chosenContainer.Spec.Sidecars).To(HaveLen(1))
				Expect(chosenContainer.Spec.Sidecars[0].Image).To(Equal(registry.Host() + "/redis:7"))
				Expect(taskStepHeadRequests(registry)).To(ContainElement(imageresolvertesting.Request{
					Method: http.MethodHead,
					Path:   "/v2/redis/manifests/7",
				}))
			})
		})

		Context("when sidecar image has a docker:/// prefix", func() {
			var (
				registry *imageresolvertesting.Registry
				digest   string
			)

			BeforeEach(func() {
				registry = newTaskStepRegistry()
				digest = pushTaskStepImage(registry, "redis", "7")
				registry.DrainRequests()
				taskStepOptions = []exec.TaskStepOption{
					exec.WithImageResolver(imageresolver.NewResolver(authn.DefaultKeychain)),
				}

				taskPlan.Sidecars = []atc.SidecarSource{
					{Config: &atc.SidecarConfig{
						Name:  "redis",
						Image: "docker:///" + registry.Host() + "/redis:7",
						Ports: []atc.SidecarPort{{ContainerPort: 6379}},
					}},
				}
			})

			It("strips the prefix before resolving the digest", func() {
				Expect(stepErr).ToNot(HaveOccurred())
				Expect(taskStepHeadRequests(registry)).To(ContainElement(imageresolvertesting.Request{
					Method: http.MethodHead,
					Path:   "/v2/redis/manifests/7",
				}))
			})

			It("sets the resolved digest on the sidecar", func() {
				Expect(chosenContainer.Spec.Sidecars).To(HaveLen(1))
				Expect(chosenContainer.Spec.Sidecars[0].Image).To(Equal(registry.Host() + "/redis@" + digest))
			})
		})

		Context("when sidecar image has a docker:// prefix (double-slash)", func() {
			var registry *imageresolvertesting.Registry

			BeforeEach(func() {
				registry = newTaskStepRegistry()
				pushTaskStepImage(registry, "mydb", "v3")
				registry.DrainRequests()
				taskStepOptions = []exec.TaskStepOption{
					exec.WithImageResolver(imageresolver.NewResolver(authn.DefaultKeychain)),
				}

				taskPlan.Sidecars = []atc.SidecarSource{
					{Config: &atc.SidecarConfig{
						Name:  "mydb",
						Image: "docker://" + registry.Host() + "/mydb:v3",
						Ports: []atc.SidecarPort{{ContainerPort: 5432}},
					}},
				}
			})

			It("strips the prefix and resolves correctly", func() {
				Expect(stepErr).ToNot(HaveOccurred())
				Expect(taskStepHeadRequests(registry)).To(ContainElement(imageresolvertesting.Request{
					Method: http.MethodHead,
					Path:   "/v2/mydb/manifests/v3",
				}))
			})
		})

		Context("when sidecar image has a raw:/// prefix", func() {
			var registry *imageresolvertesting.Registry

			BeforeEach(func() {
				registry = newTaskStepRegistry()
				pushTaskStepImage(registry, "nginx", "alpine")
				registry.DrainRequests()
				taskStepOptions = []exec.TaskStepOption{
					exec.WithImageResolver(imageresolver.NewResolver(authn.DefaultKeychain)),
				}

				taskPlan.Sidecars = []atc.SidecarSource{
					{Config: &atc.SidecarConfig{
						Name:  "nginx",
						Image: "raw:///" + registry.Host() + "/nginx:alpine",
						Ports: []atc.SidecarPort{{ContainerPort: 80}},
					}},
				}
			})

			It("strips the prefix and resolves correctly", func() {
				Expect(stepErr).ToNot(HaveOccurred())
				Expect(taskStepHeadRequests(registry)).To(ContainElement(imageresolvertesting.Request{
					Method: http.MethodHead,
					Path:   "/v2/nginx/manifests/alpine",
				}))
			})
		})

		Context("when sidecar image has a docker:/// prefix and is already digest-pinned", func() {
			var registry *imageresolvertesting.Registry

			BeforeEach(func() {
				registry = newTaskStepRegistry()
				registry.DrainRequests()
				taskStepOptions = []exec.TaskStepOption{
					exec.WithImageResolver(imageresolver.NewResolver(authn.DefaultKeychain)),
				}

				taskPlan.Sidecars = []atc.SidecarSource{
					{Config: &atc.SidecarConfig{
						Name:  "redis",
						Image: "docker:///" + registry.Host() + "/redis@sha256:alreadypinned",
						Ports: []atc.SidecarPort{{ContainerPort: 6379}},
					}},
				}
			})

			It("skips resolution for already-pinned digests", func() {
				Expect(stepErr).ToNot(HaveOccurred())
				Expect(chosenContainer.Spec.Sidecars).To(HaveLen(1))
				Expect(chosenContainer.Spec.Sidecars[0].Image).To(Equal("docker:///" + registry.Host() + "/redis@sha256:alreadypinned"))
				Expect(taskStepHeadRequests(registry)).To(BeEmpty())
			})
		})

		Context("when a sidecar file references an unknown source", func() {
			BeforeEach(func() {
				taskPlan.Sidecars = []atc.SidecarSource{{File: "nonexistent/sidecars/db.yml"}}
			})

			It("returns an error", func() {
				Expect(stepErr).To(HaveOccurred())
				Expect(stepErr.Error()).To(ContainSubstring("unknown artifact source"))
			})
		})

		Context("when a sidecar file path is malformed", func() {
			BeforeEach(func() {
				taskPlan.Sidecars = []atc.SidecarSource{{File: "no-slash"}}
			})

			It("returns an error", func() {
				Expect(stepErr).To(HaveOccurred())
				Expect(stepErr.Error()).To(ContainSubstring("must be in the format SOURCE/FILE"))
			})
		})
	})
})
