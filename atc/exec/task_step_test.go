package exec_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing/fstest"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/exec/build"
	"github.com/concourse/concourse/atc/exec/execfakes"
	"github.com/concourse/concourse/atc/imageresolver"
	"github.com/concourse/concourse/atc/imageresolver/imageresolvertesting"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/runtime/runtimetest"
	"github.com/concourse/concourse/tracing"
	"github.com/concourse/concourse/vars"
	"github.com/onsi/gomega/gbytes"
	"go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("TaskStep", func() {
	var (
		ctx    context.Context
		cancel func()

		stdoutBuf *gbytes.Buffer
		stderrBuf *gbytes.Buffer

		fakePool     *execfakes.FakePool
		fakeStreamer *execfakes.FakeStreamer

		fakeDelegate *execfakes.FakeTaskDelegate

		fakeDelegateFactory *execfakes.FakeTaskDelegateFactory

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

		stepMetadata = exec.StepMetadata{
			TeamID:      123,
			BuildID:     1234,
			JobID:       12345,
			ExternalURL: "http://foo.bar",
		}

		planID = atc.PlanID("42")

		expectedOwner = db.NewBuildStepContainerOwner(stepMetadata.BuildID, planID, stepMetadata.TeamID)

		defaultTaskTimeout time.Duration = 0
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())

		stdoutBuf = gbytes.NewBuffer()
		stderrBuf = gbytes.NewBuffer()

		fakeStreamer = new(execfakes.FakeStreamer)

		fakeDelegate = new(execfakes.FakeTaskDelegate)
		fakeDelegate.StdoutReturns(stdoutBuf)
		fakeDelegate.StderrReturns(stderrBuf)

		fakeDelegate.StartSpanReturns(ctx, tracing.NoopSpan)

		fakeDelegateFactory = new(execfakes.FakeTaskDelegateFactory)
		fakeDelegateFactory.TaskDelegateReturns(fakeDelegate)

		taskStepOptions = nil

		state = exec.NewRunState(noopStepper, vars.StaticVariables{"source-param": "super-secret-source"})
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
			fakePool,
			fakeStreamer,
			fakeDelegateFactory,
			defaultTaskTimeout,
			taskStepOptions...,
		)

		stepOk, stepErr = taskStep.Run(ctx, state)
	})

	expectWorkerSpecTeamIDSet := func() {
		Expect(fakePool.FindOrSelectWorkerCallCount()).To(Equal(1))
		_, _, _, workerSpec := fakePool.FindOrSelectWorkerArgsForCall(0)
		Expect(workerSpec.TeamID).To(Equal(stepMetadata.TeamID))
	}

	Context("when the plan has a config", func() {
		var chosenWorker *runtimetest.Worker
		var chosenContainer *runtimetest.WorkerContainer

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
			fakePool = new(execfakes.FakePool)
			fakePool.FindOrSelectWorkerReturns(chosenWorker, nil)
		})

		It("Task env includes atc external url and build identity", func() {
			Expect(chosenContainer.Spec.Env).To(ConsistOf("ATC_EXTERNAL_URL=http://foo.bar", "BUILD_ID=1234", "SECURE=secret-task-param"))
		})

		Context("before running the task", func() {
			BeforeEach(func() {
				chosenContainer.ProcessDefs[0].Stub.Do = func(_ context.Context, _ *runtimetest.Process) error {
					defer GinkgoRecover()
					Expect(fakeDelegate.InitializingCallCount()).To(Equal(1))

					return nil
				}
			})

			It("invokes the delegate's Initializing callback", func() {
				// validate the process actually ran
				Expect(chosenContainer.RunningProcesses()).To(HaveLen(1))
			})
		})

		Describe("worker selection", func() {
			var ctx context.Context

			JustBeforeEach(func() {
				Expect(fakePool.FindOrSelectWorkerCallCount()).To(Equal(1))
				ctx, _, _, _ = fakePool.FindOrSelectWorkerArgsForCall(0)
			})

			It("doesn't enforce a timeout", func() {
				_, ok := ctx.Deadline()
				Expect(ok).To(BeFalse())
			})

			It("emits a BeforeSelectWorker event", func() {
				Expect(fakeDelegate.BeforeSelectWorkerCallCount()).To(Equal(1))
			})

			It("emits a SelectedWorker event", func() {
				Expect(fakeDelegate.SelectedWorkerCallCount()).To(Equal(1))
				_, workerName := fakeDelegate.SelectedWorkerArgsForCall(0)
				Expect(workerName).To(Equal("worker"))
			})

			Context("when selecting a worker fails", func() {
				BeforeEach(func() {
					fakePool.FindOrSelectWorkerReturns(nil, errors.New("nope"))
				})

				It("returns an err", func() {
					Expect(stepErr).To(MatchError(ContainSubstring("nope")))
				})
			})
		})

		It("sets the config on the TaskDelegate", func() {
			Expect(fakeDelegate.SetTaskConfigCallCount()).To(Equal(1))
			actualTaskConfig := fakeDelegate.SetTaskConfigArgsForCall(0)
			Expect(actualTaskConfig).To(Equal(*taskPlan.Config))
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
				Expect(fakeDelegate.ErroredCallCount()).To(Equal(1))
				_, status := fakeDelegate.ErroredArgsForCall(0)
				Expect(status).To(Equal(exec.TimeoutLogMessage))
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

			BeforeEach(func() {
				defaultTaskTimeout = 0
				spanRecorder = new(tracetest.SpanRecorder)
				tp := trace.NewTracerProvider(trace.WithSpanProcessor(spanRecorder), trace.WithSyncer(tracetest.NewInMemoryExporter()))
				tracing.ConfigureTraceProvider(tp)

				spanCtx, buildSpan := tracing.StartSpan(ctx, "build", nil)
				fakeDelegate.StartSpanReturns(spanCtx, buildSpan)

				chosenContainer.ProcessDefs[0].Stub.Do = func(ctx context.Context, _ *runtimetest.Process) error {
					defer GinkgoRecover()
					// Properly propagates span context
					Expect(tracing.FromContext(ctx)).To(Equal(buildSpan))
					return nil
				}
			})

			AfterEach(func() {
				tracing.Configured = false
			})

			It("populates the TRACEPARENT env var", func() {
				Expect(chosenContainer.Spec.Env).To(ContainElement(MatchRegexp(`TRACEPARENT=.+`)))
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

		Context("with typed snapshot declarations", func() {
			var (
				outputSealer *recordingOutputSealer
				inputVolume  *runtimetest.Volume
				inputRef     snapshot.SnapshotRef
				outputVolume *runtimetest.Volume
				outputDigest snapshot.Digest
			)

			BeforeEach(func() {
				var err error
				outputDigest, err = snapshot.ParseDigest("sha256:" + strings.Repeat("a", 64))
				Expect(err).NotTo(HaveOccurred())
				stepMetadata.TeamName = "main"
				stepMetadata.SnapshotCreatedBy = "concourse"
				workflowDefinitionID := 77
				workflowRunID := snapshot.WorkflowRunID(9007199254740993)
				stepMetadata.WorkflowDefinitionID = &workflowDefinitionID
				stepMetadata.WorkflowRunID = &workflowRunID
				containerMetadata.Attempt = "2"
				inputDigest, err := snapshot.ParseDigest("sha256:" + strings.Repeat("b", 64))
				Expect(err).NotTo(HaveOccurred())
				inputRef = snapshot.SnapshotRef{ID: 81, Type: snapshot.TypeRef("repository/v1"), Digest: inputDigest}
				inputVolume = runtimetest.NewVolume("typed-input")
				Expect(repo.RegisterArtifacts(map[build.ArtifactName]build.ArtifactEntry{
					"repository": {Artifact: inputVolume, FromCache: true, Snapshot: &inputRef},
				})).To(Succeed())
				taskPlan.Config.Inputs = []atc.TaskInputConfig{{Name: "source", Path: "checkout"}}
				taskPlan.InputMapping = map[string]string{"source": "repository"}
				taskPlan.SnapshotInputs = map[string]atc.SnapshotInputConfig{
					"repository": {Type: snapshot.TypeRef("repository/v1")},
				}
				taskPlan.Config.Outputs = []atc.TaskOutputConfig{{Name: "change", Path: "result"}}
				taskPlan.OutputMapping = map[string]string{"change": "mapped-change"}
				taskPlan.SnapshotOutputs = map[string]atc.SnapshotOutputConfig{
					"mapped-change": {
						Type: snapshot.TypeRef("repository-change/v1"), Retention: snapshot.RetentionClassWorkflow,
						WorkflowPort: "result", WorkflowDefinitionID: 77, WorkflowRunID: "9007199254740993",
						SourceMetadata: json.RawMessage(`{"adapter":"resource-version","operation_key":"capture-key"}`),
					},
				}
				outputVolume = runtimetest.NewVolume("typed-output")
				chosenContainer.Mounts = []runtime.VolumeMount{{
					Volume: outputVolume, MountPath: "some-artifact-root/result/",
				}}
				outputSealer = &recordingOutputSealer{result: map[string]snapshot.SealedOutput{
					"mapped-change": {
						Port:     snapshot.Port{Name: "mapped-change", Type: snapshot.TypeRef("repository-change/v1")},
						Snapshot: snapshot.SnapshotRef{ID: 91, Type: snapshot.TypeRef("repository-change/v1"), Digest: outputDigest},
					},
				}}
				snapshotMetadata, snapshotContent := snapshotStoresForSealedOutputs(outputSealer.result)
				taskStepOptions = []exec.TaskStepOption{
					exec.WithTaskOutputSealer(outputSealer),
					exec.WithTaskSnapshotStores(snapshotMetadata, snapshotContent),
				}
			})

			It("seals the complete typed batch and atomically publishes worker artifacts with refs", func() {
				Expect(stepErr).NotTo(HaveOccurred())
				Expect(stepOk).To(BeTrue())
				Expect(outputSealer.calls).To(HaveLen(1))
				request := outputSealer.calls[0]
				Expect(request.BuildID).To(Equal(1234))
				Expect(request.TeamID).To(Equal(123))
				Expect(request.TeamName).To(Equal("main"))
				Expect(request.CreatedBy).To(Equal("concourse"))
				Expect(request.PlanID).To(Equal("42"))
				Expect(request.Attempt).To(Equal("2"))
				Expect(request.StepKind).To(Equal("task"))
				Expect(request.StepName).To(Equal("some-task"))
				Expect(request.InputOrder).To(Equal([]string{"repository"}))
				Expect(request.Inputs).To(Equal(map[string]snapshot.SnapshotRef{"repository": inputRef}))
				Expect(request.OutputDeclarations).To(Equal([]snapshot.Port{{
					Name: "mapped-change", Type: snapshot.TypeRef("repository-change/v1"),
				}}))
				Expect(request.Outputs).To(HaveLen(1))
				Expect(request.Outputs[0].ClientKey).To(Equal("mapped-change"))
				Expect(request.Outputs[0].Port).To(Equal(request.OutputDeclarations[0]))
				Expect(request.Outputs[0].Retention).To(Equal(snapshot.RetentionClassWorkflow))
				Expect(request.Outputs[0].WorkflowPort).To(Equal("result"))
				Expect(request.Outputs[0].SourceMetadata).To(MatchJSON(`{"adapter":"resource-version","operation_key":"capture-key"}`))
				Expect(*request.WorkflowDefinitionID).To(Equal(77))
				Expect(request.WorkflowRunID.String()).To(Equal("9007199254740993"))
				Expect(chosenContainer.Spec.Inputs).To(ConsistOf(runtime.Input{
					Artifact: inputVolume, DestinationPath: "some-artifact-root/checkout", FromCache: true,
				}))
				Expect(chosenContainer.Spec.Env).To(ContainElements(
					"AGENT_INPUT_REPOSITORY_SNAPSHOT_TYPE=repository/v1",
					"AGENT_INPUT_REPOSITORY_SNAPSHOT_DIGEST="+inputRef.Digest.String(),
					"AGENT_OUTPUT_CHANGE_RECORD_TYPE=repository-change/v1",
					HavePrefix("AGENT_OUTPUT_CHANGE_RECORD_SCHEMA=sha256:"),
				))

				entry, found := repo.ArtifactEntryFor("mapped-change")
				Expect(found).To(BeTrue())
				Expect(entry.Artifact).To(BeAssignableToTypeOf(&runtime.SnapshotArtifact{}))
				Expect(entry.Artifact).NotTo(Equal(outputVolume))
				Expect(entry.Snapshot).NotTo(BeNil())
				Expect(*entry.Snapshot).To(Equal(outputSealer.result["mapped-change"].Snapshot))
			})

			Context("when the authenticated workflow producer has only an internal output", func() {
				BeforeEach(func() {
					declaration := taskPlan.SnapshotOutputs["mapped-change"]
					declaration.Retention = ""
					declaration.WorkflowPort = ""
					declaration.WorkflowDefinitionID = 0
					declaration.WorkflowRunID = ""
					taskPlan.SnapshotOutputs["mapped-change"] = declaration
				})

				It("passes the exact authenticated workflow association to the seal request", func() {
					Expect(stepErr).NotTo(HaveOccurred())
					Expect(stepOk).To(BeTrue())
					Expect(outputSealer.calls).To(HaveLen(1))
					request := outputSealer.calls[0]
					Expect(request.WorkflowDefinitionID).NotTo(BeNil())
					Expect(*request.WorkflowDefinitionID).To(Equal(77))
					Expect(request.WorkflowRunID).NotTo(BeNil())
					Expect(*request.WorkflowRunID).To(Equal(snapshot.WorkflowRunID(9007199254740993)))
					Expect(request.Outputs).To(HaveLen(1))
					Expect(request.Outputs[0].Retention).NotTo(Equal(snapshot.RetentionClassWorkflow))
					Expect(request.Outputs[0].WorkflowPort).To(BeEmpty())
				})
			})

			Context("when an ordinary build claims a workflow-retained output", func() {
				BeforeEach(func() {
					stepMetadata.WorkflowDefinitionID = nil
					stepMetadata.WorkflowRunID = nil
				})

				It("rejects the unauthenticated association before worker selection", func() {
					Expect(stepErr).To(MatchError(ContainSubstring("not associated with a workflow run")))
					Expect(fakePool.FindOrSelectWorkerCallCount()).To(Equal(0))
					Expect(outputSealer.calls).To(BeEmpty())
				})
			})

			Context("when the process exits nonzero", func() {
				BeforeEach(func() { chosenContainer.ProcessDefs[0].Stub.ExitStatus = 1 })

				It("does not seal or publish typed outputs", func() {
					Expect(stepErr).NotTo(HaveOccurred())
					Expect(stepOk).To(BeFalse())
					Expect(outputSealer.calls).To(BeEmpty())
					_, found := repo.ArtifactEntryFor("mapped-change")
					Expect(found).To(BeFalse())
				})
			})

			Context("when sealing fails", func() {
				BeforeEach(func() { outputSealer.err = errors.New("seal failed") })

				It("propagates the error without publishing the typed artifact", func() {
					Expect(stepErr).To(MatchError(ContainSubstring("seal failed")))
					_, found := repo.ArtifactEntryFor("mapped-change")
					Expect(found).To(BeFalse())
				})
			})

			Context("when the process wait fails", func() {
				BeforeEach(func() { chosenContainer.ProcessDefs[0].Stub.Err = "wait failed" })

				It("does not seal or publish typed outputs", func() {
					Expect(stepErr).To(MatchError("wait failed"))
					Expect(outputSealer.calls).To(BeEmpty())
					_, found := repo.ArtifactEntryFor("mapped-change")
					Expect(found).To(BeFalse())
				})
			})

			Context("when the required typed output mount is missing", func() {
				BeforeEach(func() { chosenContainer.Mounts = nil })

				It("rejects the entire batch before sealing", func() {
					Expect(stepErr).To(MatchError(ContainSubstring("required typed output")))
					Expect(outputSealer.calls).To(BeEmpty())
					_, found := repo.ArtifactEntryFor("mapped-change")
					Expect(found).To(BeFalse())
				})
			})

			Context("when the typed output has duplicate mounts", func() {
				BeforeEach(func() {
					chosenContainer.Mounts = append(chosenContainer.Mounts, runtime.VolumeMount{
						Volume: runtimetest.NewVolume("duplicate"), MountPath: "some-artifact-root/result",
					})
				})

				It("rejects the entire batch before sealing", func() {
					Expect(stepErr).To(MatchError(ContainSubstring("duplicate mounts")))
					Expect(outputSealer.calls).To(BeEmpty())
					_, found := repo.ArtifactEntryFor("mapped-change")
					Expect(found).To(BeFalse())
				})
			})

			Context("when one of two required typed output mounts is missing", func() {
				BeforeEach(func() {
					taskPlan.Config.Outputs = append(taskPlan.Config.Outputs, atc.TaskOutputConfig{Name: "review"})
					taskPlan.SnapshotOutputs["review"] = atc.SnapshotOutputConfig{Type: snapshot.TypeRef("review/v1")}
				})

				It("does not seal or publish the valid first output", func() {
					Expect(stepErr).To(MatchError(ContainSubstring("required typed output \"review\"")))
					Expect(outputSealer.calls).To(BeEmpty())
					_, firstFound := repo.ArtifactEntryFor("mapped-change")
					_, secondFound := repo.ArtifactEntryFor("review")
					Expect(firstFound).To(BeFalse())
					Expect(secondFound).To(BeFalse())
				})
			})

			Context("when an optional typed output mount exists but has no produced marker", func() {
				BeforeEach(func() {
					declaration := taskPlan.SnapshotOutputs["mapped-change"]
					declaration.Optional = true
					taskPlan.SnapshotOutputs["mapped-change"] = declaration
					chosenContainer.Mounts = append(chosenContainer.Mounts, runtime.VolumeMount{
						Volume:    runtimetest.NewVolume("typed-output-markers"),
						MountPath: "/tmp/.jetbridge/typed-output-markers/v1",
					})
					outputSealer.result = map[string]snapshot.SealedOutput{}
				})

				It("treats the always-mounted empty output as absent", func() {
					Expect(stepErr).NotTo(HaveOccurred())
					Expect(stepOk).To(BeTrue())
					Expect(chosenContainer.Spec.Outputs).To(HaveKeyWithValue(
						"__jetbridge_typed_output_markers_v1",
						"/tmp/.jetbridge/typed-output-markers/v1/",
					))
					Expect(chosenContainer.Spec.Env).To(ContainElement(
						`JETBRIDGE_OPTIONAL_OUTPUT_MARKERS={"change":"/tmp/.jetbridge/typed-output-markers/v1/Y2hhbmdl"}`,
					))
					Expect(outputSealer.calls).To(HaveLen(1))
					Expect(outputSealer.calls[0].OutputDeclarations).To(Equal([]snapshot.Port{{
						Name: "mapped-change", Type: snapshot.TypeRef("repository-change/v1"), Optional: true,
					}}))
					Expect(outputSealer.calls[0].Outputs).To(BeEmpty())
					_, found := repo.ArtifactEntryFor("mapped-change")
					Expect(found).To(BeFalse())
				})
			})

			Context("when an optional typed output has its explicit produced marker", func() {
				BeforeEach(func() {
					declaration := taskPlan.SnapshotOutputs["mapped-change"]
					declaration.Optional = true
					taskPlan.SnapshotOutputs["mapped-change"] = declaration

					sealed := outputSealer.result["mapped-change"]
					sealed.Port.Optional = true
					outputSealer.result["mapped-change"] = sealed

					markers := runtimetest.NewVolume("typed-output-markers").WithContent(
						runtimetest.VolumeContent{
							"Y2hhbmdl": &fstest.MapFile{},
						},
					)
					chosenContainer.Mounts = append(chosenContainer.Mounts, runtime.VolumeMount{
						Volume: markers, MountPath: "/tmp/.jetbridge/typed-output-markers/v1",
					})
				})

				It("seals and publishes the marked output", func() {
					Expect(stepErr).NotTo(HaveOccurred())
					Expect(stepOk).To(BeTrue())
					Expect(outputSealer.calls).To(HaveLen(1))
					Expect(outputSealer.calls[0].Outputs).To(HaveLen(1))
					entry, found := repo.ArtifactEntryFor("mapped-change")
					Expect(found).To(BeTrue())
					Expect(entry.Artifact).To(BeAssignableToTypeOf(&runtime.SnapshotArtifact{}))
				})
			})

			Context("when atomic repository publication rejects the batch", func() {
				var existing *runtimetest.Volume

				BeforeEach(func() {
					state = state.NewArtifactScope()
					repo = state.ArtifactRepository()
					existing = runtimetest.NewVolume("existing")
					repo.RegisterArtifact("mapped-change", existing, false)
				})

				It("propagates the error without replacing the existing generation", func() {
					Expect(stepErr).To(MatchError(ContainSubstring(build.ErrArtifactAlreadyRegistered.Error())))
					Expect(outputSealer.calls).To(HaveLen(1))
					entry, found := repo.ArtifactEntryFor("mapped-change")
					Expect(found).To(BeTrue())
					Expect(entry.Artifact).To(Equal(existing))
					Expect(entry.Snapshot).To(BeNil())
				})
			})
		})

		Context("with typed snapshot inputs", func() {
			var inputDigest snapshot.Digest

			BeforeEach(func() {
				var err error
				inputDigest, err = snapshot.ParseDigest("sha256:" + strings.Repeat("c", 64))
				Expect(err).NotTo(HaveOccurred())
				taskPlan.Config.Inputs = []atc.TaskInputConfig{{Name: "source", Path: "checkout"}}
				taskPlan.InputMapping = map[string]string{"source": "repository"}
				taskPlan.SnapshotInputs = map[string]atc.SnapshotInputConfig{
					"repository": {Type: snapshot.TypeRef("repository/v1")},
				}
				taskStepOptions = []exec.TaskStepOption{exec.WithTaskOutputSealer(&recordingOutputSealer{})}
			})

			Context("when an optional input is absent", func() {
				BeforeEach(func() {
					declaration := taskPlan.SnapshotInputs["repository"]
					declaration.Optional = true
					taskPlan.SnapshotInputs["repository"] = declaration
				})

				It("runs without mounting or reporting it missing", func() {
					Expect(stepErr).NotTo(HaveOccurred())
					Expect(stepOk).To(BeTrue())
					Expect(fakePool.FindOrSelectWorkerCallCount()).To(Equal(1))
					Expect(chosenContainer.Spec.Inputs).To(BeEmpty())
				})
			})

			Context("when a required input is absent", func() {
				It("returns MissingInputsError before worker selection", func() {
					Expect(stepErr).To(BeAssignableToTypeOf(exec.MissingInputsError{}))
					Expect(stepErr.(exec.MissingInputsError).Inputs).To(Equal([]string{"repository"}))
					Expect(fakePool.FindOrSelectWorkerCallCount()).To(Equal(0))
				})
			})

			Context("when the input snapshot type differs", func() {
				BeforeEach(func() {
					ref := snapshot.SnapshotRef{ID: 41, Type: snapshot.TypeRef("review/v1"), Digest: inputDigest}
					Expect(repo.RegisterArtifacts(map[build.ArtifactName]build.ArtifactEntry{
						"repository": {Artifact: runtimetest.NewVolume("input"), Snapshot: &ref},
					})).To(Succeed())
				})

				It("fails before worker selection", func() {
					Expect(stepErr).To(MatchError(ContainSubstring("does not match declared type")))
					Expect(fakePool.FindOrSelectWorkerCallCount()).To(Equal(0))
				})
			})
		})

		Context("when a file-fetched task config omits a typed declaration", func() {
			BeforeEach(func() {
				taskPlan.Config = nil
				taskPlan.ConfigPath = "config/task.yml"
				taskPlan.SnapshotOutputs = map[string]atc.SnapshotOutputConfig{
					"declared": {Type: snapshot.TypeRef("opaque/v1")},
				}
				repo.RegisterArtifact("config", runtimetest.NewVolume("config"), false)
				fakeStreamer.StreamFileReturns(io.NopCloser(strings.NewReader(`
platform: linux
run:
  path: "true"
outputs:
- name: actual
`)), nil)
				taskStepOptions = []exec.TaskStepOption{exec.WithTaskOutputSealer(&recordingOutputSealer{})}
			})

			It("fails membership validation before worker selection", func() {
				Expect(stepErr).To(MatchError(ContainSubstring("absent from the fetched task config")))
				Expect(fakePool.FindOrSelectWorkerCallCount()).To(Equal(0))
			})
		})

		Context("when a file-fetched typed task declares an extra untyped artifact", func() {
			BeforeEach(func() {
				taskPlan.Config = nil
				taskPlan.ConfigPath = "config/task.yml"
				taskPlan.SnapshotOutputs = map[string]atc.SnapshotOutputConfig{
					"review": {Type: snapshot.TypeRef("review/v1")},
				}
				repo.RegisterArtifact("config", runtimetest.NewVolume("config"), false)
				fakeStreamer.StreamFileReturns(io.NopCloser(strings.NewReader(`
platform: linux
run:
  path: "true"
outputs:
- name: review
- name: hidden
`)), nil)
				taskStepOptions = []exec.TaskStepOption{exec.WithTaskOutputSealer(&recordingOutputSealer{})}
			})

			It("fails exact output coverage before worker selection", func() {
				Expect(stepErr).To(MatchError(ContainSubstring("every declared task output must be typed")))
				Expect(stepErr).To(MatchError(ContainSubstring("hidden")))
				Expect(fakePool.FindOrSelectWorkerCallCount()).To(Equal(0))
			})
		})

		Context("when a file-fetched typed task consumes an extra untyped artifact", func() {
			BeforeEach(func() {
				taskPlan.Config = nil
				taskPlan.ConfigPath = "config/task.yml"
				taskPlan.SnapshotInputs = map[string]atc.SnapshotInputConfig{
					"repository": {Type: snapshot.TypeRef("repository/v1")},
				}
				repo.RegisterArtifact("config", runtimetest.NewVolume("config"), false)
				fakeStreamer.StreamFileReturns(io.NopCloser(strings.NewReader(`
platform: linux
run:
  path: "true"
inputs:
- name: repository
- name: hidden
`)), nil)
				taskStepOptions = []exec.TaskStepOption{exec.WithTaskOutputSealer(&recordingOutputSealer{})}
			})

			It("fails exact input coverage before worker selection", func() {
				Expect(stepErr).To(MatchError(ContainSubstring("every declared task input must be typed")))
				Expect(stepErr).To(MatchError(ContainSubstring("hidden")))
				Expect(fakePool.FindOrSelectWorkerCallCount()).To(Equal(0))
			})
		})

		Context("when an ordinary task input resolves to a typed snapshot", func() {
			BeforeEach(func() {
				digest, err := snapshot.ParseDigest("sha256:" + strings.Repeat("d", 64))
				Expect(err).NotTo(HaveOccurred())
				ref := snapshot.SnapshotRef{ID: 42, Type: snapshot.TypeRef("repository/v1"), Digest: digest}
				taskPlan.Config.Inputs = []atc.TaskInputConfig{{Name: "repository"}}
				Expect(repo.RegisterArtifacts(map[build.ArtifactName]build.ArtifactEntry{
					"repository": {Artifact: runtimetest.NewVolume("repository"), Snapshot: &ref},
				})).To(Succeed())
			})

			It("fails closed before worker selection", func() {
				Expect(stepErr).To(MatchError(ContainSubstring(`task input "repository" is a typed snapshot but has no input_types declaration`)))
				Expect(fakePool.FindOrSelectWorkerCallCount()).To(Equal(0))
			})
		})

		Context("when the task image source resolves to a typed snapshot", func() {
			BeforeEach(func() {
				digest, err := snapshot.ParseDigest("sha256:" + strings.Repeat("e", 64))
				Expect(err).NotTo(HaveOccurred())
				ref := snapshot.SnapshotRef{ID: 43, Type: snapshot.TypeRef("repository/v1"), Digest: digest}
				taskPlan.ImageArtifactName = "image"
				Expect(repo.RegisterArtifacts(map[build.ArtifactName]build.ArtifactEntry{
					"image": {Artifact: runtimetest.NewVolume("image"), Snapshot: &ref},
				})).To(Succeed())
			})

			It("fails closed before worker selection", func() {
				Expect(stepErr).To(MatchError(ContainSubstring(`task image source "image" is a typed snapshot`)))
				Expect(fakePool.FindOrSelectWorkerCallCount()).To(Equal(0))
			})
		})

		Context("when the task config source resolves to a typed snapshot", func() {
			BeforeEach(func() {
				digest, err := snapshot.ParseDigest("sha256:" + strings.Repeat("f", 64))
				Expect(err).NotTo(HaveOccurred())
				ref := snapshot.SnapshotRef{ID: 44, Type: snapshot.TypeRef("repository/v1"), Digest: digest}
				taskPlan.Config = nil
				taskPlan.ConfigPath = "config/task.yml"
				Expect(repo.RegisterArtifacts(map[build.ArtifactName]build.ArtifactEntry{
					"config": {Artifact: runtimetest.NewVolume("config"), Snapshot: &ref},
				})).To(Succeed())
			})

			It("fails closed before reading the config", func() {
				Expect(stepErr).To(MatchError(ContainSubstring(`task config source "config" is a typed snapshot`)))
				Expect(fakeStreamer.StreamFileCallCount()).To(Equal(0))
				Expect(fakePool.FindOrSelectWorkerCallCount()).To(Equal(0))
			})
		})

		Context("with a typed input carrying no snapshot ref", func() {
			BeforeEach(func() {
				stepMetadata.TeamName = "main"
				stepMetadata.SnapshotCreatedBy = "concourse"
				containerMetadata.Attempt = "1"
				taskPlan.Config.Inputs = []atc.TaskInputConfig{{Name: "repository"}}
				taskPlan.SnapshotInputs = map[string]atc.SnapshotInputConfig{
					"repository": {Type: snapshot.TypeRef("repository/v1")},
				}
				inputVolume := runtimetest.NewVolume("repository")
				repo.RegisterArtifact("repository", inputVolume, false)
				taskStepOptions = []exec.TaskStepOption{exec.WithTaskOutputSealer(&recordingOutputSealer{})}
			})

			It("fails before worker selection", func() {
				Expect(stepErr).To(MatchError(ContainSubstring("snapshot ref")))
				Expect(fakePool.FindOrSelectWorkerCallCount()).To(Equal(0))
			})
		})

		Context("with typed declarations but no sealer", func() {
			BeforeEach(func() {
				taskPlan.Config.Inputs = []atc.TaskInputConfig{{Name: "optional", Optional: true}}
				taskPlan.SnapshotInputs = map[string]atc.SnapshotInputConfig{
					"optional": {Type: snapshot.TypeRef("opaque/v1"), Optional: true},
				}
			})

			It("fails closed before worker selection", func() {
				Expect(stepErr).To(MatchError(ContainSubstring("output sealer")))
				Expect(fakePool.FindOrSelectWorkerCallCount()).To(Equal(0))
			})
		})

		Context("with typed workflow outputs claiming different runs", func() {
			BeforeEach(func() {
				taskPlan.Config.Outputs = []atc.TaskOutputConfig{{Name: "first"}, {Name: "second"}}
				taskPlan.SnapshotOutputs = map[string]atc.SnapshotOutputConfig{
					"first": {
						Type: snapshot.TypeRef("opaque/v1"), Retention: snapshot.RetentionClassWorkflow,
						WorkflowPort: "first", WorkflowDefinitionID: 7, WorkflowRunID: "8",
					},
					"second": {
						Type: snapshot.TypeRef("opaque/v1"), Retention: snapshot.RetentionClassWorkflow,
						WorkflowPort: "second", WorkflowDefinitionID: 7, WorkflowRunID: "9",
					},
				}
				taskStepOptions = []exec.TaskStepOption{exec.WithTaskOutputSealer(&recordingOutputSealer{})}
			})

			It("fails before worker selection", func() {
				Expect(stepErr).To(MatchError(ContainSubstring("multiple workflow definition/run associations")))
				Expect(fakePool.FindOrSelectWorkerCallCount()).To(Equal(0))
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
					fakeStreamer.StreamFileReturns(io.NopCloser(strings.NewReader("definitely not json")), nil)

					taskPlan.ImageArtifactName = "some-image-artifact"
					imageVolume := runtimetest.NewVolume("image-volume")
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
					fakeStreamer.StreamFileReturns(nil, runtime.ErrFileNotFound)

					taskPlan.ImageArtifactName = "some-image-artifact"
					imageVolume := runtimetest.NewVolume("image-volume")
					repo.RegisterArtifact("some-image-artifact", imageVolume, false)
				})

				It("returns the error", func() {
					Expect(stepErr).To(HaveOccurred())
					Expect(stepErr.Error()).To(ContainSubstring("file 'metadata.json' not found within artifact 'image-volume'"))
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
				fakeStreamer.StreamFileReturns(io.NopCloser(strings.NewReader(rootfsMetadata)), nil)

				taskPlan.ImageArtifactName = "some-image-artifact"
				imageVolume := runtimetest.NewVolume("image-volume")
				repo.RegisterArtifact("some-image-artifact", imageVolume, false)
			})

			It("uses the ENTRYPOINT/CMD and passes it back to the task delegate", func() {
				Expect(fakeDelegate.SetTaskConfigCallCount()).To(Equal(1))
				actualTaskConfig := fakeDelegate.SetTaskConfigArgsForCall(0)
				Expect(actualTaskConfig.Run.Path).To(Equal("/bin/sh"))
				Expect(actualTaskConfig.Run.Args).To(Equal([]string{"-c", "echo hello world"}))
			})

			Context("there are args in the task config", func() {
				BeforeEach(func() {
					taskPlan.Config.Run.Args = []string{"task", "args"}
				})

				It("they are appended to the ENTRYPOINT/CMD args", func() {
					Expect(fakeDelegate.SetTaskConfigCallCount()).To(Equal(1))
					actualTaskConfig := fakeDelegate.SetTaskConfigArgsForCall(0)
					Expect(actualTaskConfig.Run.Path).To(Equal("/bin/sh"))
					Expect(actualTaskConfig.Run.Args).To(Equal([]string{"-c", "echo hello world", "task", "args"}))
				})
			})

			Context("only ENTRYPOINT is specified", func() {
				BeforeEach(func() {
					taskPlan.Config.Run.Path = ""
					taskPlan.Config.Run.Args = []string{}
					rootfsMetadata := `{ "entrypoint": [ "some-program", "some-arg" ] }`
					fakeStreamer.StreamFileReturns(io.NopCloser(strings.NewReader(rootfsMetadata)), nil)
				})

				It("is parsed correctly", func() {
					Expect(fakeDelegate.SetTaskConfigCallCount()).To(Equal(1))
					actualTaskConfig := fakeDelegate.SetTaskConfigArgsForCall(0)
					Expect(actualTaskConfig.Run.Path).To(Equal("some-program"))
					Expect(actualTaskConfig.Run.Args).To(Equal([]string{"some-arg"}))
				})
			})

			Context("only CMD is specified", func() {
				BeforeEach(func() {
					taskPlan.Config.Run.Path = ""
					taskPlan.Config.Run.Args = []string{}
					rootfsMetadata := `{ "cmd": [ "cmd-program", "cmd-arg" ] }`
					fakeStreamer.StreamFileReturns(io.NopCloser(strings.NewReader(rootfsMetadata)), nil)
				})

				It("is parsed correctly", func() {
					Expect(fakeDelegate.SetTaskConfigCallCount()).To(Equal(1))
					actualTaskConfig := fakeDelegate.SetTaskConfigArgsForCall(0)
					Expect(actualTaskConfig.Run.Path).To(Equal("cmd-program"))
					Expect(actualTaskConfig.Run.Args).To(Equal([]string{"cmd-arg"}))
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

					expectWorkerSpecTeamIDSet()
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
								expectWorkerSpecTeamIDSet()
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
								expectWorkerSpecTeamIDSet()
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

			It("does not try to stream metadata.json from the artifact", func() {
				Expect(fakeStreamer.StreamFileCallCount()).To(Equal(0))
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
			var fetchedImageSpec runtime.ImageSpec

			BeforeEach(func() {
				taskPlan.Config.RootfsURI = "some-image"
				taskPlan.Config.ImageResource = &atc.ImageResource{
					Type:   "docker",
					Source: atc.Source{"some": "super-secret-source"},
					Params: atc.Params{"some": "params"},
				}
				taskPlan.Tags = atc.Tags{"some", "tags"}

				fetchedImageSpec = runtime.ImageSpec{
					ImageArtifact: runtimetest.NewVolume("some-volume"),
				}

				fakeDelegate.FetchImageReturns(fetchedImageSpec, nil)
			})

			It("succeeds", func() {
				Expect(stepErr).ToNot(HaveOccurred())
				Expect(stepOk).To(BeTrue())
			})

			It("fetches the image", func() {
				Expect(fakeDelegate.FetchImageCallCount()).To(Equal(1))
				_, imageResource, types, privileged, tags, skipInterval := fakeDelegate.FetchImageArgsForCall(0)
				Expect(imageResource).To(Equal(atc.ImageResource{
					Type:   "docker",
					Source: atc.Source{"some": "super-secret-source"},
					Params: atc.Params{"some": "params"},
				}))
				Expect(types).To(Equal(taskPlan.ResourceTypes))
				Expect(privileged).To(BeFalse())
				Expect(tags).To(Equal(atc.Tags{"some", "tags"}))
				Expect(skipInterval).To(Equal(false))
			})

			It("creates the specs with the fetched image", func() {
				Expect(chosenContainer.Spec.ImageSpec).To(Equal(fetchedImageSpec))
			})

			Context("when privileged", func() {
				BeforeEach(func() {
					taskPlan.Privileged = true
				})

				It("fetches a privileged image", func() {
					Expect(fakeDelegate.FetchImageCallCount()).To(Equal(1))
					_, _, _, privileged, _, _ := fakeDelegate.FetchImageArgsForCall(0)
					Expect(privileged).To(BeTrue())
				})
			})

			Context("when check skip interval is true", func() {
				BeforeEach(func() {
					taskPlan.CheckSkipInterval = true
				})

				It("fetches an image with forced check", func() {
					Expect(fakeDelegate.FetchImageCallCount()).To(Equal(1))
					_, _, _, _, _, skipInterval := fakeDelegate.FetchImageArgsForCall(0)
					Expect(skipInterval).To(BeTrue())
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
				Expect(fakeDelegate.FinishedCallCount()).To(Equal(1))
				_, status := fakeDelegate.FinishedArgsForCall(0)
				Expect(status).To(Equal(exec.ExitStatus(1)))
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
				fakeStreamer.StreamFileReturnsOnCall(0,
					io.NopCloser(strings.NewReader(sidecarYAML)), nil,
				)

				sidecarVolume := runtimetest.NewVolume("sidecar-source")
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

			It("includes inline sidecars in the container spec without file streaming", func() {
				Expect(stepErr).ToNot(HaveOccurred())
				Expect(chosenContainer.Spec.Sidecars).To(HaveLen(1))
				Expect(chosenContainer.Spec.Sidecars[0].Name).To(Equal("redis"))
				Expect(chosenContainer.Spec.Sidecars[0].Image).To(Equal("redis:7"))
				Expect(fakeStreamer.StreamFileCallCount()).To(Equal(0))
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
				fakeStreamer.StreamFileReturnsOnCall(0,
					io.NopCloser(strings.NewReader(sidecarYAML)), nil,
				)

				sidecarVolume := runtimetest.NewVolume("sidecar-source")
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
				fakeStreamer.StreamFileReturnsOnCall(0,
					io.NopCloser(strings.NewReader(sidecarYAML)), nil,
				)

				sidecarVolume := runtimetest.NewVolume("sidecar-source")
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
			var fakeResolver *imageresolvertesting.FakeResolver

			BeforeEach(func() {
				fakeResolver = &imageresolvertesting.FakeResolver{}
				fakeResolver.ResolveStub(func(ctx context.Context, repo string, tag string, auth *imageresolver.BasicAuth) (string, error) {
					return "sha256:resolved" + repo + tag, nil
				})

				taskStepOptions = []exec.TaskStepOption{exec.WithImageResolver(fakeResolver)}

				taskPlan.Sidecars = []atc.SidecarSource{
					{Config: &atc.SidecarConfig{
						Name:  "redis",
						Image: "redis:7",
						Ports: []atc.SidecarPort{{ContainerPort: 6379}},
					}},
				}
			})

			It("resolves bare image tags to pinned digests", func() {
				Expect(stepErr).ToNot(HaveOccurred())
				Expect(chosenContainer.Spec.Sidecars).To(HaveLen(1))
				Expect(chosenContainer.Spec.Sidecars[0].Image).To(Equal("redis@sha256:resolvedredis7"))
			})
		})

		Context("when sidecar image is already digest-pinned", func() {
			var fakeResolver *imageresolvertesting.FakeResolver

			BeforeEach(func() {
				fakeResolver = &imageresolvertesting.FakeResolver{}
				fakeResolver.ResolveReturns("sha256:shouldnotbecalled", nil)

				taskStepOptions = []exec.TaskStepOption{exec.WithImageResolver(fakeResolver)}

				taskPlan.Sidecars = []atc.SidecarSource{
					{Config: &atc.SidecarConfig{
						Name:  "redis",
						Image: "redis@sha256:abc123",
						Ports: []atc.SidecarPort{{ContainerPort: 6379}},
					}},
				}
			})

			It("skips resolution for already-pinned digests", func() {
				Expect(stepErr).ToNot(HaveOccurred())
				Expect(chosenContainer.Spec.Sidecars).To(HaveLen(1))
				Expect(chosenContainer.Spec.Sidecars[0].Image).To(Equal("redis@sha256:abc123"))
				Expect(fakeResolver.ResolveCallCount()).To(Equal(0))
			})
		})

		Context("when sidecar image resolution fails", func() {
			BeforeEach(func() {
				fakeResolver := &imageresolvertesting.FakeResolver{}
				fakeResolver.ResolveReturns("", fmt.Errorf("registry unreachable"))

				taskStepOptions = []exec.TaskStepOption{exec.WithImageResolver(fakeResolver)}

				taskPlan.Sidecars = []atc.SidecarSource{
					{Config: &atc.SidecarConfig{
						Name:  "redis",
						Image: "redis:7",
						Ports: []atc.SidecarPort{{ContainerPort: 6379}},
					}},
				}
			})

			It("falls through to the original tag-based image (best-effort)", func() {
				Expect(stepErr).ToNot(HaveOccurred())
				Expect(chosenContainer.Spec.Sidecars).To(HaveLen(1))
				Expect(chosenContainer.Spec.Sidecars[0].Image).To(Equal("redis:7"))
			})
		})

		Context("when sidecar image has a docker:/// prefix", func() {
			var fakeResolver *imageresolvertesting.FakeResolver

			BeforeEach(func() {
				fakeResolver = &imageresolvertesting.FakeResolver{}
				fakeResolver.ResolveStub(func(ctx context.Context, repo string, tag string, auth *imageresolver.BasicAuth) (string, error) {
					return "sha256:stripped" + repo + tag, nil
				})

				taskStepOptions = []exec.TaskStepOption{exec.WithImageResolver(fakeResolver)}

				taskPlan.Sidecars = []atc.SidecarSource{
					{Config: &atc.SidecarConfig{
						Name:  "redis",
						Image: "docker:///redis:7",
						Ports: []atc.SidecarPort{{ContainerPort: 6379}},
					}},
				}
			})

			It("strips the prefix before resolving the digest", func() {
				Expect(stepErr).ToNot(HaveOccurred())
				Expect(fakeResolver.ResolveCallCount()).To(Equal(1))
				_, resolvedRepo, resolvedTag, _ := fakeResolver.ResolveArgsForCall(0)
				Expect(resolvedRepo).To(Equal("redis"))
				Expect(resolvedTag).To(Equal("7"))
			})

			It("sets the resolved digest on the sidecar", func() {
				Expect(chosenContainer.Spec.Sidecars).To(HaveLen(1))
				Expect(chosenContainer.Spec.Sidecars[0].Image).To(Equal("redis@sha256:strippedredis7"))
			})
		})

		Context("when sidecar image has a docker:// prefix (double-slash)", func() {
			var fakeResolver *imageresolvertesting.FakeResolver

			BeforeEach(func() {
				fakeResolver = &imageresolvertesting.FakeResolver{}
				fakeResolver.ResolveStub(func(ctx context.Context, repo string, tag string, auth *imageresolver.BasicAuth) (string, error) {
					return "sha256:stripped" + repo + tag, nil
				})

				taskStepOptions = []exec.TaskStepOption{exec.WithImageResolver(fakeResolver)}

				taskPlan.Sidecars = []atc.SidecarSource{
					{Config: &atc.SidecarConfig{
						Name:  "mydb",
						Image: "docker://myregistry.example.com/mydb:v3",
						Ports: []atc.SidecarPort{{ContainerPort: 5432}},
					}},
				}
			})

			It("strips the prefix and resolves correctly", func() {
				Expect(stepErr).ToNot(HaveOccurred())
				Expect(fakeResolver.ResolveCallCount()).To(Equal(1))
				_, resolvedRepo, resolvedTag, _ := fakeResolver.ResolveArgsForCall(0)
				Expect(resolvedRepo).To(Equal("myregistry.example.com/mydb"))
				Expect(resolvedTag).To(Equal("v3"))
			})
		})

		Context("when sidecar image has a raw:/// prefix", func() {
			var fakeResolver *imageresolvertesting.FakeResolver

			BeforeEach(func() {
				fakeResolver = &imageresolvertesting.FakeResolver{}
				fakeResolver.ResolveStub(func(ctx context.Context, repo string, tag string, auth *imageresolver.BasicAuth) (string, error) {
					return "sha256:stripped" + repo + tag, nil
				})

				taskStepOptions = []exec.TaskStepOption{exec.WithImageResolver(fakeResolver)}

				taskPlan.Sidecars = []atc.SidecarSource{
					{Config: &atc.SidecarConfig{
						Name:  "nginx",
						Image: "raw:///nginx:alpine",
						Ports: []atc.SidecarPort{{ContainerPort: 80}},
					}},
				}
			})

			It("strips the prefix and resolves correctly", func() {
				Expect(stepErr).ToNot(HaveOccurred())
				Expect(fakeResolver.ResolveCallCount()).To(Equal(1))
				_, resolvedRepo, resolvedTag, _ := fakeResolver.ResolveArgsForCall(0)
				Expect(resolvedRepo).To(Equal("nginx"))
				Expect(resolvedTag).To(Equal("alpine"))
			})
		})

		Context("when sidecar image has a docker:/// prefix and is already digest-pinned", func() {
			var fakeResolver *imageresolvertesting.FakeResolver

			BeforeEach(func() {
				fakeResolver = &imageresolvertesting.FakeResolver{}
				fakeResolver.ResolveReturns("sha256:shouldnotbecalled", nil)

				taskStepOptions = []exec.TaskStepOption{exec.WithImageResolver(fakeResolver)}

				taskPlan.Sidecars = []atc.SidecarSource{
					{Config: &atc.SidecarConfig{
						Name:  "redis",
						Image: "docker:///redis@sha256:alreadypinned",
						Ports: []atc.SidecarPort{{ContainerPort: 6379}},
					}},
				}
			})

			It("skips resolution for already-pinned digests", func() {
				Expect(stepErr).ToNot(HaveOccurred())
				Expect(chosenContainer.Spec.Sidecars).To(HaveLen(1))
				Expect(chosenContainer.Spec.Sidecars[0].Image).To(Equal("docker:///redis@sha256:alreadypinned"))
				Expect(fakeResolver.ResolveCallCount()).To(Equal(0))
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
