package exec_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"code.cloudfoundry.org/clock"
	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/engine"
	"github.com/concourse/concourse/atc/event"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/imageresolver/imageresolvertesting"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/policy"
	"github.com/concourse/concourse/atc/resource"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/runtime/runtimetest"
	"github.com/concourse/concourse/atc/worker"
	"github.com/concourse/concourse/tracing"
	"github.com/concourse/concourse/vars"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5/pgconn"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/time/rate"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// saveVersionsErrorScope preserves a healthy PostgreSQL-backed scope while
// replacing only the method needed to exercise CheckStep's persistence errors.
type saveVersionsErrorScope struct {
	db.ResourceConfigScope
	err error
}

func (scope saveVersionsErrorScope) SaveVersions(db.SpanContext, []atc.Version) error {
	return scope.err
}

// releaseCountingLock counts releases of whatever lock the real delegate
// handed out; nothing in PostgreSQL distinguishes "released" from "never held"
// for the no-op lock a non-resource check gets.
type releaseCountingLock struct {
	lock.Lock
	releases *int
}

func (l releaseCountingLock) Release() error {
	*l.releases++
	return l.Lock.Release()
}

// recordingCheckDelegate keeps every behavior of the real delegate, recording
// the arguments the build event stream does not carry and providing the two
// fault injections PostgreSQL cannot perform on demand.
type recordingCheckDelegate struct {
	exec.CheckDelegate

	onScope     func(db.ResourceConfig, db.ResourceConfigScope)
	wrapScope   func(db.ResourceConfigScope) db.ResourceConfigScope
	onWaitToRun func(db.ResourceConfigScope)
	pointErr    error

	beforeSelectWorkerCount int
	containerOwnerCount     int
	configs                 []db.ResourceConfig
	pointedScopes           []db.ResourceConfigScope
	startTimeUpdates        []checkStartTimeUpdate
	lockReleases            int
	span                    trace.Span
}

type checkStartTimeUpdate struct {
	scope       db.ResourceConfigScope
	nestedCheck bool
}

func (delegate *recordingCheckDelegate) StartSpan(ctx context.Context, component string, attrs tracing.Attrs) (context.Context, trace.Span) {
	ctx, span := delegate.CheckDelegate.StartSpan(ctx, component, attrs)
	delegate.span = span
	return ctx, span
}

func (delegate *recordingCheckDelegate) BeforeSelectWorker(logger lager.Logger) error {
	delegate.beforeSelectWorkerCount++
	return delegate.CheckDelegate.BeforeSelectWorker(logger)
}

func (delegate *recordingCheckDelegate) ContainerOwner(planID atc.PlanID) db.ContainerOwner {
	delegate.containerOwnerCount++
	return delegate.CheckDelegate.ContainerOwner(planID)
}

func (delegate *recordingCheckDelegate) FindOrCreateScope(config db.ResourceConfig) (db.ResourceConfigScope, error) {
	scope, err := delegate.CheckDelegate.FindOrCreateScope(config)
	if err != nil {
		return nil, err
	}

	delegate.configs = append(delegate.configs, config)
	if delegate.onScope != nil {
		delegate.onScope(config, scope)
	}
	if delegate.wrapScope != nil {
		return delegate.wrapScope(scope), nil
	}
	return scope, nil
}

func (delegate *recordingCheckDelegate) WaitToRun(ctx context.Context, scope db.ResourceConfigScope) (lock.Lock, bool, error) {
	if delegate.onWaitToRun != nil {
		delegate.onWaitToRun(scope)
	}

	acquired, run, err := delegate.CheckDelegate.WaitToRun(ctx, scope)
	if acquired == nil {
		return nil, run, err
	}
	return releaseCountingLock{Lock: acquired, releases: &delegate.lockReleases}, run, err
}

func (delegate *recordingCheckDelegate) PointToCheckedConfig(scope db.ResourceConfigScope) error {
	delegate.pointedScopes = append(delegate.pointedScopes, scope)
	if delegate.pointErr != nil {
		return delegate.pointErr
	}
	return delegate.CheckDelegate.PointToCheckedConfig(scope)
}

func (delegate *recordingCheckDelegate) UpdateScopeLastCheckStartTime(scope db.ResourceConfigScope, nestedCheck bool) (bool, int, error) {
	delegate.startTimeUpdates = append(delegate.startTimeUpdates, checkStartTimeUpdate{
		scope:       scope,
		nestedCheck: nestedCheck,
	})
	return delegate.CheckDelegate.UpdateScopeLastCheckStartTime(scope, nestedCheck)
}

var _ = Describe("CheckStep", func() {
	var (
		ctx    context.Context
		cancel context.CancelFunc

		planID                atc.PlanID
		runState              exec.RunState
		fixture               *execDBFixture
		resourceConfigFactory db.ResourceConfigFactory
		expectedConfig        db.ResourceConfig
		resourceConfig        db.ResourceConfig
		resourceConfigScope   db.ResourceConfigScope
		realBuild             db.Build
		realPipeline          db.Pipeline
		delegate              *recordingCheckDelegate
		delegateFactory       exec.CheckDelegateFactory
		defaultTimeout        time.Duration = 0

		scopeWrapper            func(db.ResourceConfigScope) db.ResourceConfigScope
		waitToRunHook           func(db.ResourceConfigScope)
		pointToCheckedConfigErr error

		stepper      exec.Stepper
		imageStepper *imageFetchStepper

		fakePool        *scriptedPool
		chosenWorker    *runtimetest.Worker
		chosenContainer *runtimetest.WorkerContainer

		stepMetadata      exec.StepMetadata
		checkStep         exec.Step
		checkPlan         atc.CheckPlan
		containerMetadata db.ContainerMetadata

		stepOk  bool
		stepErr error

		expectedOwner db.ContainerOwner

		checkStepOpts []exec.CheckStepOption
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		fixture = useExecDB()
		currentTeam, pipeline, _, build := createExecJobBuild(
			fixture,
			"some-team",
			atc.PipelineRef{Name: "some-pipeline"},
			atc.Config{
				Jobs: atc.JobConfigs{{Name: "some-job"}},
				Resources: atc.ResourceConfigs{{
					Name:   "some-resource",
					Type:   "some-base-type",
					Source: atc.Source{"some": "super-secret-source"},
				}},
				ResourceTypes: atc.ResourceTypes{{
					Name:   "some-resource-type",
					Type:   "some-base-type",
					Source: atc.Source{"some": "super-secret-source"},
				}},
			},
			"some-user",
		)
		realBuild = build
		realPipeline = pipeline
		resourceConfigFactory = fixture.ResourceConfigFactory
		var err error
		expectedConfig, err = resourceConfigFactory.FindOrCreateResourceConfig(
			"some-base-type",
			atc.Source{"some": "super-secret-source"},
			nil,
		)
		Expect(err).NotTo(HaveOccurred())
		resourceConfig = expectedConfig
		resourceConfigScope, err = resourceConfig.FindOrCreateScope(nil)
		Expect(err).NotTo(HaveOccurred())

		planID = "some-plan-id"

		imageStepper = new(imageFetchStepper)
		stepper = noopStepper

		runState = exec.NewRunState(func(plan atc.Plan) exec.Step {
			return stepper(plan)
		}, vars.StaticVariables{"source-var": "super-secret-source"})

		scopeWrapper = nil
		waitToRunHook = nil
		pointToCheckedConfigErr = nil

		delegateFactory = checkDelegateFactory(func(state exec.RunState) exec.CheckDelegate {
			delegate = &recordingCheckDelegate{
				CheckDelegate: engine.NewCheckDelegate(
					realBuild,
					atc.Plan{ID: planID, Check: &checkPlan},
					state,
					clock.NewClock(),
					db.NewResourceCheckRateLimiter(rate.Inf, 0, time.Minute, nil, time.Minute, clock.NewClock()),
					policy.NoopChecker{},
				),
				onScope: func(config db.ResourceConfig, scope db.ResourceConfigScope) {
					resourceConfig = config
					resourceConfigScope = scope
				},
				wrapScope:   scopeWrapper,
				onWaitToRun: waitToRunHook,
				pointErr:    pointToCheckedConfigErr,
			}
			return delegate
		})

		stepMetadata = exec.StepMetadata{
			TeamID:  currentTeam.ID(),
			BuildID: realBuild.ID(),
		}
		expectedOwner = db.NewBuildStepContainerOwner(stepMetadata.BuildID, planID, stepMetadata.TeamID)

		chosenWorker = runtimetest.NewWorker("worker").
			WithContainer(
				expectedOwner,
				runtimetest.NewContainer().WithProcess(
					runtime.ProcessSpec{
						Path: "/opt/resource/check",
					},
					runtimetest.ProcessStub{},
				),
				nil,
			)
		chosenContainer = chosenWorker.Containers[0]
		fakePool = new(scriptedPool)
		fakePool.FindOrSelectWorkerReturns(chosenWorker, nil)

		containerMetadata = db.ContainerMetadata{}

		checkPlan = atc.CheckPlan{
			Name:   "some-name",
			Type:   "some-base-type",
			Source: atc.Source{"some": "((source-var))"},
			TypeImage: atc.TypeImage{
				BaseType: "some-base-type",
			},
		}

		containerMetadata = db.ContainerMetadata{
			User: "test-user",
		}

	})

	AfterEach(func() {
		cancel()
	})

	JustBeforeEach(func() {
		checkStep = exec.NewCheckStep(
			planID,
			checkPlan,
			stepMetadata,
			resourceConfigFactory,
			containerMetadata,
			fakePool,
			delegateFactory,
			defaultTimeout,
			checkStepOpts...,
		)

		stepOk, stepErr = checkStep.Run(ctx, runState)
	})

	Context("with a reasonable configuration", func() {
		It("emits an Initializing event", func() {
			Expect(execBuildEventTypes(fixture, realBuild)).To(ContainElement(event.EventTypeInitializeCheck))
		})

		Context("when not running", func() {
			BeforeEach(func() {
				checkPlan.Interval = atc.CheckEvery{Never: true}
			})

			It("doesn't run the step and succeeds", func() {
				Expect(stepErr).ToNot(HaveOccurred())
				Expect(stepOk).To(BeTrue())

				Expect(chosenContainer.RunningProcesses()).To(BeEmpty())
			})

			Context("when there is a latest version", func() {
				BeforeEach(func() {
					Expect(resourceConfigScope.SaveVersions(
						db.SpanContext{},
						[]atc.Version{{"some": "latest-version"}},
					)).To(Succeed())
				})

				It("stores the latest version as the step result", func() {
					var val atc.Version
					Expect(runState.Result(planID, &val)).To(BeTrue())
					Expect(val).To(Equal(atc.Version{"some": "latest-version"}))
				})
			})

			Context("when there is no version", func() {
				It("does not store a version", func() {
					var dst any
					Expect(runState.Result(planID, &dst)).To(BeFalse())
				})
			})
		})

		Context("running", func() {
			var invokedResource resource.Resource

			BeforeEach(func() {
				invokedResource = resource.Resource{}

				chosenContainer.ProcessDefs[0].Stub.Do = func(_ context.Context, p *runtimetest.Process) error {
					return json.NewDecoder(p.Stdin()).Decode(&invokedResource)
				}
			})

			Context("when given a from version", func() {
				BeforeEach(func() {
					checkPlan.FromVersion = atc.Version{"from": "version"}
				})

				It("constructs the resource with the version", func() {
					Expect(invokedResource.Version).To(Equal(checkPlan.FromVersion))
				})
			})

			Context("when not given a from version", func() {
				BeforeEach(func() {
					checkPlan.FromVersion = nil
					waitToRunHook = func(scope db.ResourceConfigScope) {
						// Persisting the version as the run is granted proves LatestVersion
						// is queried after WaitToRun rather than before it.
						Expect(scope.SaveVersions(db.SpanContext{}, []atc.Version{{"latest": "version"}})).To(Succeed())
					}
				})

				It("finds the latest version itself - it's a strong, independent check step who dont need no plan", func() {
					Expect(invokedResource.Version).To(Equal(atc.Version{"latest": "version"}))
				})
			})

			Describe("worker selection", func() {
				var ctx context.Context
				var workerSpec worker.Spec

				JustBeforeEach(func() {
					Expect(fakePool.FindOrSelectWorkerCallCount()).To(Equal(1))
					ctx, _, _, workerSpec = fakePool.FindOrSelectWorkerArgsForCall(0)
				})

				It("get container owner from delegate", func() {
					Expect(delegate.containerOwnerCount).To(Equal(1))
				})

				It("doesn't enforce a timeout", func() {
					_, ok := ctx.Deadline()
					Expect(ok).To(BeFalse())
				})

				It("calls SelectWorker with the correct WorkerSpec", func() {
					Expect(workerSpec.TeamID).To(Equal(stepMetadata.TeamID))
				})

				It("emits a BeforeSelectWorker event", func() {
					Expect(delegate.beforeSelectWorkerCount).To(Equal(1))
				})

				It("emits a SelectedWorker event", func() {
					Expect(persistedSelectedWorkers(fixture, realBuild)).To(Equal([]string{"worker"}))
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

			Describe("running the check step", func() {
				Context("when using a custom resource type", func() {
					var (
						fetchedImageArtifact *runtimetest.Volume
						imageResourceCache   db.ResourceCache
					)

					BeforeEach(func() {
						checkPlan.TypeImage.GetPlan = &atc.Plan{
							ID: "1/image-get",
							Get: &atc.GetPlan{
								Name:   "some-custom-type",
								Type:   "another-custom-type",
								Source: atc.Source{"some-custom": "((source-var))"},
								Params: atc.Params{"some-custom": "((params-var))"},
							},
						}

						checkPlan.TypeImage.CheckPlan = &atc.Plan{
							ID: "1/image-check",
							Check: &atc.CheckPlan{
								Name:   "some-custom-type",
								Type:   "another-custom-type",
								Source: atc.Source{"some-custom": "((source-var))"},
							},
						}

						checkPlan.Type = "some-custom-type"

						fetchedImageArtifact = runtimetest.NewVolume("some-volume")

						var err error
						imageResourceCache, err = fixture.ResourceCacheFactory.FindOrCreateResourceCache(
							db.ForBuild(realBuild.ID()),
							"another-custom-type",
							atc.Version{"some": "image-version"},
							atc.Source{"some-custom": "super-secret-source"},
							nil,
							nil,
						)
						Expect(err).NotTo(HaveOccurred())

						imageStepper.artifact = fetchedImageArtifact
						imageStepper.cache = imageResourceCache
						stepper = imageStepper.step
					})

					It("fetches the resource type image", func() {
						Expect(imageStepper.ranPlans).To(Equal([]atc.Plan{
							*checkPlan.TypeImage.CheckPlan,
							*checkPlan.TypeImage.GetPlan,
						}))
						Expect(chosenContainer.Spec.ImageSpec.Privileged).To(BeFalse())
					})

					It("sets the image spec in the container spec", func() {
						Expect(chosenContainer.Spec.ImageSpec).To(Equal(runtime.ImageSpec{
							ImageArtifact: fetchedImageArtifact,
							ResourceType:  "some-custom-type",
						}))
					})

					It("creates the resource config using the image resource cache", func() {
						Expect(resourceConfig.CreatedByResourceCache()).NotTo(BeNil())
						Expect(resourceConfig.CreatedByResourceCache().ID()).To(Equal(imageResourceCache.ID()))
						expectedCustomConfig, err := fixture.ResourceConfigFactory.FindOrCreateResourceConfig(
							"some-custom-type",
							atc.Source{"some": "super-secret-source"},
							imageResourceCache,
						)
						Expect(err).NotTo(HaveOccurred())
						Expect(resourceConfig.ID()).To(Equal(expectedCustomConfig.ID()))
					})

					Context("when the resource type is privileged", func() {
						BeforeEach(func() {
							checkPlan.TypeImage.Privileged = true
						})

						It("fetches the image with privileged", func() {
							Expect(chosenContainer.Spec.ImageSpec.Privileged).To(BeTrue())
						})
					})

					Context("when the timeout is bogus", func() {
						BeforeEach(func() {
							checkPlan.Timeout = "bogus"
						})

						It("fails miserably", func() {
							Expect(stepErr).To(MatchError(ContainSubstring("parse timeout: time: invalid duration \"bogus\"")))
						})
					})
				})

				Context("when there is default check timeout", func() {
					BeforeEach(func() {
						defaultTimeout = time.Minute * 30
					})

					It("enforces it on the check", func() {
						t, ok := chosenContainer.ContextOfRun().Deadline()
						Expect(ok).To(BeTrue())
						Expect(t).To(BeTemporally("~", time.Now().Add(time.Minute*30), time.Minute))
					})
				})

				Context("when there is default check timeout and the plan specifies a timeout also", func() {
					BeforeEach(func() {
						defaultTimeout = time.Minute * 30
						checkPlan.Timeout = "1h"
					})

					It("enforces the plan's timeout on the check", func() {
						t, ok := chosenContainer.ContextOfRun().Deadline()
						Expect(ok).To(BeTrue())
						Expect(t).To(BeTemporally("~", time.Now().Add(time.Hour), time.Minute))
					})
				})

				Context("when the plan is for a resource", func() {
					BeforeEach(func() {
						checkPlan.Resource = "some-resource"
					})

					It("points the resource or resource type to the scope", func() {
						resource, found, err := realPipeline.Resource("some-resource")
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())
						Expect(resource.ResourceConfigScopeID()).To(Equal(resourceConfigScope.ID()))
					})

					It("uses build step container owner", func() {
						Expect(chosenContainer.RunningProcesses()).To(HaveLen(1))
					})

					It("update scope's check start time", func() {
						Expect(delegate.startTimeUpdates).To(HaveLen(1))
						Expect(delegate.startTimeUpdates[0].scope.ID()).To(Equal(resourceConfigScope.ID()))
						Expect(delegate.startTimeUpdates[0].nestedCheck).To(BeFalse())
					})
				})

				Context("when the plan is nested", func() {
					BeforeEach(func() {
						checkPlan.Resource = ""
						checkPlan.ResourceType = "some-resource-type"
					})

					It("points the resource or resource type to the scope", func() {
						resourceType, found, err := realPipeline.ResourceType("some-resource-type")
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())
						Expect(resourceType.ResourceConfigScopeID()).To(Equal(resourceConfigScope.ID()))
					})

					It("uses delegate's container owner", func() {
						Expect(delegate.containerOwnerCount).To(Equal(1))
						Expect(chosenContainer.RunningProcesses()).To(HaveLen(1))
					})

					It("update scope's check start time", func() {
						Expect(delegate.startTimeUpdates).To(HaveLen(1))
						Expect(delegate.startTimeUpdates[0].scope.ID()).To(Equal(resourceConfigScope.ID()))
						Expect(delegate.startTimeUpdates[0].nestedCheck).To(BeTrue())
					})
				})

				Context("when the plan specifies a timeout", func() {
					BeforeEach(func() {
						checkPlan.Timeout = "1ms"

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
						Expect(execBuildErrorMessages(fixture, realBuild)).To(Equal([]string{exec.TimeoutLogMessage}))
					})
				})

				Context("uses containerspec", func() {
					It("with certs volume mount", func() {
						Expect(chosenContainer.Spec.CertsBindMount).To(BeTrue())
					})

					It("uses base type for image", func() {
						Expect(chosenContainer.Spec.ImageSpec).To(Equal(runtime.ImageSpec{
							ResourceType: "some-base-type",
						}))
					})

					It("does not set the workdir", func() {
						Expect(chosenContainer.Spec.Dir).To(Equal(""))
					})

					Context("when tracing is enabled", func() {
						var spanRecorder *tracetest.SpanRecorder
						var runSpanContext trace.SpanContext

						BeforeEach(func() {
							spanRecorder = new(tracetest.SpanRecorder)
							tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder), sdktrace.WithSyncer(tracetest.NewInMemoryExporter()))
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
				})
			})

			Context("with tracing configured", func() {
				var tracedVersion atc.Version

				BeforeEach(func() {
					exporter := tracetest.NewInMemoryExporter()
					tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
					tracing.ConfigureTraceProvider(tp)

					tracedVersion = atc.Version{"trace": "persisted"}
					chosenContainer.ProcessDefs[0].Stub.Output = []atc.Version{tracedVersion}
				})

				AfterEach(func() {
					tracing.Configured = false
				})

				It("propagates span context to scope", func() {
					version, found, err := resourceConfigScope.FindVersion(tracedVersion)
					Expect(err).NotTo(HaveOccurred())
					Expect(found).To(BeTrue())
					reloaded, err := version.Reload()
					Expect(err).NotTo(HaveOccurred())
					Expect(reloaded).To(BeTrue())
					traceID := delegate.span.SpanContext().TraceID().String()
					traceParent := version.SpanContext().Get("traceparent")
					Expect(traceParent).To(ContainSubstring(traceID))
				})
			})

			Context("having RunCheckStep succeed", func() {
				BeforeEach(func() {
					chosenContainer.ProcessDefs[0].Stub.Output = []atc.Version{
						{"version": "1"},
						{"version": "2"},
					}
				})

				It("succeeds", func() {
					Expect(stepOk).To(BeTrue())
				})

				It("saves the versions to the config scope", func() {
					Expect(delegate.configs).To(HaveLen(1))
					config := delegate.configs[0]
					Expect(config.ID()).To(Equal(expectedConfig.ID()))
					Expect(config.OriginBaseResourceType().Name).To(Equal("some-base-type"))
					Expect(resourceConfigScope.ResourceConfig().ID()).To(Equal(config.ID()))

					for _, expectedVersion := range []atc.Version{{"version": "1"}, {"version": "2"}} {
						version, found, err := resourceConfigScope.FindVersion(expectedVersion)
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())
						reloaded, err := version.Reload()
						Expect(err).NotTo(HaveOccurred())
						Expect(reloaded).To(BeTrue())
						Expect(atc.Version(version.Version())).To(Equal(expectedVersion))
					}
				})

				It("stores the latest version as the step result", func() {
					var val atc.Version
					Expect(runState.Result(planID, &val)).To(BeTrue())
					persistedVersion, found, err := resourceConfigScope.FindVersion(atc.Version{"version": "2"})
					Expect(err).NotTo(HaveOccurred())
					Expect(found).To(BeTrue())
					reloaded, err := persistedVersion.Reload()
					Expect(err).NotTo(HaveOccurred())
					Expect(reloaded).To(BeTrue())
					Expect(val).To(Equal(atc.Version(persistedVersion.Version())))
				})

				It("emits a successful Finished event", func() {
					Expect(execBuildFinishEvents(fixture, realBuild)).To(HaveLen(1))
					Expect(execBuildFinishEvents(fixture, realBuild)[0].Succeeded).To(BeTrue())
				})

				Context("when no versions are returned", func() {
					BeforeEach(func() {
						chosenContainer.ProcessDefs[0].Stub.Output = []atc.Version{}
					})

					It("succeeds", func() {
						Expect(stepErr).ToNot(HaveOccurred())
						Expect(stepOk).To(BeTrue())
					})

					It("does not store a version", func() {
						var dst any
						Expect(runState.Result(planID, &dst)).To(BeFalse())
					})
				})

				Context("before running the check", func() {
					var (
						lastCheckAtRun db.LastCheck
						lastCheckErr   error
					)

					BeforeEach(func() {
						chosenContainer.ProcessDefs[0].Stub.Do = func(_ context.Context, _ *runtimetest.Process) error {
							lastCheckAtRun, lastCheckErr = resourceConfigScope.LastCheck()
							return nil
						}
					})

					It("updates the scope's last check start time", func() {
						Expect(lastCheckErr).NotTo(HaveOccurred())
						Expect(lastCheckAtRun.StartTime).To(BeTemporally("~", time.Now(), time.Minute))
						Expect(chosenContainer.RunningProcesses()).To(HaveLen(1))
					})
				})

				Context("after saving", func() {
					It("updates the scope's last check end time", func() {
						lastCheck, err := resourceConfigScope.LastCheck()
						Expect(err).NotTo(HaveOccurred())
						Expect(lastCheck.EndTime).To(BeTemporally(">=", lastCheck.StartTime))
						Expect(lastCheck.Succeeded).To(BeTrue())
					})

					It("releases the lock", func() {
						_, found, err := resourceConfigScope.FindVersion(atc.Version{"version": "2"})
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())
						Expect(delegate.lockReleases).To(Equal(1))
					})
				})
			})

			Context("having the check step erroring", func() {
				BeforeEach(func() {
					chosenContainer.ProcessDefs[0].Stub.Err = "run-check-step-err"
				})

				It("errors", func() {
					Expect(stepErr).To(MatchError(ContainSubstring("run-check-step-err")))
				})

				It("updates the scope's last check end time", func() {
					lastCheck, err := resourceConfigScope.LastCheck()
					Expect(err).NotTo(HaveOccurred())
					Expect(lastCheck.EndTime).To(BeTemporally("~", time.Now(), time.Minute))
					Expect(lastCheck.Succeeded).To(BeFalse())
				})

				// Finished is for script success/failure, whereas this is an error
				It("does not emit a Finished event", func() {
					Expect(execBuildFinishEvents(fixture, realBuild)).To(BeEmpty())
				})
			})

			Context("with a script failure", func() {
				BeforeEach(func() {
					chosenContainer.ProcessDefs[0].Stub.ExitStatus = 42
				})

				It("does not error", func() {
					// don't return an error - the script output has already been
					// printed, and emitting an errored event would double it up
					Expect(stepErr).ToNot(HaveOccurred())
				})

				It("updates the scope's last check end time", func() {
					lastCheck, err := resourceConfigScope.LastCheck()
					Expect(err).NotTo(HaveOccurred())
					Expect(lastCheck.EndTime).To(BeTemporally("~", time.Now(), time.Minute))
					Expect(lastCheck.Succeeded).To(BeFalse())
				})

				It("emits a failed Finished event", func() {
					Expect(execBuildFinishEvents(fixture, realBuild)).To(HaveLen(1))
					Expect(execBuildFinishEvents(fixture, realBuild)[0].Succeeded).To(BeFalse())
				})
			})

			Context("having SaveVersions failing", func() {
				var expectedErr error

				BeforeEach(func() {
					expectedErr = errors.New("save-versions-err")
					scopeWrapper = func(scope db.ResourceConfigScope) db.ResourceConfigScope {
						return saveVersionsErrorScope{ResourceConfigScope: scope, err: expectedErr}
					}
				})

				It("errors", func() {
					Expect(stepErr).To(HaveOccurred())
					Expect(errors.Is(stepErr, expectedErr)).To(BeTrue())
				})
			})

			Context("when SaveVersions fails with FK violation (scope deleted by GC)", func() {
				BeforeEach(func() {
					scopeWrapper = func(scope db.ResourceConfigScope) db.ResourceConfigScope {
						return saveVersionsErrorScope{
							ResourceConfigScope: scope,
							err: fmt.Errorf(
								"save versions: %w",
								&pgconn.PgError{Code: pgerrcode.ForeignKeyViolation},
							),
						}
					}
				})

				It("does not error", func() {
					Expect(stepErr).NotTo(HaveOccurred())
				})

				It("finishes with failure (non-fatal)", func() {
					Expect(execBuildFinishEvents(fixture, realBuild)).To(HaveLen(1))
					Expect(execBuildFinishEvents(fixture, realBuild)[0].Succeeded).To(BeFalse())
				})
			})
		})
	})

	Context("having credentials in the config", func() {
		BeforeEach(func() {
			checkPlan.Source = atc.Source{"some": "((missing-cred))"}
		})

		Context("having cred evaluation failing", func() {
			It("errors", func() {
				Expect(stepErr).To(HaveOccurred())
			})
		})
	})

	Context("when PointToCheckedConfig fails with FK violation (scope deleted by GC)", func() {
		BeforeEach(func() {
			pointToCheckedConfigErr = &pgconn.PgError{Code: pgerrcode.ForeignKeyViolation}
		})

		It("does not error", func() {
			Expect(stepErr).NotTo(HaveOccurred())
		})

		It("finishes with failure (non-fatal)", func() {
			Expect(execBuildFinishEvents(fixture, realBuild)).To(HaveLen(1))
			Expect(execBuildFinishEvents(fixture, realBuild)[0].Succeeded).To(BeFalse())
		})
	})

	// MO-02, MO-03, MO-04: Check metrics
	Describe("Check Metrics", func() {
		BeforeEach(func() {
			// Drain any leftover metric state from other tests
			metric.Metrics.ChecksStarted.Delta()
			metric.Metrics.ChecksFinishedWithSuccess.Delta()
			metric.Metrics.ChecksFinishedWithError.Delta()
		})

		Context("when a check succeeds with versions", func() {
			BeforeEach(func() {
				chosenContainer.ProcessDefs[0].Stub.Output = []atc.Version{
					{"version": "1"},
				}
			})

			// MO-02: ChecksStarted incremented at execution start
			It("increments ChecksStarted", func() {
				Expect(stepOk).To(BeTrue())
				Expect(metric.Metrics.ChecksStarted.Delta()).To(BeNumerically("==", 1))
			})

			// MO-03: ChecksFinishedWithSuccess incremented on success
			It("increments ChecksFinishedWithSuccess", func() {
				Expect(stepOk).To(BeTrue())
				Expect(metric.Metrics.ChecksFinishedWithSuccess.Delta()).To(BeNumerically("==", 1))
			})

			It("does not increment ChecksFinishedWithError", func() {
				Expect(stepOk).To(BeTrue())
				Expect(metric.Metrics.ChecksFinishedWithError.Delta()).To(BeNumerically("==", 0))
			})
		})

		Context("when a check fails with non-zero exit", func() {
			BeforeEach(func() {
				chosenContainer.ProcessDefs[0].Stub.ExitStatus = 1
			})

			// MO-04: ChecksFinishedWithError incremented on failure
			It("increments ChecksFinishedWithError", func() {
				Expect(stepOk).To(BeFalse())
				Expect(metric.Metrics.ChecksFinishedWithError.Delta()).To(BeNumerically("==", 1))
			})

			It("increments ChecksStarted", func() {
				Expect(stepOk).To(BeFalse())
				Expect(metric.Metrics.ChecksStarted.Delta()).To(BeNumerically("==", 1))
			})

			It("does not increment ChecksFinishedWithSuccess", func() {
				Expect(stepOk).To(BeFalse())
				Expect(metric.Metrics.ChecksFinishedWithSuccess.Delta()).To(BeNumerically("==", 0))
			})
		})

		Context("when a check times out", func() {
			BeforeEach(func() {
				checkPlan.Timeout = "1ms"

				chosenContainer.ProcessDefs[0].Stub.Do = func(ctx context.Context, _ *runtimetest.Process) error {
					select {
					case <-ctx.Done():
						return fmt.Errorf("wrapped: %w", ctx.Err())
					case <-time.After(100 * time.Millisecond):
						return nil
					}
				}
			})

			It("increments ChecksFinishedWithError on timeout", func() {
				Expect(stepOk).To(BeFalse())
				Expect(metric.Metrics.ChecksFinishedWithError.Delta()).To(BeNumerically("==", 1))
			})

			It("increments ChecksStarted", func() {
				Expect(stepOk).To(BeFalse())
				Expect(metric.Metrics.ChecksStarted.Delta()).To(BeNumerically("==", 1))
			})
		})

		Context("when WaitToRun returns run=false", func() {
			BeforeEach(func() {
				checkPlan.Interval = atc.CheckEvery{Never: true}
			})

			It("does not increment ChecksStarted", func() {
				Expect(stepOk).To(BeTrue())
				Expect(metric.Metrics.ChecksStarted.Delta()).To(BeNumerically("==", 0))
			})
		})
	})

	Context("native resolution for registry-image", func() {
		var fakeResolver *imageresolvertesting.FakeResolver

		BeforeEach(func() {
			fakeResolver = new(imageresolvertesting.FakeResolver)
			checkStepOpts = []exec.CheckStepOption{exec.WithCheckResolver(fakeResolver)}

			checkPlan = atc.CheckPlan{
				Name: "some-registry-image",
				Type: "registry-image",
				Source: atc.Source{
					"repository": "gcr.io/my-project/my-image",
					"tag":        "latest",
				},
				TypeImage: atc.TypeImage{
					BaseType: "registry-image",
				},
			}
		})

		AfterEach(func() {
			checkStepOpts = nil
		})

		Context("when the resolver succeeds", func() {
			BeforeEach(func() {
				fakeResolver.ResolveReturns("sha256:abc123def456", nil)
			})

			It("resolves natively without creating a container", func() {
				Expect(stepErr).ToNot(HaveOccurred())
				Expect(stepOk).To(BeTrue())

				Expect(fakeResolver.ResolveCallCount()).To(Equal(1))
				_, repo, tag, auth := fakeResolver.ResolveArgsForCall(0)
				Expect(repo).To(Equal("gcr.io/my-project/my-image"))
				Expect(tag).To(Equal("latest"))
				Expect(auth).To(BeNil())

				// Should NOT have selected a worker or created a container.
				Expect(fakePool.FindOrSelectWorkerCallCount()).To(Equal(0))
			})

			It("saves the resolved version", func() {
				expectedVersion := atc.Version{"digest": "sha256:abc123def456"}
				version, found, err := resourceConfigScope.FindVersion(expectedVersion)
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				reloaded, err := version.Reload()
				Expect(err).NotTo(HaveOccurred())
				Expect(reloaded).To(BeTrue())
				Expect(atc.Version(version.Version())).To(Equal(expectedVersion))
			})

			It("stores the result in run state", func() {
				var val atc.Version
				Expect(runState.Result(planID, &val)).To(BeTrue())
				Expect(val).To(Equal(atc.Version{"digest": "sha256:abc123def456"}))
			})

			It("updates check timestamps and metrics", func() {
				Expect(metric.Metrics.ChecksStarted.Delta()).To(BeNumerically(">=", 1))
				Expect(metric.Metrics.ChecksFinishedWithSuccess.Delta()).To(BeNumerically(">=", 1))
			})
		})

		Context("with explicit username/password", func() {
			BeforeEach(func() {
				checkPlan.Source = atc.Source{
					"repository": "private.registry.io/image",
					"username":   "my-user",
					"password":   "my-pass",
				}
				fakeResolver.ResolveReturns("sha256:deadbeef", nil)
			})

			It("passes BasicAuth to the resolver", func() {
				Expect(stepErr).ToNot(HaveOccurred())
				Expect(fakeResolver.ResolveCallCount()).To(Equal(1))
				_, _, _, auth := fakeResolver.ResolveArgsForCall(0)
				Expect(auth).ToNot(BeNil())
				Expect(auth.Username).To(Equal("my-user"))
				Expect(auth.Password).To(Equal("my-pass"))
			})
		})

		Context("when the resolver fails", func() {
			BeforeEach(func() {
				fakeResolver.ResolveReturns("", errors.New("UNAUTHORIZED"))
			})

			It("returns an error and does not save versions", func() {
				Expect(stepErr).To(HaveOccurred())
				Expect(stepErr.Error()).To(ContainSubstring("UNAUTHORIZED"))
				_, found, err := resourceConfigScope.FindVersion(atc.Version{"digest": "sha256:abc123def456"})
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeFalse())
			})

			It("increments the error metric", func() {
				Expect(metric.Metrics.ChecksFinishedWithError.Delta()).To(BeNumerically(">=", 1))
			})
		})

		Context("when the type is not registry-image", func() {
			BeforeEach(func() {
				checkPlan.Type = "git"
				checkPlan.Source = atc.Source{"uri": "https://example.com/repo.git"}
				checkPlan.TypeImage = atc.TypeImage{BaseType: "git"}
			})

			It("falls back to container-based check", func() {
				// The resolver should NOT be called.
				Expect(fakeResolver.ResolveCallCount()).To(Equal(0))
				// A worker should be selected for the container check.
				Expect(fakePool.FindOrSelectWorkerCallCount()).To(Equal(1))
			})
		})
	})
})
