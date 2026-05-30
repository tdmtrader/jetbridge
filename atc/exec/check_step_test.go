package exec_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/concourse/concourse/atc/db/lock/lockfakes"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/exec/execfakes"
	"github.com/concourse/concourse/atc/imageresolver/imageresolvertesting"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/resource"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/runtime/runtimetest"
	"github.com/concourse/concourse/atc/worker"
	"github.com/concourse/concourse/tracing"
	"github.com/concourse/concourse/vars"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("CheckStep", func() {
	var (
		ctx    context.Context
		cancel context.CancelFunc

		planID                    atc.PlanID
		runState                  exec.RunState
		fakeResourceConfigFactory *dbfakes.FakeResourceConfigFactory
		fakeResourceConfig        *dbfakes.FakeResourceConfig
		fakeResourceConfigScope   *dbfakes.FakeResourceConfigScope
		fakeDelegate              *execfakes.FakeCheckDelegate
		fakeDelegateFactory       *execfakes.FakeCheckDelegateFactory
		spanCtx                   context.Context
		defaultTimeout            time.Duration = 0

		fakePool        *execfakes.FakePool
		chosenWorker    *runtimetest.Worker
		chosenContainer *runtimetest.WorkerContainer

		fakeStdout, fakeStderr io.Writer

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

		planID = "some-plan-id"

		runState = exec.NewRunState(noopStepper, vars.StaticVariables{"source-var": "super-secret-source"})
		fakeDelegateFactory = new(execfakes.FakeCheckDelegateFactory)
		fakeDelegate = new(execfakes.FakeCheckDelegate)

		stepMetadata = exec.StepMetadata{
			TeamID:  345,
			BuildID: 678,
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
		fakePool = new(execfakes.FakePool)
		fakePool.FindOrSelectWorkerReturns(chosenWorker, nil)

		spanCtx = context.Background()
		fakeDelegate.StartSpanReturns(spanCtx, tracing.NoopSpan)

		fakeStdout = bytes.NewBufferString("out")
		fakeDelegate.StdoutReturns(fakeStdout)

		fakeStderr = bytes.NewBufferString("err")
		fakeDelegate.StderrReturns(fakeStderr)

		fakeDelegate.ContainerOwnerReturns(expectedOwner)

		containerMetadata = db.ContainerMetadata{}

		fakeResourceConfigFactory = new(dbfakes.FakeResourceConfigFactory)
		fakeResourceConfig = new(dbfakes.FakeResourceConfig)
		fakeResourceConfig.IDReturns(501)
		fakeResourceConfig.OriginBaseResourceTypeReturns(&db.UsedBaseResourceType{
			ID:   502,
			Name: "some-base-type",
		})
		fakeResourceConfigFactory.FindOrCreateResourceConfigReturns(fakeResourceConfig, nil)

		fakeResourceConfigScope = new(dbfakes.FakeResourceConfigScope)
		fakeDelegate.FindOrCreateScopeReturns(fakeResourceConfigScope, nil)
		fakeDelegate.UpdateScopeLastCheckStartTimeStub = func(scope db.ResourceConfigScope, nestedCheck bool) (bool, int, error) {
			found, err := scope.UpdateLastCheckStartTime(int(time.Now().Unix()), nil)
			return found, 678, err
		}
		fakeDelegate.UpdateScopeLastCheckEndTimeStub = func(scope db.ResourceConfigScope, succeeded bool) (bool, error) {
			return scope.UpdateLastCheckEndTime(succeeded)
		}

		fakeDelegateFactory.CheckDelegateReturns(fakeDelegate)

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
			fakeResourceConfigFactory,
			containerMetadata,
			fakePool,
			fakeDelegateFactory,
			defaultTimeout,
			checkStepOpts...,
		)

		stepOk, stepErr = checkStep.Run(ctx, runState)
	})

	Context("with a reasonable configuration", func() {
		It("emits an Initializing event", func() {
			Expect(fakeDelegate.InitializingCallCount()).To(Equal(1))
		})

		Context("when not running", func() {
			BeforeEach(func() {
				fakeDelegate.WaitToRunReturns(nil, false, nil)
			})

			It("doesn't run the step and succeeds", func() {
				Expect(stepErr).ToNot(HaveOccurred())
				Expect(stepOk).To(BeTrue())

				Expect(chosenContainer.RunningProcesses()).To(BeEmpty())
			})

			Context("when there is a latest version", func() {
				BeforeEach(func() {
					fakeVersion := new(dbfakes.FakeResourceConfigVersion)
					fakeVersion.VersionReturns(db.Version{"some": "latest-version"})
					fakeResourceConfigScope.LatestVersionReturns(fakeVersion, true, nil)
				})

				It("stores the latest version as the step result", func() {
					var val atc.Version
					Expect(runState.Result(planID, &val)).To(BeTrue())
					Expect(val).To(Equal(atc.Version{"some": "latest-version"}))
				})
			})

			Context("when there is no version", func() {
				BeforeEach(func() {
					fakeResourceConfigScope.LatestVersionReturns(nil, false, nil)
				})

				It("does not store a version", func() {
					var dst any
					Expect(runState.Result(planID, &dst)).To(BeFalse())
				})
			})
		})

		Context("running", func() {
			var fakeLock *lockfakes.FakeLock

			var invokedResource resource.Resource

			BeforeEach(func() {
				fakeLock = new(lockfakes.FakeLock)
				fakeDelegate.WaitToRunReturns(fakeLock, true, nil)

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
				var fakeVersion *dbfakes.FakeResourceConfigVersion

				BeforeEach(func() {
					checkPlan.FromVersion = nil

					fakeVersion = new(dbfakes.FakeResourceConfigVersion)
					fakeVersion.VersionReturns(db.Version{"latest": "version"})
					fakeResourceConfigScope.LatestVersionStub = func() (db.ResourceConfigVersion, bool, error) {
						Expect(fakeDelegate.WaitToRunCallCount()).To(
							Equal(1),
							"should have gotten latest version after waiting, not before",
						)

						return fakeVersion, true, nil
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
					Expect(fakeDelegate.ContainerOwnerCallCount()).To(Equal(1))
				})

				It("doesn't enforce a timeout", func() {
					_, ok := ctx.Deadline()
					Expect(ok).To(BeFalse())
				})

				It("calls SelectWorker with the correct WorkerSpec", func() {
					Expect(workerSpec.TeamID).To(Equal(345))
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

			Describe("running the check step", func() {
				Context("when using a custom resource type", func() {
					var (
						fakeImageSpec          runtime.ImageSpec
						fakeImageResourceCache *dbfakes.FakeResourceCache
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

						fakeImageSpec = runtime.ImageSpec{
							ImageArtifact: runtimetest.NewVolume("some-volume"),
						}

						fakeImageResourceCache = new(dbfakes.FakeResourceCache)
						fakeImageResourceCache.IDReturns(123)

						fakeDelegate.FetchImageReturns(fakeImageSpec, fakeImageResourceCache, nil)
					})

					It("fetches the resource type image", func() {
						Expect(fakeDelegate.FetchImageCallCount()).To(Equal(1))
						_, actualGetImagePlan, actualCheckImagePlan, privileged := fakeDelegate.FetchImageArgsForCall(0)
						Expect(actualGetImagePlan).To(Equal(*checkPlan.TypeImage.GetPlan))
						Expect(actualCheckImagePlan).To(Equal(checkPlan.TypeImage.CheckPlan))
						Expect(privileged).To(BeFalse())
					})

					It("sets the image spec in the container spec", func() {
						Expect(chosenContainer.Spec.ImageSpec).To(Equal(fakeImageSpec))
					})

					It("creates the resource config using the image resource cache", func() {
						Expect(fakeResourceConfigFactory.FindOrCreateResourceConfigCallCount()).To(Equal(1))
						type_, source, irc := fakeResourceConfigFactory.FindOrCreateResourceConfigArgsForCall(0)
						Expect(type_).To(Equal("some-custom-type"))
						Expect(source).To(Equal(atc.Source{"some": "super-secret-source"}))
						Expect(irc).To(Equal(fakeImageResourceCache))
					})

					Context("when the resource type is privileged", func() {
						BeforeEach(func() {
							checkPlan.TypeImage.Privileged = true
						})

						It("fetches the image with privileged", func() {
							Expect(fakeDelegate.FetchImageCallCount()).To(Equal(1))
							_, _, _, privileged := fakeDelegate.FetchImageArgsForCall(0)
							Expect(privileged).To(BeTrue())
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
						Expect(fakeDelegate.PointToCheckedConfigCallCount()).To(Equal(1))
						scope := fakeDelegate.PointToCheckedConfigArgsForCall(0)
						Expect(scope).To(Equal(fakeResourceConfigScope))
					})

					It("uses build step container owner", func() {
						Expect(chosenContainer.RunningProcesses()).To(HaveLen(1))
					})

					It("update scope's check start time", func() {
						Expect(fakeDelegate.UpdateScopeLastCheckStartTimeCallCount()).To(Equal(1))
						scope, nestedStep := fakeDelegate.UpdateScopeLastCheckStartTimeArgsForCall(0)
						Expect(scope).To(Equal(fakeResourceConfigScope))
						Expect(nestedStep).To(BeFalse())
					})
				})

				Context("when the plan is nested", func() {
					BeforeEach(func() {
						checkPlan.Resource = ""
						checkPlan.ResourceType = "some-resource-type"

						expectedOwner = db.NewBuildStepContainerOwner(
							501,
							atc.PlanID("502"),
							503,
						)
						fakeDelegate.ContainerOwnerReturns(expectedOwner)

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
						fakePool.FindOrSelectWorkerReturns(chosenWorker, nil)
					})

					It("points the resource or resource type to the scope", func() {
						Expect(fakeDelegate.PointToCheckedConfigCallCount()).To(Equal(1))
					})

					It("uses delegate's container owner", func() {
						Expect(fakeDelegate.ContainerOwnerCallCount()).To(Equal(1))
						Expect(chosenContainer.RunningProcesses()).To(HaveLen(1))
					})

					It("update scope's check start time", func() {
						Expect(fakeDelegate.UpdateScopeLastCheckStartTimeCallCount()).To(Equal(1))
						scope, nestedStep := fakeDelegate.UpdateScopeLastCheckStartTimeArgsForCall(0)
						Expect(scope).To(Equal(fakeResourceConfigScope))
						Expect(nestedStep).To(BeTrue())
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
						Expect(fakeDelegate.ErroredCallCount()).To(Equal(1))
						_, status := fakeDelegate.ErroredArgsForCall(0)
						Expect(status).To(Equal(exec.TimeoutLogMessage))
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

						BeforeEach(func() {
							spanRecorder = new(tracetest.SpanRecorder)
							tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder), sdktrace.WithSyncer(tracetest.NewInMemoryExporter()))
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
				})
			})

			Context("with tracing configured", func() {
				var buildSpan trace.Span

				BeforeEach(func() {
					exporter := tracetest.NewInMemoryExporter()
					tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
					tracing.ConfigureTraceProvider(tp)

					spanCtx, buildSpan = tracing.StartSpan(context.Background(), "fake-operation", nil)
					fakeDelegate.StartSpanReturns(spanCtx, buildSpan)
				})

				AfterEach(func() {
					tracing.Configured = false
				})

				It("propagates span context to scope", func() {
					Expect(fakeResourceConfigScope.SaveVersionsCallCount()).To(Equal(1))
					spanContext, _ := fakeResourceConfigScope.SaveVersionsArgsForCall(0)
					traceID := buildSpan.SpanContext().TraceID().String()
					traceParent := spanContext.Get("traceparent")
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
					Expect(fakeResourceConfigFactory.FindOrCreateResourceConfigCallCount()).To(Equal(1))
					type_, source, irc := fakeResourceConfigFactory.FindOrCreateResourceConfigArgsForCall(0)
					Expect(type_).To(Equal("some-base-type"))
					Expect(source).To(Equal(atc.Source{"some": "super-secret-source"}))
					Expect(irc).To(BeNil())

					Expect(fakeDelegate.FindOrCreateScopeCallCount()).To(Equal(1))
					config := fakeDelegate.FindOrCreateScopeArgsForCall(0)
					Expect(config).To(Equal(fakeResourceConfig))

					spanContext, versions := fakeResourceConfigScope.SaveVersionsArgsForCall(0)
					Expect(spanContext).To(Equal(db.SpanContext{}))
					Expect(versions).To(Equal([]atc.Version{
						{"version": "1"},
						{"version": "2"},
					}))
				})

				It("stores the latest version as the step result", func() {
					var val atc.Version
					Expect(runState.Result(planID, &val)).To(BeTrue())
					Expect(val).To(Equal(atc.Version{"version": "2"}))
				})

				It("emits a successful Finished event", func() {
					Expect(fakeDelegate.FinishedCallCount()).To(Equal(1))
					_, succeeded := fakeDelegate.FinishedArgsForCall(0)
					Expect(succeeded).To(BeTrue())
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
					BeforeEach(func() {
						fakeResourceConfigScope.UpdateLastCheckStartTimeStub = func(int, *json.RawMessage) (bool, error) {
							Expect(chosenContainer.RunningProcesses()).To(BeEmpty())
							return true, nil
						}
					})

					It("updates the scope's last check start time", func() {
						Expect(fakeResourceConfigScope.UpdateLastCheckStartTimeCallCount()).To(Equal(1))
						Expect(chosenContainer.RunningProcesses()).To(HaveLen(1))
					})
				})

				Context("after saving", func() {
					BeforeEach(func() {
						fakeResourceConfigScope.SaveVersionsStub = func(db.SpanContext, []atc.Version) error {
							Expect(fakeResourceConfigScope.UpdateLastCheckEndTimeCallCount()).To(Equal(0))
							return nil
						}
					})

					It("updates the scope's last check end time", func() {
						Expect(fakeResourceConfigScope.UpdateLastCheckEndTimeCallCount()).To(Equal(1))
					})

					It("releases the lock", func() {
						Expect(fakeResourceConfigScope.SaveVersionsCallCount()).To(Equal(1))
						Expect(fakeLock.ReleaseCallCount()).To(Equal(1))
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
					Expect(fakeResourceConfigScope.UpdateLastCheckEndTimeCallCount()).To(Equal(1))
				})

				// Finished is for script success/failure, whereas this is an error
				It("does not emit a Finished event", func() {
					Expect(fakeDelegate.FinishedCallCount()).To(Equal(0))
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
					Expect(fakeResourceConfigScope.UpdateLastCheckEndTimeCallCount()).To(Equal(1))
				})

				It("emits a failed Finished event", func() {
					Expect(fakeDelegate.FinishedCallCount()).To(Equal(1))
					_, succeeded := fakeDelegate.FinishedArgsForCall(0)
					Expect(succeeded).To(BeFalse())
				})
			})

			Context("having SaveVersions failing", func() {
				var expectedErr error

				BeforeEach(func() {
					expectedErr = errors.New("save-versions-err")

					fakeResourceConfigScope.SaveVersionsReturns(expectedErr)
				})

				It("errors", func() {
					Expect(stepErr).To(HaveOccurred())
					Expect(errors.Is(stepErr, expectedErr)).To(BeTrue())
				})
			})

			Context("when SaveVersions fails with FK violation (scope deleted by GC)", func() {
				BeforeEach(func() {
					fakeResourceConfigScope.SaveVersionsReturns(
						fmt.Errorf("save versions: %w", &pgconn.PgError{Code: pgerrcode.ForeignKeyViolation}),
					)
				})

				It("does not error", func() {
					Expect(stepErr).NotTo(HaveOccurred())
				})

				It("finishes with failure (non-fatal)", func() {
					Expect(fakeDelegate.FinishedCallCount()).To(Equal(1))
					_, succeeded := fakeDelegate.FinishedArgsForCall(0)
					Expect(succeeded).To(BeFalse())
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
			fakeDelegate.PointToCheckedConfigReturns(
				&pgconn.PgError{Code: pgerrcode.ForeignKeyViolation},
			)
		})

		It("does not error", func() {
			Expect(stepErr).NotTo(HaveOccurred())
		})

		It("finishes with failure (non-fatal)", func() {
			Expect(fakeDelegate.FinishedCallCount()).To(Equal(1))
			_, succeeded := fakeDelegate.FinishedArgsForCall(0)
			Expect(succeeded).To(BeFalse())
		})
	})

	// MO-02, MO-03, MO-04: Check metrics
	Describe("Check Metrics", func() {
		var fakeLock *lockfakes.FakeLock

		BeforeEach(func() {
			fakeLock = new(lockfakes.FakeLock)
			fakeDelegate.WaitToRunReturns(fakeLock, true, nil)

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
				fakeDelegate.WaitToRunReturns(nil, false, nil)
			})

			It("does not increment ChecksStarted", func() {
				Expect(stepOk).To(BeTrue())
				Expect(metric.Metrics.ChecksStarted.Delta()).To(BeNumerically("==", 0))
			})
		})
	})

	Context("native resolution for registry-image", func() {
		var (
			fakeResolver *imageresolvertesting.FakeResolver
			fakeLock     *lockfakes.FakeLock
		)

		BeforeEach(func() {
			fakeResolver = new(imageresolvertesting.FakeResolver)
			checkStepOpts = []exec.CheckStepOption{exec.WithCheckResolver(fakeResolver)}

			fakeLock = new(lockfakes.FakeLock)
			fakeDelegate.WaitToRunReturns(fakeLock, true, nil)

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
				Expect(fakeResourceConfigScope.SaveVersionsCallCount()).To(Equal(1))
				_, versions := fakeResourceConfigScope.SaveVersionsArgsForCall(0)
				Expect(versions).To(Equal([]atc.Version{{"digest": "sha256:abc123def456"}}))
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
				Expect(fakeResourceConfigScope.SaveVersionsCallCount()).To(Equal(0))
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
