package exec_test

import (
	"context"
	"errors"

	. "github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/vars"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Retry Step", func() {
	var (
		ctx    context.Context
		cancel func()

		attempt1 stepFunc
		attempt2 stepFunc
		attempt3 stepFunc

		state         RunState
		attemptEvents chan string
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())

		attemptEvents = make(chan string, 6)
		attempt1 = stepFunc(func(context.Context, RunState) (bool, error) {
			attemptEvents <- "attempt-1-started"
			return false, nil
		})
		attempt2 = stepFunc(func(context.Context, RunState) (bool, error) {
			attemptEvents <- "attempt-2-started"
			return false, nil
		})
		attempt3 = stepFunc(func(context.Context, RunState) (bool, error) {
			attemptEvents <- "attempt-3-started"
			return false, nil
		})

		state = NewRunState(noopStepper, vars.StaticVariables{})
	})

	Describe("Run", func() {
		var stepOk bool
		var stepErr error

		JustBeforeEach(func() {
			stepOk, stepErr = Retry(attempt1, attempt2, attempt3).Run(ctx, state)
		})

		Context("when attempt 1 succeeds", func() {
			BeforeEach(func() {
				attempt1 = stepFunc(func(context.Context, RunState) (bool, error) {
					attemptEvents <- "attempt-1-started"
					return true, nil
				})
			})

			It("returns nil having only run the first attempt", func() {
				Expect(stepErr).ToNot(HaveOccurred())

				Expect(attemptEvents).To(Receive(Equal("attempt-1-started")))
				Consistently(attemptEvents).ShouldNot(Receive())
			})

			It("succeeds", func() {
				Expect(stepOk).To(BeTrue())
			})
		})

		Context("when attempt 1 fails, and attempt 2 succeeds", func() {
			BeforeEach(func() {
				attempt2 = stepFunc(func(context.Context, RunState) (bool, error) {
					attemptEvents <- "attempt-2-started"
					return true, nil
				})
			})

			It("returns nil having only run the first and second attempts", func() {
				Expect(stepErr).ToNot(HaveOccurred())

				Expect(attemptEvents).To(Receive(Equal("attempt-1-started")))
				Expect(attemptEvents).To(Receive(Equal("attempt-2-started")))
				Consistently(attemptEvents).ShouldNot(Receive())
			})

			It("succeeds", func() {
				Expect(stepOk).To(BeTrue())
			})
		})

		Context("when attempt 1 errors, and attempt 2 succeeds", func() {
			BeforeEach(func() {
				attempt1 = stepFunc(func(context.Context, RunState) (bool, error) {
					attemptEvents <- "attempt-1-started"
					return false, errors.New("nope")
				})
				attempt2 = stepFunc(func(context.Context, RunState) (bool, error) {
					attemptEvents <- "attempt-2-started"
					return true, nil
				})
			})

			It("returns nil having only run the first and second attempts", func() {
				Expect(stepErr).ToNot(HaveOccurred())

				Expect(attemptEvents).To(Receive(Equal("attempt-1-started")))
				Expect(attemptEvents).To(Receive(Equal("attempt-2-started")))
				Consistently(attemptEvents).ShouldNot(Receive())
			})

			It("succeeds", func() {
				Expect(stepOk).To(BeTrue())
			})
		})

		Context("when attempt 1 errors, and attempt 2 is interrupted", func() {
			BeforeEach(func() {
				attempt1 = stepFunc(func(context.Context, RunState) (bool, error) {
					attemptEvents <- "attempt-1-started"
					return false, errors.New("nope")
				})
				attempt2 = stepFunc(func(c context.Context, r RunState) (bool, error) {
					attemptEvents <- "attempt-2-started"
					cancel()
					return false, c.Err()
				})
			})

			It("returns the context error having only run the first and second attempts", func() {
				Expect(stepErr).To(Equal(context.Canceled))

				Expect(attemptEvents).To(Receive(Equal("attempt-1-started")))
				Expect(attemptEvents).To(Receive(Equal("attempt-2-started")))
				Consistently(attemptEvents).ShouldNot(Receive())
			})

			It("fails", func() {
				Expect(stepOk).To(BeFalse())
			})
		})

		Context("when attempt 1 errors, attempt 2 times out, and attempt 3 succeeds", func() {
			BeforeEach(func() {
				attempt1 = stepFunc(func(context.Context, RunState) (bool, error) {
					attemptEvents <- "attempt-1-started"
					return false, errors.New("nope")
				})
				attempt2 = stepFunc(func(c context.Context, r RunState) (bool, error) {
					attemptEvents <- "attempt-2-started"
					timeout, subCancel := context.WithTimeout(c, 0)
					defer subCancel()
					<-timeout.Done()
					return false, timeout.Err()
				})
				attempt3 = stepFunc(func(context.Context, RunState) (bool, error) {
					attemptEvents <- "attempt-3-started"
					return true, nil
				})
			})

			It("returns nil after running all 3 steps", func() {
				Expect(stepErr).ToNot(HaveOccurred())

				Expect(attemptEvents).To(Receive(Equal("attempt-1-started")))
				Expect(attemptEvents).To(Receive(Equal("attempt-2-started")))
				Expect(attemptEvents).To(Receive(Equal("attempt-3-started")))
				Consistently(attemptEvents).ShouldNot(Receive())
			})

			It("succeeds", func() {
				Expect(stepOk).To(BeTrue())
			})
		})

		Context("when attempt 1 fails, attempt 2 fails, and attempt 3 succeeds", func() {
			BeforeEach(func() {
				attempt3 = stepFunc(func(context.Context, RunState) (bool, error) {
					attemptEvents <- "attempt-3-started"
					return true, nil
				})
			})

			It("returns nil after running all 3 steps", func() {
				Expect(stepErr).ToNot(HaveOccurred())

				Expect(attemptEvents).To(Receive(Equal("attempt-1-started")))
				Expect(attemptEvents).To(Receive(Equal("attempt-2-started")))
				Expect(attemptEvents).To(Receive(Equal("attempt-3-started")))
				Consistently(attemptEvents).ShouldNot(Receive())
			})

			It("succeeds", func() {
				Expect(stepOk).To(BeTrue())
			})
		})

		Context("when attempt 1 fails, attempt 2 fails, and attempt 3 errors", func() {
			disaster := errors.New("nope")

			BeforeEach(func() {
				attempt3 = stepFunc(func(context.Context, RunState) (bool, error) {
					attemptEvents <- "attempt-3-started"
					return false, disaster
				})
			})

			It("returns the error", func() {
				Expect(stepErr).To(Equal(disaster))

				Expect(attemptEvents).To(Receive(Equal("attempt-1-started")))
				Expect(attemptEvents).To(Receive(Equal("attempt-2-started")))
				Expect(attemptEvents).To(Receive(Equal("attempt-3-started")))
				Consistently(attemptEvents).ShouldNot(Receive())
			})

			It("fails", func() {
				Expect(stepOk).To(BeFalse())
			})
		})

		Context("when attempt 1 fails, attempt 2 fails, and attempt 3 fails", func() {
			It("returns nil after running all three attempts", func() {
				Expect(stepErr).ToNot(HaveOccurred())

				Expect(attemptEvents).To(Receive(Equal("attempt-1-started")))
				Expect(attemptEvents).To(Receive(Equal("attempt-2-started")))
				Expect(attemptEvents).To(Receive(Equal("attempt-3-started")))
				Consistently(attemptEvents).ShouldNot(Receive())
			})

			It("fails", func() {
				Expect(stepOk).To(BeFalse())
			})
		})
	})
})
