package exec_test

import (
	"context"
	"errors"
	"fmt"
	"sync"

	. "github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/exec/build"
	"github.com/concourse/concourse/vars"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Parallel", func() {
	var (
		ctx    context.Context
		cancel func()

		stepA stepFunc
		stepB stepFunc

		repo          *build.Repository
		state         RunState
		events        chan string
		stepAContexts chan context.Context
		stepBContexts chan context.Context
		stepAStates   chan RunState
		stepBStates   chan RunState

		parallelLimit int
		failFast      bool
		noSteps       bool
		stepOk        bool
		stepErr       error
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())

		state = NewRunState(noopStepper, vars.StaticVariables{})
		repo = state.ArtifactRepository()
		events = make(chan string, 4)
		stepAContexts = make(chan context.Context, 1)
		stepBContexts = make(chan context.Context, 1)
		stepAStates = make(chan RunState, 1)
		stepBStates = make(chan RunState, 1)
		stepA = stepFunc(func(ctx context.Context, state RunState) (bool, error) {
			events <- "step-a-started"
			stepAContexts <- ctx
			stepAStates <- state
			return false, nil
		})
		stepB = stepFunc(func(ctx context.Context, state RunState) (bool, error) {
			events <- "step-b-started"
			stepBContexts <- ctx
			stepBStates <- state
			return false, nil
		})
		parallelLimit = 2
		failFast = false
		noSteps = false
	})

	AfterEach(func() {
		cancel()
	})

	JustBeforeEach(func() {
		if noSteps {
			stepOk, stepErr = (InParallelStep{}).Run(ctx, state)
			return
		}
		stepOk, stepErr = InParallel([]Step{stepA, stepB}, parallelLimit, failFast).Run(ctx, state)
	})

	It("succeeds", func() {
		Expect(stepErr).ToNot(HaveOccurred())
	})

	It("passes the artifact repo to all steps", func() {
		Expect(stepAStates).To(Receive(WithTransform(func(state RunState) *build.Repository {
			return state.ArtifactRepository()
		}, BeIdenticalTo(repo))))
		Expect(stepBStates).To(Receive(WithTransform(func(state RunState) *build.Repository {
			return state.ArtifactRepository()
		}, BeIdenticalTo(repo))))
	})

	Describe("executing each step", func() {
		Context("when not constrained by parallel limit", func() {
			BeforeEach(func() {
				wg := new(sync.WaitGroup)
				wg.Add(2)

				stepA = stepFunc(func(context.Context, RunState) (bool, error) {
					events <- "step-a-started"
					wg.Done()
					wg.Wait()
					return true, nil
				})

				stepB = stepFunc(func(context.Context, RunState) (bool, error) {
					events <- "step-b-started"
					wg.Done()
					wg.Wait()
					return true, nil
				})
			})

			It("happens concurrently", func() {
				Expect([]string{<-events, <-events}).To(ConsistOf("step-a-started", "step-b-started"))
			})
		})

		Context("when parallel limit is 1", func() {
			BeforeEach(func() {
				parallelLimit = 1
				stepACompleted := make(chan struct{})

				stepA = stepFunc(func(context.Context, RunState) (bool, error) {
					events <- "step-a-started"
					close(stepACompleted)
					return true, nil
				})

				stepB = stepFunc(func(context.Context, RunState) (bool, error) {
					defer GinkgoRecover()

					select {
					case <-stepACompleted:
					default:
						Fail("step B started before step A could complete")
					}
					events <- "step-b-started"
					return true, nil
				})
			})

			It("happens sequentially", func() {
				Expect(events).To(Receive(Equal("step-a-started")))
				Expect(events).To(Receive(Equal("step-b-started")))
			})
		})
	})

	Describe("canceling", func() {
		BeforeEach(func() {
			wg := new(sync.WaitGroup)
			wg.Add(2)

			stepA = stepFunc(func(ctx context.Context, state RunState) (bool, error) {
				events <- "step-a-started"
				stepAContexts <- ctx
				wg.Done()
				return true, nil
			})

			stepB = stepFunc(func(ctx context.Context, state RunState) (bool, error) {
				events <- "step-b-started"
				stepBContexts <- ctx
				wg.Done()
				wg.Wait()
				cancel()
				return true, nil
			})
		})

		It("cancels each substep", func() {
			Expect((<-stepAContexts).Err()).To(Equal(context.Canceled))
			Expect((<-stepBContexts).Err()).To(Equal(context.Canceled))
		})

		It("returns ctx.Err()", func() {
			Expect(stepErr).To(Equal(context.Canceled))
		})

		Context("when there are steps pending execution", func() {
			BeforeEach(func() {
				parallelLimit = 1

				stepA = stepFunc(func(ctx context.Context, state RunState) (bool, error) {
					events <- "step-a-started"
					stepAContexts <- ctx
					cancel()
					return true, nil
				})

				stepB = stepFunc(func(context.Context, RunState) (bool, error) {
					events <- "step-b-started"
					return true, nil
				})
			})

			It("returns ctx.Err()", func() {
				Expect(stepErr).To(Equal(context.Canceled))
			})

			It("does not execute the remaining steps", func() {
				Expect((<-stepAContexts).Err()).To(Equal(context.Canceled))
				Expect(events).To(Receive(Equal("step-a-started")))
				Consistently(events).ShouldNot(Receive())
			})

		})
	})

	Context("when steps fail", func() {
		Context("with normal error", func() {
			disasterA := errors.New("nope A")
			disasterB := errors.New("nope B")

			BeforeEach(func() {
				stepA = stepFunc(func(context.Context, RunState) (bool, error) {
					events <- "step-a-started"
					return false, disasterA
				})
				stepB = stepFunc(func(context.Context, RunState) (bool, error) {
					events <- "step-b-started"
					return false, disasterB
				})
			})

			Context("and fail fast is false", func() {
				BeforeEach(func() {
					parallelLimit = 1
				})
				It("lets all steps finish before exiting", func() {
					Expect(events).To(Receive(Equal("step-a-started")))
					Expect(events).To(Receive(Equal("step-b-started")))
				})
				It("exits with an error including the original message", func() {
					Expect(stepErr.Error()).To(ContainSubstring("nope A"))
					Expect(stepErr.Error()).To(ContainSubstring("nope B"))
				})
			})

			Context("and fail fast is true", func() {
				BeforeEach(func() {
					parallelLimit = 1
					failFast = true
				})
				It("it cancels remaining steps", func() {
					Expect(events).To(Receive(Equal("step-a-started")))
					Consistently(events).ShouldNot(Receive())
				})
				It("exits with an error including the message from the failed steps", func() {
					Expect(stepErr.Error()).To(ContainSubstring("nope A"))
					Expect(stepErr.Error()).NotTo(ContainSubstring("nope B"))
				})
			})
		})

		Context("with context canceled error", func() {
			// error might be wrapped. For example we pass context from in_parallel step
			// -> task step -> ... -> HTTP request (e.g. K8s API). When context
			// got canceled in in_parallel step, the http client sending the request will
			// wrap the context.Canceled error into Url.Error
			disasterB := fmt.Errorf("some thing failed by %w", context.Canceled)

			BeforeEach(func() {
				stepB = stepFunc(func(context.Context, RunState) (bool, error) {
					events <- "step-b-started"
					return false, disasterB
				})
			})

			It("exits with no error", func() {
				Expect(stepErr).ToNot(HaveOccurred())
			})
		})
	})

	Context("when all steps are successful", func() {
		BeforeEach(func() {
			stepA = stepFunc(func(context.Context, RunState) (bool, error) {
				events <- "step-a-started"
				return true, nil
			})
			stepB = stepFunc(func(context.Context, RunState) (bool, error) {
				events <- "step-b-started"
				return true, nil
			})
		})

		It("succeeds", func() {
			Expect(stepOk).To(BeTrue())
		})
	})

	Context("and some steps are not successful", func() {
		BeforeEach(func() {
			stepA = stepFunc(func(context.Context, RunState) (bool, error) {
				events <- "step-a-started"
				return true, nil
			})
			stepB = stepFunc(func(context.Context, RunState) (bool, error) {
				events <- "step-b-started"
				return false, nil
			})
		})

		It("fails", func() {
			Expect(stepOk).To(BeFalse())
		})
	})

	Context("when no steps indicate success", func() {
		BeforeEach(func() {
			stepA = stepFunc(func(context.Context, RunState) (bool, error) {
				events <- "step-a-started"
				return false, nil
			})
			stepB = stepFunc(func(context.Context, RunState) (bool, error) {
				events <- "step-b-started"
				return false, nil
			})
		})

		It("fails", func() {
			Expect(stepOk).To(BeFalse())
		})
	})

	Context("when there are no steps", func() {
		BeforeEach(func() {
			noSteps = true
		})

		It("succeeds", func() {
			Expect(stepOk).To(BeTrue())
		})
	})

	Describe("Panic", func() {
		Context("when one step panics", func() {
			BeforeEach(func() {
				stepA = stepFunc(func(context.Context, RunState) (bool, error) {
					events <- "step-a-started"
					return false, nil
				})
				stepB = stepFunc(func(context.Context, RunState) (bool, error) {
					events <- "step-b-started"
					panic("something went wrong")
				})
			})

			It("returns an error", func() {
				Expect(stepErr.Error()).To(ContainSubstring("something went wrong"))
			})
		})
	})
})
