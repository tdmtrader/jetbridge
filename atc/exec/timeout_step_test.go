package exec_test

import (
	"context"
	"errors"
	"time"

	. "github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/vars"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Timeout Step", func() {
	var (
		ctx    context.Context
		cancel func()

		runStep stepFunc

		state        RunState
		events       chan string
		stepContexts chan context.Context

		timeoutDuration string

		stepOk  bool
		stepErr error
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())

		events = make(chan string, 1)
		stepContexts = make(chan context.Context, 1)
		runStep = stepFunc(func(ctx context.Context, state RunState) (bool, error) {
			events <- "step-started"
			stepContexts <- ctx
			return false, nil
		})

		state = NewRunState(noopStepper, vars.StaticVariables{})

		timeoutDuration = "1h"
	})

	JustBeforeEach(func() {
		stepOk, stepErr = Timeout(runStep, timeoutDuration).Run(ctx, state)
	})

	Context("when the duration is valid", func() {
		It("runs the step with a deadline", func() {
			runCtx := <-stepContexts
			deadline, ok := runCtx.Deadline()
			Expect(ok).To(BeTrue())
			Expect(deadline).To(BeTemporally("~", time.Now().Add(time.Hour), 10*time.Second))
		})

		Context("when the step returns an error", func() {
			var someError error

			BeforeEach(func() {
				someError = errors.New("some error")
				runStep = stepFunc(func(ctx context.Context, state RunState) (bool, error) {
					events <- "step-started"
					stepContexts <- ctx
					return false, someError
				})
			})

			It("returns the error", func() {
				Expect(stepErr).NotTo(BeNil())
				Expect(stepErr).To(Equal(someError))
			})
		})

		Context("when the step exceeds the timeout", func() {
			BeforeEach(func() {
				runStep = stepFunc(func(ctx context.Context, state RunState) (bool, error) {
					events <- "step-started"
					stepContexts <- ctx
					return true, context.DeadlineExceeded
				})
			})

			It("returns no error", func() {
				Expect(stepErr).ToNot(HaveOccurred())
			})

			It("is not successful", func() {
				Expect(stepOk).To(BeFalse())
			})
		})

		Describe("canceling", func() {
			BeforeEach(func() {
				cancel()
			})

			It("forwards the context down", func() {
				runCtx := <-stepContexts
				Expect(runCtx.Err()).To(Equal(context.Canceled))
			})

			It("is not successful", func() {
				Expect(stepOk).To(BeFalse())
			})
		})

		Context("when the step is successful", func() {
			BeforeEach(func() {
				runStep = stepFunc(func(ctx context.Context, state RunState) (bool, error) {
					events <- "step-started"
					stepContexts <- ctx
					return true, nil
				})
			})

			It("is successful", func() {
				Expect(stepOk).To(BeTrue())
			})
		})

		Context("when the step fails", func() {
			BeforeEach(func() {
				runStep = stepFunc(func(ctx context.Context, state RunState) (bool, error) {
					events <- "step-started"
					stepContexts <- ctx
					return false, nil
				})
			})

			It("is not successful", func() {
				Expect(stepOk).To(BeFalse())
			})
		})
	})

	Context("when the duration is invalid", func() {
		BeforeEach(func() {
			timeoutDuration = "nope"
		})

		It("errors immediately without running the step", func() {
			Expect(stepErr).To(HaveOccurred())
			Consistently(events).ShouldNot(Receive())
		})
	})
})
