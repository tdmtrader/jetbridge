package engine_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"code.cloudfoundry.org/clock"
	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/builds"
	"github.com/concourse/concourse/atc/creds"
	"github.com/concourse/concourse/atc/creds/dummy"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	"github.com/concourse/concourse/atc/db/lock"
	. "github.com/concourse/concourse/atc/engine"
	"github.com/concourse/concourse/atc/engine/enginefakes"
	"github.com/concourse/concourse/atc/event"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/vars"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// A build whose row is intact cannot fail only the tracking lock acquisition.
type trackingLockErrorBuild struct {
	db.Build

	err error
}

func (b trackingLockErrorBuild) AcquireTrackingLock(lager.Logger, time.Duration) (lock.Lock, bool, error) {
	return nil, false, b.err
}

// Unlistening leaves no trace on the build, so record it around the real
// listener; err injects the listen failure a healthy bus cannot produce.
type abortListenerRecordingBuild struct {
	db.Build

	err        error
	unlistened bool
}

func (b *abortListenerRecordingBuild) AbortSignal() (*db.NotifySignal, func(), error) {
	if b.err != nil {
		return nil, nil, b.err
	}

	signal, unlisten, err := b.Build.AbortSignal()
	return signal, func() {
		b.unlistened = true
		unlisten()
	}, err
}

// A build whose pipeline resolves cannot fail only the credential lookup.
type variablesErrorBuild struct {
	db.Build

	err error
}

func (b variablesErrorBuild) Variables(lager.Logger, creds.Secrets, creds.VarSourcePool) (vars.Variables, error) {
	return nil, b.err
}

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

		globalSecrets creds.Secrets
		varSourcePool creds.VarSourcePool
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

		globalSecrets = &dummy.Secrets{StaticVariables: vars.StaticVariables{"foo": "bar"}}
		varSourcePool = creds.NewVarSourcePool(
			lagertest.NewTestLogger("var-source-pool"),
			creds.CredentialManagementConfig{},
			time.Minute,
			time.Minute,
			clock.NewClock(),
		)
		DeferCleanup(varSourcePool.Close)
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
			engine = NewEngine(stepperFactory, globalSecrets, varSourcePool)
		})

		JustBeforeEach(func() {
			build = engine.NewBuild(realBuild)
		})

		It("returns a build", func() {
			Expect(build).NotTo(BeNil())
		})
	})

	Describe("Run", func() {
		var (
			fixture   *engineDBFixture
			resource  db.Resource
			realBuild db.Build
			recorder  *eventRecordingBuild
			dbBuild   db.Build

			plan       atc.Plan
			prepareRow func()

			engineBuild   builds.Runnable
			release       chan bool
			trackedStates *sync.Map
			waitGroup     *sync.WaitGroup

			logger lager.Logger
			ctx    context.Context
		)

		// A released advisory lock can be taken again; a leaked one cannot.
		expectTrackingLockReleased := func() {
			GinkgoHelper()

			acquired, ok, err := fixture.LockFactory.Acquire(
				lagertest.NewTestLogger("build-tracking-lock"),
				lock.NewBuildTrackingLockID(realBuild.ID()),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(ok).To(BeTrue())
			Expect(acquired.Release()).To(Succeed())
		}

		// The check build the resource itself creates is already started and
		// already named "check", which is what the engine branches on.
		useCheckBuild := func() {
			GinkgoHelper()

			var found bool
			var err error
			realBuild, found, err = resource.CreateBuild(context.Background(), false, plan)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(realBuild.Name()).To(Equal(db.CheckBuildName))

			recorder = &eventRecordingBuild{Build: realBuild}
			dbBuild = recorder
			prepareRow = func() {}
		}

		BeforeEach(func() {
			logger = lagertest.NewTestLogger("test")
			ctx = context.Background()

			fixture = useEngineDB()
			var pipeline db.Pipeline
			_, pipeline, _, realBuild = createEngineJobBuild(
				fixture,
				"some-team",
				atc.PipelineRef{Name: "some-pipeline"},
				atc.Config{
					Jobs: atc.JobConfigs{{Name: "some-job"}},
					Resources: atc.ResourceConfigs{{
						Name: "some-resource", Type: dbtest.BaseResourceType,
						Source: atc.Source{"some": "source"},
					}},
				},
				"some-user",
			)

			var found bool
			var err error
			resource, found, err = pipeline.Resource("some-resource")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())

			recorder = &eventRecordingBuild{Build: realBuild}
			dbBuild = recorder

			plan = atc.Plan{
				ID: "build-plan",
				LoadVar: &atc.LoadVarPlan{
					Name: "some-var",
					File: "some-file.yml",
				},
			}
			prepareRow = func() {
				started, err := realBuild.Start(plan)
				Expect(err).NotTo(HaveOccurred())
				Expect(started).To(BeTrue())
				Expect(realBuild.Reload()).To(BeTrue())
			}

			release = make(chan bool)
			trackedStates = new(sync.Map)
			waitGroup = new(sync.WaitGroup)
		})

		JustBeforeEach(func() {
			prepareRow()

			engineBuild = NewBuild(
				dbBuild,
				stepperFactory,
				globalSecrets,
				varSourcePool,
				release,
				trackedStates,
				waitGroup,
			)

			engineBuild.Run(lagerctx.NewContext(ctx, logger))
		})

		Context("when acquiring the lock succeeds", func() {
			Context("when the build is running", func() {
				Context("when listening for aborts succeeds", func() {
					var aborts *abortListenerRecordingBuild

					BeforeEach(func() {
						aborts = &abortListenerRecordingBuild{Build: recorder}
						dbBuild = aborts
					})

					Context("when converting the plan to a step succeeds", func() {
						var fakeStep *scriptedStep

						BeforeEach(func() {
							fakeStep = new(scriptedStep)
							fakeCoreStepFactory.LoadVarStepReturns(fakeStep)
						})

						It("releases the lock", func() {
							waitGroup.Wait()
							expectTrackingLockReleased()
						})

						It("unlistens the abort signal", func() {
							waitGroup.Wait()
							Expect(aborts.unlistened).To(BeTrue())
						})

						It("constructs a step from the build's plan", func() {
							waitGroup.Wait()
							Expect(fakeCoreStepFactory.LoadVarStepCallCount()).To(Equal(1))
							stepPlan, _, _ := fakeCoreStepFactory.LoadVarStepArgsForCall(0)
							Expect(stepPlan).To(Equal(plan))
							Expect(stepPlan).To(Equal(realBuild.PrivatePlan()))
						})

						Context("when getting the build vars succeeds", func() {
							var invokedState chan exec.RunState

							BeforeEach(func() {
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
										Expect(realBuild.Reload()).To(BeTrue())
										Expect(realBuild.Status()).To(Equal(db.BuildStatusStarted))
									})
								})

								Context("when this is a check build", func() {
									BeforeEach(useCheckBuild)

									It("finishes the build as errored so in-flight check tracking is cleared", func() {
										waitGroup.Wait()
										Expect(realBuild.Reload()).To(BeTrue())
										Expect(realBuild.Status()).To(Equal(db.BuildStatusErrored))
									})

									It("saves an error event explaining the drain", func() {
										waitGroup.Wait()
										Expect(recorder.events).To(HaveLen(1))
										ev := recorder.events[0]
										Expect(ev.EventType()).To(Equal(event.EventTypeError))
										Expect(ev.(event.Error).Message).To(Equal("build released during drain"))
									})
								})
							})

							Context("when the build is aborted", func() {
								var aborted chan error

								BeforeEach(func() {
									readyToAbort := make(chan bool)
									aborted = make(chan error, 1)

									go func() {
										<-readyToAbort
										abortHandle, _, err := fixture.BuildFactory.Build(realBuild.ID())
										if err != nil {
											aborted <- err
											return
										}
										aborted <- abortHandle.MarkAsAborted()
									}()

									fakeStep.RunStub = func(ctx context.Context, _ exec.RunState) (bool, error) {
										close(readyToAbort)
										select {
										case <-ctx.Done():
										case <-time.After(10 * time.Second):
										}
										return true, nil
									}
								})

								It("cancels the context given to the step", func() {
									waitGroup.Wait()
									Expect(<-aborted).NotTo(HaveOccurred())
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
									Expect(realBuild.Reload()).To(BeTrue())
									Expect(realBuild.Status()).To(Equal(db.BuildStatusSucceeded))
								})
							})

							Context("when the build finishes woefully", func() {
								BeforeEach(func() {
									fakeStep.RunReturns(false, nil)
								})

								It("finishes the build", func() {
									waitGroup.Wait()
									Expect(realBuild.Reload()).To(BeTrue())
									Expect(realBuild.Status()).To(Equal(db.BuildStatusFailed))
								})

								It("does not save an error event", func() {
									waitGroup.Wait()
									Expect(recorder.events).To(BeEmpty())
								})
							})

							Context("when the build finishes with error", func() {
								Context("when the error is not retryable", func() {
									BeforeEach(func() {
										fakeStep.RunReturns(false, errors.New("nope"))
									})

									It("finishes the build", func() {
										waitGroup.Wait()
										Expect(realBuild.Reload()).To(BeTrue())
										Expect(realBuild.Status()).To(Equal(db.BuildStatusErrored))
									})

									Context("on a normal (job) build", func() {
										// The step's own LogError wrapper surfaces the error
										// event (at the leaf origin); finish() must NOT emit a
										// second one at the build's root origin, which the UI
										// renders as a duplicate error message (review finding,
										// 2026-07-12).
										It("does not double-emit the error event", func() {
											waitGroup.Wait()
											Expect(recorder.events).To(BeEmpty())
										})
									})

									Context("on a check build (whose check step is not LogError-wrapped)", func() {
										BeforeEach(useCheckBuild)

										It("surfaces the error event so the check reason shows in the UI", func() {
											waitGroup.Wait()
											Expect(recorder.events).To(HaveLen(1))
											ev := recorder.events[0]
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
										BeforeEach(useCheckBuild)

										It("should not retry, thus finishes the build", func() {
											waitGroup.Wait()
											Expect(realBuild.Reload()).To(BeTrue())
											Expect(realBuild.Status()).To(Equal(db.BuildStatusErrored))
										})

										It("saves an error event with the unwrapped cause", func() {
											waitGroup.Wait()
											Expect(recorder.events).To(HaveLen(1))
											ev := recorder.events[0]
											Expect(ev.EventType()).To(Equal(event.EventTypeError))
											Expect(ev.(event.Error).Message).To(Equal("nope"))
										})
									})

									Context("when this is a normal build", func() {
										It("should retry, thus not finishe the build", func() {
											waitGroup.Wait()
											Expect(realBuild.Reload()).To(BeTrue())
											Expect(realBuild.Status()).To(Equal(db.BuildStatusStarted))
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
									Expect(recorder.events).To(BeEmpty())
								})

								It("finishes the build", func() {
									waitGroup.Wait()
									Expect(realBuild.Reload()).To(BeTrue())
									Expect(realBuild.Status()).To(Equal(db.BuildStatusAborted))
								})
							})

							Context("when the build finishes with a wrapped cancelled error", func() {
								BeforeEach(func() {
									fakeStep.RunReturns(false, fmt.Errorf("but im not a wrapper: %w", context.Canceled))
								})

								It("finishes the build", func() {
									waitGroup.Wait()
									Expect(realBuild.Reload()).To(BeTrue())
									Expect(realBuild.Status()).To(Equal(db.BuildStatusAborted))
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
									Expect(realBuild.Reload()).To(BeTrue())
									Expect(realBuild.Status()).To(Equal(db.BuildStatusErrored))
								})

								It("surfaces the panic as an error event (no LogError wrapper ran)", func() {
									waitGroup.Wait()
									Expect(recorder.events).To(HaveLen(1))
									Expect(recorder.events[0].EventType()).To(Equal(event.EventTypeError))
								})
							})

							It("clears the run state it tracked for the build", func() {
								waitGroup.Wait()

								tracked := 0
								trackedStates.Range(func(any, any) bool {
									tracked++
									return true
								})
								Expect(tracked).To(BeZero())
							})
						})

						Context("when getting the build vars fails", func() {
							BeforeEach(func() {
								dbBuild = variablesErrorBuild{Build: recorder, err: errors.New("ruh roh")}
							})

							It("releases the lock", func() {
								expectTrackingLockReleased()
							})

							It("saves an error event", func() {
								Expect(recorder.events).To(HaveLen(1))
								Expect(recorder.events[0].EventType()).To(Equal(event.EventTypeError))
							})
						})
					})

					Context("when converting the plan to a step fails", func() {
						BeforeEach(func() {
							// A build only gets a supported schema when it starts,
							// so a build that never started is the real form of the
							// unsupported-schema state.
							prepareRow = func() {}
						})

						It("releases the lock", func() {
							expectTrackingLockReleased()
						})

						It("saves an error event", func() {
							Expect(recorder.events).To(HaveLen(1))
							Expect(recorder.events[0].EventType()).To(Equal(event.EventTypeError))
						})
					})
				})

				Context("when listening for aborts fails", func() {
					BeforeEach(func() {
						dbBuild = &abortListenerRecordingBuild{Build: recorder, err: errors.New("nope")}
					})

					It("releases the lock", func() {
						expectTrackingLockReleased()
					})
				})
			})

			Context("when the build has already finished", func() {
				BeforeEach(func() {
					prepareRow = func() {
						started, err := realBuild.Start(plan)
						Expect(err).NotTo(HaveOccurred())
						Expect(started).To(BeTrue())
						Expect(realBuild.Finish(db.BuildStatusSucceeded)).To(Succeed())
						Expect(realBuild.Reload()).To(BeTrue())
					}
				})

				It("does not build the step", func() {
					Expect(stepperFactory.stepperCalls).To(BeZero())
				})

				It("releases the lock", func() {
					expectTrackingLockReleased()
				})
			})

			Context("when the build is no longer in the database", func() {
				BeforeEach(func() {
					prepareRow = func() {
						deleted, err := realBuild.Delete()
						Expect(err).NotTo(HaveOccurred())
						Expect(deleted).To(BeTrue())
					}
				})

				It("does not build the step", func() {
					Expect(stepperFactory.stepperCalls).To(BeZero())
				})

				It("releases the lock", func() {
					expectTrackingLockReleased()
				})
			})
		})

		Context("when acquiring the lock fails", func() {
			BeforeEach(func() {
				dbBuild = trackingLockErrorBuild{Build: recorder, err: errors.New("no lock for you")}
			})

			It("does not build the step", func() {
				Expect(stepperFactory.stepperCalls).To(BeZero())
			})
		})
	})
})
