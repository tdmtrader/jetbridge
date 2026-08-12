package engine_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/builds"
	"github.com/concourse/concourse/atc/creds/credsfakes"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/db/lock/lockfakes"
	. "github.com/concourse/concourse/atc/engine"
	"github.com/concourse/concourse/atc/engine/enginefakes"
	"github.com/concourse/concourse/atc/event"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/vars"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// A stepper factory hands out a closure and keeps no record of having done so,
// so the number of builds it was asked about is only observable from outside.
type countingStepperFactory struct {
	StepperFactory

	stepperCalls int
}

func (factory *countingStepperFactory) StepperForBuild(build db.Build) (exec.Stepper, error) {
	factory.stepperCalls++
	return factory.StepperFactory.StepperForBuild(build)
}

var _ = Describe("Engine", func() {
	var (
		fakeCoreStepFactory *enginefakes.FakeCoreStepFactory
		stepperFactory      *countingStepperFactory

		fakeGlobalCreds   *credsfakes.FakeSecrets
		fakeVarSourcePool *credsfakes.FakeVarSourcePool
	)

	BeforeEach(func() {
		fakeCoreStepFactory = new(enginefakes.FakeCoreStepFactory)
		stepperFactory = &countingStepperFactory{
			StepperFactory: NewStepperFactory(
				fakeCoreStepFactory,
				"http://example.com",
				newCheckRateLimiter(),
				nil,
				nil,
				nil,
				nil,
				nil,
				nil,
			),
		}

		fakeGlobalCreds = new(credsfakes.FakeSecrets)
		fakeVarSourcePool = new(credsfakes.FakeVarSourcePool)
	})

	Describe("NewBuild", func() {
		var (
			build     builds.Runnable
			engine    Engine
			realBuild db.Build
		)

		BeforeEach(func() {
			fixture := useEngineDB()
			_, _, _, realBuild = createEngineJobBuild(
				fixture,
				"some-team",
				atc.PipelineRef{
					Name:         "some-pipeline",
					InstanceVars: atc.InstanceVars{"branch": "master"},
				},
				atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}},
				"some-user",
			)
			engine = NewEngine(stepperFactory, fakeGlobalCreds, fakeVarSourcePool)
		})

		JustBeforeEach(func() {
			build = engine.NewBuild(realBuild)
		})

		It("returns a build", func() {
			Expect(build).NotTo(BeNil())
		})
	})

	Describe("retained runtime and fault matrix", func() {
		var (
			build     builds.Runnable
			fakeBuild *dbfakes.FakeBuild
			release   chan bool
			waitGroup *sync.WaitGroup
		)

		BeforeEach(func() {
			// This fake controls non-persistable lock, listener, cancellation,
			// panic, retry, and callback ordering in the engine runtime.
			fakeBuild = new(dbfakes.FakeBuild)
			fakeBuild.IDReturns(128)
			fakeBuild.SchemaReturns("exec.v2")

			release = make(chan bool)
			trackedStates := new(sync.Map)
			waitGroup = new(sync.WaitGroup)

			build = NewBuild(
				fakeBuild,
				stepperFactory,
				fakeGlobalCreds,
				fakeVarSourcePool,
				release,
				trackedStates,
				waitGroup,
			)
		})

		Describe("Run", func() {
			var (
				logger lager.Logger
				ctx    context.Context
			)

			BeforeEach(func() {
				logger = lagertest.NewTestLogger("test")
				ctx = context.Background()
			})

			JustBeforeEach(func() {
				build.Run(lagerctx.NewContext(ctx, logger))
			})

			Context("when acquiring the lock succeeds", func() {
				var fakeLock *lockfakes.FakeLock

				BeforeEach(func() {
					fakeLock = new(lockfakes.FakeLock)

					fakeBuild.AcquireTrackingLockReturns(fakeLock, true, nil)
				})

				Context("when the build is active", func() {
					BeforeEach(func() {
						fakeBuild.IsRunningReturns(true)
						fakeBuild.ReloadReturns(true, nil)
					})

					Context("when listening for aborts succeeds", func() {
						var abortSignal *db.NotifySignal
						var unlistenCalled bool

						BeforeEach(func() {
							abortSignal = db.NewNotifySignal()
							unlistenCalled = false

							fakeBuild.AbortSignalReturns(abortSignal, func() { unlistenCalled = true }, nil)
						})

						Context("when converting the plan to a step succeeds", func() {
							var fakeStep *scriptedStep

							BeforeEach(func() {
								fakeStep = new(scriptedStep)
								fakeBuild.PrivatePlanReturns(atc.Plan{
									ID: "build-plan",
									LoadVar: &atc.LoadVarPlan{
										Name: "some-var",
										File: "some-file.yml",
									},
								})

								fakeCoreStepFactory.LoadVarStepReturns(fakeStep)
							})

							It("releases the lock", func() {
								waitGroup.Wait()
								Expect(fakeLock.ReleaseCallCount()).To(Equal(1))
							})

							It("unlistens the abort signal", func() {
								waitGroup.Wait()
								Expect(unlistenCalled).To(BeTrue())
							})

							It("constructs a step from the build's plan", func() {
								waitGroup.Wait()
								Expect(fakeCoreStepFactory.LoadVarStepCallCount()).To(Equal(1))
								plan, _, _ := fakeCoreStepFactory.LoadVarStepArgsForCall(0)
								Expect(plan).ToNot(BeZero())
								Expect(plan).To(Equal(fakeBuild.PrivatePlan())) //XXX
							})

							Context("when getting the build vars succeeds", func() {
								var invokedState chan exec.RunState

								BeforeEach(func() {
									fakeBuild.VariablesReturns(vars.StaticVariables{"foo": "bar"}, nil)

									invokedState = make(chan exec.RunState, 1)
									fakeStep.RunStub = func(ctx context.Context, state exec.RunState) (bool, error) {
										invokedState <- state
										return true, nil
									}
								})

								It("runs the step with the build variables", func() {
									state := <-invokedState

									val, found, err := state.Get(vars.Reference{Path: "foo"})
									Expect(err).ToNot(HaveOccurred())
									Expect(found).To(BeTrue())
									Expect(val).To(Equal("bar"))
								})

								Context("when the build is released", func() {
									BeforeEach(func() {
										readyToRelease := make(chan bool)

										go func() {
											<-readyToRelease
											release <- true
										}()

										fakeStep.RunStub = func(context.Context, exec.RunState) (bool, error) {
											close(readyToRelease)
											<-time.After(time.Hour)
											return true, nil
										}
									})

									Context("when this is a job build", func() {
										It("does not finish the build, leaving it running for the next web to resume", func() {
											waitGroup.Wait()
											Expect(fakeBuild.FinishCallCount()).To(Equal(0))
										})
									})

									Context("when this is an in-memory check build", func() {
										BeforeEach(func() {
											fakeBuild.NameReturns(db.CheckBuildName)
										})

										It("finishes the build as errored so in-flight check tracking is cleared", func() {
											waitGroup.Wait()
											Expect(fakeBuild.FinishCallCount()).To(Equal(1))
											Expect(fakeBuild.FinishArgsForCall(0)).To(Equal(db.BuildStatusErrored))
										})

										It("saves an error event explaining the drain", func() {
											waitGroup.Wait()
											Expect(fakeBuild.SaveEventCallCount()).To(Equal(1))
											ev := fakeBuild.SaveEventArgsForCall(0)
											Expect(ev.EventType()).To(Equal(event.EventTypeError))
											Expect(ev.(event.Error).Message).To(Equal("build released during drain"))
										})
									})
								})

								Context("when the build is aborted", func() {
									BeforeEach(func() {
										fakeBuild.ReloadReturns(true, nil)
										fakeBuild.IsAbortedReturns(true)
										readyToAbort := make(chan bool)

										go func() {
											<-readyToAbort
											abortSignal.Signal()
										}()

										fakeStep.RunStub = func(context.Context, exec.RunState) (bool, error) {
											close(readyToAbort)
											<-time.After(time.Second)
											return true, nil
										}
									})

									It("cancels the context given to the step", func() {
										waitGroup.Wait()
										stepCtx, _ := fakeStep.RunArgsForCall(0)
										Expect(stepCtx.Done()).To(BeClosed())
									})
								})

								Context("when the build finishes successfully", func() {
									BeforeEach(func() {
										fakeStep.RunReturns(true, nil)
									})

									It("finishes the build", func() {
										waitGroup.Wait()
										Expect(fakeBuild.FinishCallCount()).To(Equal(1))
										Expect(fakeBuild.FinishArgsForCall(0)).To(Equal(db.BuildStatusSucceeded))
									})
								})

								Context("when the build finishes woefully", func() {
									BeforeEach(func() {
										fakeStep.RunReturns(false, nil)
									})

									It("finishes the build", func() {
										waitGroup.Wait()
										Expect(fakeBuild.FinishCallCount()).To(Equal(1))
										Expect(fakeBuild.FinishArgsForCall(0)).To(Equal(db.BuildStatusFailed))
									})

									It("does not save an error event", func() {
										waitGroup.Wait()
										Expect(fakeBuild.SaveEventCallCount()).To(Equal(0))
									})
								})

								Context("when the build finishes with error", func() {
									Context("when the error is not retryable", func() {
										BeforeEach(func() {
											fakeStep.RunReturns(false, errors.New("nope"))
										})

										It("finishes the build", func() {
											waitGroup.Wait()
											Expect(fakeBuild.FinishCallCount()).To(Equal(1))
											Expect(fakeBuild.FinishArgsForCall(0)).To(Equal(db.BuildStatusErrored))
										})

										Context("on a normal (job) build", func() {
											// The step's own LogError wrapper surfaces the error
											// event (at the leaf origin); finish() must NOT emit a
											// second one at the build's root origin, which the UI
											// renders as a duplicate error message (review finding,
											// 2026-07-12).
											It("does not double-emit the error event", func() {
												waitGroup.Wait()
												Expect(fakeBuild.SaveEventCallCount()).To(Equal(0))
											})
										})

										Context("on a check build (whose check step is not LogError-wrapped)", func() {
											BeforeEach(func() {
												fakeBuild.NameReturns(db.CheckBuildName)
											})

											It("surfaces the error event so the check reason shows in the UI", func() {
												waitGroup.Wait()
												Expect(fakeBuild.SaveEventCallCount()).To(Equal(1))
												ev := fakeBuild.SaveEventArgsForCall(0)
												Expect(ev.EventType()).To(Equal(event.EventTypeError))
												Expect(ev.(event.Error).Message).To(Equal("nope"))
											})
										})
									})

									Context("when the error is retryable", func() {
										BeforeEach(func() {
											fakeStep.RunReturns(false, exec.Retriable{Cause: errors.New("nope")})
										})

										Context("when this is a check build", func() {
											BeforeEach(func() {
												fakeBuild.NameReturns(db.CheckBuildName)
											})

											It("should not retry, thus finishes the build", func() {
												waitGroup.Wait()
												Expect(fakeBuild.FinishCallCount()).To(Equal(1))
												Expect(fakeBuild.FinishArgsForCall(0)).To(Equal(db.BuildStatusErrored))
											})

											It("saves an error event with the unwrapped cause", func() {
												waitGroup.Wait()
												Expect(fakeBuild.SaveEventCallCount()).To(Equal(1))
												ev := fakeBuild.SaveEventArgsForCall(0)
												Expect(ev.EventType()).To(Equal(event.EventTypeError))
												Expect(ev.(event.Error).Message).To(Equal("nope"))
											})
										})

										Context("when this is a normal build", func() {
											It("should retry, thus not finishe the build", func() {
												waitGroup.Wait()
												Expect(fakeBuild.FinishCallCount()).To(Equal(0))
											})
										})
									})
								})

								Context("when the build finishes with cancelled error", func() {
									BeforeEach(func() {
										fakeStep.RunReturns(false, context.Canceled)
									})

									It("does not save an error event", func() {
										waitGroup.Wait()
										Expect(fakeBuild.SaveEventCallCount()).To(Equal(0))
									})

									It("finishes the build", func() {
										waitGroup.Wait()
										Expect(fakeBuild.FinishCallCount()).To(Equal(1))
										Expect(fakeBuild.FinishArgsForCall(0)).To(Equal(db.BuildStatusAborted))
									})
								})

								Context("when the build finishes with a wrapped cancelled error", func() {
									BeforeEach(func() {
										fakeStep.RunReturns(false, fmt.Errorf("but im not a wrapper: %w", context.Canceled))
									})

									It("finishes the build", func() {
										waitGroup.Wait()
										Expect(fakeBuild.FinishCallCount()).To(Equal(1))
										Expect(fakeBuild.FinishArgsForCall(0)).To(Equal(db.BuildStatusAborted))
									})
								})

								Context("when the build panics", func() {
									BeforeEach(func() {
										fakeStep.RunStub = func(context.Context, exec.RunState) (bool, error) {
											panic("something went wrong")
										}
									})

									It("finishes the build with error", func() {
										waitGroup.Wait()
										Expect(fakeBuild.FinishCallCount()).To(Equal(1))
										Expect(fakeBuild.FinishArgsForCall(0)).To(Equal(db.BuildStatusErrored))
									})

									It("surfaces the panic as an error event (no LogError wrapper ran)", func() {
										waitGroup.Wait()
										Expect(fakeBuild.SaveEventCallCount()).To(Equal(1))
										Expect(fakeBuild.SaveEventArgsForCall(0).EventType()).To(Equal(event.EventTypeError))
									})
								})

								It("build.RunState should be called", func() {
									Expect(fakeBuild.RunStateIDCallCount()).To(Equal(2))
								})
							})

							Context("when getting the build vars fails", func() {
								BeforeEach(func() {
									fakeBuild.VariablesReturns(nil, errors.New("ruh roh"))
								})

								It("releases the lock", func() {
									Expect(fakeLock.ReleaseCallCount()).To(Equal(1))
								})

								It("saves an error event", func() {
									Expect(fakeBuild.SaveEventCallCount()).To(Equal(1))
									Expect(fakeBuild.SaveEventArgsForCall(0).EventType()).To(Equal(event.EventTypeError))
								})
							})
						})

						Context("when converting the plan to a step fails", func() {
							BeforeEach(func() {
								fakeBuild.SchemaReturns("not-schema")
							})

							It("releases the lock", func() {
								Expect(fakeLock.ReleaseCallCount()).To(Equal(1))
							})

							It("saves an error event", func() {
								Expect(fakeBuild.SaveEventCallCount()).To(Equal(1))
								Expect(fakeBuild.SaveEventArgsForCall(0).EventType()).To(Equal(event.EventTypeError))
							})
						})
					})

					Context("when listening for aborts fails", func() {
						BeforeEach(func() {
							fakeBuild.AbortSignalReturns(nil, nil, errors.New("nope"))
						})

						It("releases the lock", func() {
							Expect(fakeLock.ReleaseCallCount()).To(Equal(1))
						})
					})
				})

				Context("when the build is not yet active", func() {
					BeforeEach(func() {
						fakeBuild.ReloadReturns(true, nil)
					})

					It("does not build the step", func() {
						Expect(stepperFactory.stepperCalls).To(BeZero())
					})

					It("releases the lock", func() {
						Expect(fakeLock.ReleaseCallCount()).To(Equal(1))
					})
				})

				Context("when the build has already finished", func() {
					BeforeEach(func() {
						fakeBuild.ReloadReturns(true, nil)
						fakeBuild.StatusReturns(db.BuildStatusSucceeded)
					})

					It("does not build the step", func() {
						Expect(stepperFactory.stepperCalls).To(BeZero())
					})

					It("releases the lock", func() {
						Expect(fakeLock.ReleaseCallCount()).To(Equal(1))
					})
				})

				Context("when the build is no longer in the database", func() {
					BeforeEach(func() {
						fakeBuild.ReloadReturns(false, nil)
					})

					It("does not build the step", func() {
						Expect(stepperFactory.stepperCalls).To(BeZero())
					})

					It("releases the lock", func() {
						Expect(fakeLock.ReleaseCallCount()).To(Equal(1))
					})
				})
			})

			Context("when acquiring the lock fails", func() {
				BeforeEach(func() {
					fakeBuild.AcquireTrackingLockReturns(nil, false, errors.New("no lock for you"))
				})

				It("does not build the step", func() {
					Expect(stepperFactory.stepperCalls).To(BeZero())
				})
			})
		})
	})
})
