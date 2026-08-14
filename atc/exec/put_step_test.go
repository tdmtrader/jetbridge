package exec_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/concourse/concourse/tracing"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"

	"code.cloudfoundry.org/clock"
	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/engine"
	"github.com/concourse/concourse/atc/event"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/exec/build"
	"github.com/concourse/concourse/atc/policy"
	"github.com/concourse/concourse/atc/resource"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/runtime/runtimetest"
	"github.com/concourse/concourse/atc/worker"
	"github.com/concourse/concourse/vars"
)

// imageFetchStepper answers the check and get substeps a delegate's FetchImage
// runs. The get substep does what the real get step does: register the fetched
// artifact and store a GetResult under its own plan ID.
type imageFetchStepper struct {
	artifact runtime.Artifact
	cache    db.ResourceCache

	ranPlans []atc.Plan
}

func (stepper *imageFetchStepper) step(plan atc.Plan) exec.Step {
	return stepFunc(func(_ context.Context, state exec.RunState) (bool, error) {
		stepper.ranPlans = append(stepper.ranPlans, plan)

		if plan.Get != nil {
			state.StoreResult(plan.ID, exec.GetResult{
				Name:          plan.Get.Name,
				ResourceCache: stepper.cache,
			})
			state.ArtifactRepository().RegisterArtifact(
				build.ArtifactName(plan.Get.Name),
				stepper.artifact,
				false,
			)
		}

		return true, nil
	})
}

func persistedFinishPuts(fixture *execDBFixture, build db.Build) []event.FinishPut {
	GinkgoHelper()
	var finished []event.FinishPut
	for _, e := range execBuildEvents(fixture, build) {
		if finish, ok := e.(event.FinishPut); ok {
			finished = append(finished, finish)
		}
	}
	return finished
}

func persistedSelectedWorkers(fixture *execDBFixture, build db.Build) []string {
	GinkgoHelper()
	var names []string
	for _, e := range execBuildEvents(fixture, build) {
		if selected, ok := e.(event.SelectedWorker); ok {
			names = append(names, selected.WorkerName)
		}
	}
	return names
}

// beforeSelectWorkerRecorder keeps the real delegate's behavior and only adds
// what PostgreSQL cannot show: whether the step asked before selecting a
// worker. BeforeSelectWorker writes nothing for a build that is not a check.
type beforeSelectWorkerRecorder struct {
	exec.PutDelegate

	callCount int
}

func (delegate *beforeSelectWorkerRecorder) BeforeSelectWorker(logger lager.Logger) error {
	delegate.callCount++
	return delegate.PutDelegate.BeforeSelectWorker(logger)
}

var _ = Describe("PutStep", func() {
	var (
		ctx    context.Context
		cancel func()

		fixture         *execDBFixture
		dbBuild         db.Build
		dbTeam          db.Team
		dbPipeline      db.Pipeline
		delegate        *beforeSelectWorkerRecorder
		delegateFactory exec.PutDelegateFactory

		stepper      exec.Stepper
		imageStepper *imageFetchStepper

		fakePool        *scriptedPool
		chosenWorker    *runtimetest.Worker
		chosenContainer *runtimetest.WorkerContainer

		putPlan *atc.PutPlan

		volume1 *runtimetest.Volume
		volume2 *runtimetest.Volume
		volume3 *runtimetest.Volume

		containerMetadata = db.ContainerMetadata{
			WorkingDirectory: resource.ResourcesDir("put"),
			Type:             db.ContainerTypePut,
			StepName:         "some-step",
		}

		planID        = atc.PlanID("some-plan-id")
		stepMetadata  exec.StepMetadata
		expectedOwner db.ContainerOwner

		state exec.RunState
		repo  *build.Repository

		putStep exec.Step
		stepOk  bool
		stepErr error

		versionResult resource.VersionResult

		defaultPutTimeout time.Duration = 0
	)

	putStepResource := func() db.Resource {
		GinkgoHelper()
		resource, found, err := dbPipeline.Resource("some-resource")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		return resource
	}

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())

		fixture = useExecDB()
		dbTeam, dbPipeline, _, dbBuild = createExecJobBuild(
			fixture,
			"some-team",
			atc.PipelineRef{Name: "some-pipeline"},
			atc.Config{
				Jobs: atc.JobConfigs{{Name: "some-job"}},
				Resources: atc.ResourceConfigs{{
					Name: "some-resource",
					Type: "some-resource-type",
				}},
			},
			"some-user",
		)

		stepMetadata = exec.StepMetadata{
			TeamID:       dbTeam.ID(),
			TeamName:     dbTeam.Name(),
			BuildID:      dbBuild.ID(),
			BuildName:    dbBuild.Name(),
			PipelineID:   dbPipeline.ID(),
			PipelineName: dbPipeline.Name(),
		}
		expectedOwner = db.NewBuildStepContainerOwner(stepMetadata.BuildID, planID, stepMetadata.TeamID)

		versionResult = resource.VersionResult{
			Version:  atc.Version{"some": "version"},
			Metadata: atc.Metadata{{Name: "some", Value: "metadata"}},
		}

		chosenWorker = runtimetest.NewWorker("worker").
			WithContainer(
				expectedOwner,
				runtimetest.NewContainer().WithProcess(
					runtime.ProcessSpec{
						ID:   "resource",
						Path: "/opt/resource/out",
						Args: []string{resource.ResourcesDir("put")},
					},
					runtimetest.ProcessStub{
						Attachable: true,
						Output:     versionResult,
					},
				),
				nil,
			)
		chosenContainer = chosenWorker.Containers[0]
		fakePool = new(scriptedPool)
		fakePool.FindOrSelectWorkerReturns(chosenWorker, nil)

		delegateFactory = putDelegateFactory(func(state exec.RunState) exec.PutDelegate {
			delegate = &beforeSelectWorkerRecorder{
				PutDelegate: engine.NewPutDelegate(dbBuild, planID, state, clock.NewClock(), policy.NoopChecker{}),
			}
			return delegate
		})

		imageStepper = new(imageFetchStepper)
		stepper = noopStepper

		state = exec.NewRunState(func(plan atc.Plan) exec.Step {
			return stepper(plan)
		}, vars.StaticVariables{
			"source-var": "super-secret-source",
			"params-var": "super-secret-params",
		})
		repo = state.ArtifactRepository()

		putPlan = &atc.PutPlan{
			Name:     "some-name",
			Resource: "some-resource",
			Type:     "some-resource-type",
			TypeImage: atc.TypeImage{
				BaseType: "some-resource-type",
			},
			Source: atc.Source{"some": "((source-var))"},
			Params: atc.Params{"some": "((params-var))"},
		}

		volume1 = runtimetest.NewVolume("volume1")
		volume2 = runtimetest.NewVolume("volume2")
		volume3 = runtimetest.NewVolume("volume3")

		repo.RegisterArtifact("input1", volume1, false)
		repo.RegisterArtifact("input2", volume2, true)
		repo.RegisterArtifact("input3", volume3, false)
	})

	AfterEach(func() {
		cancel()
	})

	JustBeforeEach(func() {
		plan := atc.Plan{
			ID:  atc.PlanID(planID),
			Put: putPlan,
		}

		putStep = exec.NewPutStep(
			plan.ID,
			*plan.Put,
			stepMetadata,
			containerMetadata,
			fakePool,
			delegateFactory,
			defaultPutTimeout,
		)

		stepOk, stepErr = putStep.Run(ctx, state)
		if stepErr != nil {
			testLogger.Error("putStep.Run-failed", stepErr)
		}
	})

	Describe("worker selection", func() {
		var ctx context.Context
		var workerSpec worker.Spec

		JustBeforeEach(func() {
			Expect(fakePool.FindOrSelectWorkerCallCount()).To(Equal(1))
			ctx, _, _, workerSpec = fakePool.FindOrSelectWorkerArgsForCall(0)
		})

		It("doesn't enforce a timeout", func() {
			_, ok := ctx.Deadline()
			Expect(ok).To(BeFalse())
		})

		It("emits a BeforeSelectWorker event", func() {
			Expect(delegate.callCount).To(Equal(1))
		})

		It("calls SelectWorker with the correct WorkerSpec", func() {
			Expect(workerSpec).To(Equal(
				worker.Spec{
					TeamID: stepMetadata.TeamID,
				},
			))
		})

		It("emits a SelectedWorker event", func() {
			Expect(persistedSelectedWorkers(fixture, dbBuild)).To(Equal([]string{"worker"}))
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

	Context("inputs", func() {
		Context("when inputs are specified with 'all' keyword", func() {
			BeforeEach(func() {
				putPlan.Inputs = &atc.InputsConfig{
					All: true,
				}
			})

			It("runs with all inputs", func() {
				Expect(chosenContainer.Spec.Inputs).To(ConsistOf([]runtime.Input{
					{
						Artifact:        volume1,
						DestinationPath: "/tmp/build/put/input1",
						FromCache:       false,
					},
					{
						Artifact:        volume2,
						DestinationPath: "/tmp/build/put/input2",
						FromCache:       true,
					},
					{
						Artifact:        volume3,
						DestinationPath: "/tmp/build/put/input3",
						FromCache:       false,
					},
				}))
			})
		})

		Context("when inputs are left blank", func() {
			BeforeEach(func() {
				putPlan.Params = atc.Params{
					"some-param": "input1/source",
				}
			})

			It("runs with detected inputs", func() {
				Expect(chosenContainer.Spec.Inputs).To(ConsistOf([]runtime.Input{
					{
						Artifact:        volume1,
						DestinationPath: "/tmp/build/put/input1",
						FromCache:       false,
					},
				}))
			})
		})

		Context("when only some inputs are specified ", func() {
			BeforeEach(func() {
				putPlan.Inputs = &atc.InputsConfig{
					Specified: []string{"input1", "input3"},
				}
			})

			It("runs with specified inputs", func() {
				Expect(chosenContainer.Spec.Inputs).To(ConsistOf([]runtime.Input{
					{
						Artifact:        volume1,
						DestinationPath: "/tmp/build/put/input1",
						FromCache:       false,
					},
					{
						Artifact:        volume3,
						DestinationPath: "/tmp/build/put/input3",
						FromCache:       false,
					},
				}))
			})
		})

		Context("when an empty list of inputs is specified", func() {
			BeforeEach(func() {
				putPlan.Inputs = &atc.InputsConfig{
					Specified: []string{},
				}
			})

			It("runs with no inputs", func() {
				Expect(chosenContainer.Spec.Inputs).To(Equal([]runtime.Input{}))
			})
		})

		Context("[PS-06] when a specified input is not found in the artifact repository", func() {
			BeforeEach(func() {
				putPlan.Inputs = &atc.InputsConfig{
					Specified: []string{"input1", "nonexistent-input"},
				}
			})

			It("returns a PutInputNotFoundError naming the missing input", func() {
				Expect(stepErr).To(HaveOccurred())
				Expect(stepErr).To(BeAssignableToTypeOf(exec.PutInputNotFoundError{}))
				Expect(stepErr.Error()).To(ContainSubstring("nonexistent-input"))
			})

			It("does not select a worker or run the resource script", func() {
				Expect(fakePool.FindOrSelectWorkerCallCount()).To(Equal(0))
			})
		})

		Context("when the inputs are detected", func() {
			BeforeEach(func() {
				putPlan.Inputs = &atc.InputsConfig{
					Detect: true,
				}
			})

			Context("when the params are only strings", func() {
				BeforeEach(func() {
					putPlan.Params = atc.Params{
						"some-param":    "input1/source",
						"another-param": "does-not-exist",
						"number-param":  123,
					}
				})

				It("runs with detected inputs", func() {
					Expect(chosenContainer.Spec.Inputs).To(ConsistOf([]runtime.Input{
						{
							Artifact:        volume1,
							DestinationPath: "/tmp/build/put/input1",
						},
					}))
				})
			})

			Context("when the params have maps and slices", func() {
				BeforeEach(func() {
					putPlan.Params = atc.Params{
						"some-slice": []any{
							[]any{"input1/source", "does-not-exist", 123},
							[]any{"does not exist-2"},
						},
						"some-map": map[string]any{
							"key": "input2/source",
						},
					}
				})

				It("runs with detected inputs", func() {
					Expect(chosenContainer.Spec.Inputs).To(ConsistOf([]runtime.Input{
						{
							Artifact:        volume1,
							DestinationPath: "/tmp/build/put/input1",
							FromCache:       false,
						},
						{
							Artifact:        volume2,
							DestinationPath: "/tmp/build/put/input2",
							FromCache:       true,
						},
					}))
				})
			})

			Context("when the params contains . and ..", func() {
				BeforeEach(func() {
					putPlan.Params = atc.Params{
						"some-param": "./input1/source",
						"some-map": map[string]any{
							"key": "../input2/source",
						},
					}
				})

				It("runs with detected inputs", func() {
					Expect(chosenContainer.Spec.Inputs).To(ConsistOf([]runtime.Input{
						{
							Artifact:        volume1,
							DestinationPath: "/tmp/build/put/input1",
							FromCache:       false,
						},
						{
							Artifact:        volume2,
							DestinationPath: "/tmp/build/put/input2",
							FromCache:       true,
						},
					}))
				})
			})

			Context("when only inputs are from cache ", func() {
				BeforeEach(func() {
					putPlan.Inputs = &atc.InputsConfig{
						Specified: []string{"input2"},
					}
				})

				It("runs with cached inputs", func() {
					Expect(chosenContainer.Spec.Inputs).To(ConsistOf([]runtime.Input{
						{
							Artifact:        volume2,
							DestinationPath: "/tmp/build/put/input2",
							FromCache:       true,
						},
					}))
				})
			})
		})
	})

	It("saves the build output", func() {
		_, outputs, err := dbBuild.Resources()
		Expect(err).NotTo(HaveOccurred())
		Expect(outputs).To(ConsistOf(db.BuildOutput{
			Name:    "some-name",
			Version: atc.Version{"some": "version"},
		}))

		version, found, err := putStepResource().FindVersion(atc.Version{"some": "version"})
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(version.Metadata()).To(Equal(db.ResourceConfigMetadataFields{
			{Name: "some", Value: "metadata"},
		}))

		config, err := fixture.ResourceConfigFactory.FindOrCreateResourceConfig(
			"some-resource-type",
			atc.Source{"some": "super-secret-source"},
			nil,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(putStepResource().ResourceConfigID()).To(Equal(config.ID()))
	})

	Context("when using a custom resource type", func() {
		var fetchedImageArtifact *runtimetest.Volume

		BeforeEach(func() {
			putPlan.TypeImage.GetPlan = &atc.Plan{
				ID: "1/image-get",
				Get: &atc.GetPlan{
					Name:   "some-custom-type",
					Type:   "another-custom-type",
					Source: atc.Source{"some-custom": "((source-var))"},
					Params: atc.Params{"some-custom": "((params-var))"},
				},
			}

			putPlan.TypeImage.CheckPlan = &atc.Plan{
				ID: "1/image-check",
				Check: &atc.CheckPlan{
					Name:   "some-custom-type",
					Type:   "another-custom-type",
					Source: atc.Source{"some-custom": "((source-var))"},
				},
			}

			putPlan.Type = "some-custom-type"
			putPlan.TypeImage.BaseType = "registry-image"

			fetchedImageArtifact = runtimetest.NewVolume("some-volume")
			imageStepper.artifact = fetchedImageArtifact
			stepper = imageStepper.step
		})

		It("fetches the resource type image and uses it for the container", func() {
			Expect(imageStepper.ranPlans).To(Equal([]atc.Plan{
				*putPlan.TypeImage.CheckPlan,
				*putPlan.TypeImage.GetPlan,
			}))
			Expect(chosenContainer.Spec.ImageSpec.Privileged).To(BeFalse())
		})

		It("sets the worker spec with teamID", func() {
			Expect(fakePool.FindOrSelectWorkerCallCount()).To(Equal(1))
			_, _, _, workerSpec := fakePool.FindOrSelectWorkerArgsForCall(0)

			Expect(workerSpec).To(Equal(worker.Spec{
				TeamID: stepMetadata.TeamID,
			}))
		})

		It("sets the image spec in the container spec", func() {
			Expect(chosenContainer.Spec.ImageSpec).To(Equal(runtime.ImageSpec{
				ImageArtifact: fetchedImageArtifact,
				ResourceType:  "some-custom-type",
			}))
		})

		It("saves the build output using the custom type's resource cache", func() {
			config, err := fixture.ResourceConfigFactory.FindOrCreateResourceConfig(
				"some-custom-type",
				atc.Source{"some": "super-secret-source"},
				nil,
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(putStepResource().ResourceConfigID()).To(Equal(config.ID()))
		})

		Context("when the resource type is privileged", func() {
			BeforeEach(func() {
				putPlan.TypeImage.Privileged = true
			})

			It("fetches the image with privileged", func() {
				Expect(chosenContainer.Spec.ImageSpec.Privileged).To(BeTrue())
			})
		})
	})

	Context("when the plan specifies a timeout", func() {
		BeforeEach(func() {
			putPlan.Timeout = "1ms"

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
				putPlan.Timeout = "bogus"
			})

			It("fails miserably", func() {
				Expect(stepErr).To(MatchError("parse timeout: time: invalid duration \"bogus\""))
			})
		})
	})

	Context("when there is default put timeout", func() {
		BeforeEach(func() {
			defaultPutTimeout = time.Minute * 30
		})

		It("enforces it on the put", func() {
			t, ok := chosenContainer.ContextOfRun().Deadline()
			Expect(ok).To(BeTrue())
			Expect(t).To(BeTemporally("~", time.Now().Add(time.Minute*30), time.Minute))
		})
	})

	Context("when there is default put timeout and the plan specifies a timeout also", func() {
		BeforeEach(func() {
			defaultPutTimeout = time.Minute * 30
			putPlan.Timeout = "1h"
		})

		It("enforces the plan's timeout on the put", func() {
			t, ok := chosenContainer.ContextOfRun().Deadline()
			Expect(ok).To(BeTrue())
			Expect(t).To(BeTemporally("~", time.Now().Add(time.Hour), time.Minute))
		})
	})

	Context("when tracing is enabled", func() {
		var spanRecorder *tracetest.SpanRecorder
		var runSpanContext oteltrace.SpanContext

		BeforeEach(func() {
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

	Describe("invoked resource", func() {
		var invokedResource resource.Resource

		BeforeEach(func() {
			chosenContainer.ProcessDefs[0].Stub.Do = func(_ context.Context, p *runtimetest.Process) error {
				return json.NewDecoder(p.Stdin()).Decode(&invokedResource)
			}
		})

		It("runs the script with the correct source and params", func() {
			Expect(invokedResource.Source).To(Equal(atc.Source{"some": "super-secret-source"}))
			Expect(invokedResource.Params).To(Equal(atc.Params{"some": "super-secret-params"}))
		})
	})

	Context("when the step.Plan.Resource is blank", func() {
		BeforeEach(func() {
			putPlan.Resource = ""
		})

		It("is successful", func() {
			Expect(stepOk).To(BeTrue())
		})

		It("does not save the build output", func() {
			_, outputs, err := dbBuild.Resources()
			Expect(err).NotTo(HaveOccurred())
			Expect(outputs).To(BeEmpty())
		})
	})

	Context("when the script succeeds", func() {
		It("finishes via the delegate", func() {
			finished := persistedFinishPuts(fixture, dbBuild)
			Expect(finished).To(HaveLen(1))
			Expect(finished[0].ExitStatus).To(Equal(0))
			Expect(finished[0].CreatedVersion).To(Equal(atc.Version{"some": "version"}))
			Expect(finished[0].CreatedMetadata).To(Equal(atc.Metadata{{Name: "some", Value: "metadata"}}))
		})

		It("stores the version as the step result", func() {
			var val atc.Version
			Expect(state.Result(planID, &val)).To(BeTrue())
			Expect(val).To(Equal(versionResult.Version))
		})

		It("is successful", func() {
			Expect(stepOk).To(BeTrue())
		})
	})

	Context("when running the script exits unsuccessfully", func() {
		BeforeEach(func() {
			chosenContainer.ProcessDefs[0].Stub.ExitStatus = 42
		})

		It("finishes the step via the delegate", func() {
			finished := persistedFinishPuts(fixture, dbBuild)
			Expect(finished).To(HaveLen(1))
			Expect(finished[0].ExitStatus).To(Equal(42))
			Expect(finished[0].CreatedVersion).To(BeNil())
			Expect(finished[0].CreatedMetadata).To(BeNil())
		})

		It("returns nil", func() {
			Expect(stepErr).ToNot(HaveOccurred())
		})

		It("is not successful", func() {
			Expect(stepOk).To(BeFalse())
		})
	})

	Context("when running the script exits with an error", func() {
		disaster := errors.New("oh no")

		BeforeEach(func() {
			chosenContainer.ProcessDefs[0].Stub.Err = disaster.Error()
		})

		It("does not finish the step via the delegate", func() {
			Expect(persistedFinishPuts(fixture, dbBuild)).To(BeEmpty())
		})

		It("returns the error", func() {
			Expect(stepErr).To(MatchError(disaster))
		})

		It("is not successful", func() {
			Expect(stepOk).To(BeFalse())
		})
	})
})
