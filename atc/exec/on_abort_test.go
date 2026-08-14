package exec_test

import (
	"context"
	"errors"

	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/vars"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("On Abort Step", func() {
	var (
		ctx    context.Context
		cancel func()

		step stepFunc
		hook stepFunc

		state  exec.RunState
		events chan string

		stepOk  bool
		stepErr error
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())

		events = make(chan string, 2)
		step = stepFunc(func(context.Context, exec.RunState) (bool, error) {
			events <- "main-started"
			return false, nil
		})
		hook = stepFunc(func(context.Context, exec.RunState) (bool, error) {
			events <- "abort-hook-started"
			return true, nil
		})

		state = exec.NewRunState(noopStepper, vars.StaticVariables{})

		stepOk = false
		stepErr = nil
	})

	AfterEach(func() {
		cancel()
	})

	JustBeforeEach(func() {
		stepOk, stepErr = exec.OnAbort(step, hook).Run(ctx, state)
	})

	Context("when the step is aborted", func() {
		BeforeEach(func() {
			step = stepFunc(func(context.Context, exec.RunState) (bool, error) {
				events <- "main-started"
				return false, context.Canceled
			})
		})

		It("runs the abort hook", func() {
			Expect(stepErr).To(Equal(context.Canceled))
			Expect(events).To(Receive(Equal("main-started")))
			Expect(events).To(Receive(Equal("abort-hook-started")))
		})
	})

	Context("when the step succeeds", func() {
		BeforeEach(func() {
			step = stepFunc(func(context.Context, exec.RunState) (bool, error) {
				events <- "main-started"
				return true, nil
			})
		})

		It("is successful", func() {
			Expect(stepOk).To(BeTrue())
		})

		It("does not run the abort hook", func() {
			Expect(events).To(Receive(Equal("main-started")))
			Consistently(events).ShouldNot(Receive())
		})
	})

	Context("when the step fails", func() {
		BeforeEach(func() {
			step = stepFunc(func(context.Context, exec.RunState) (bool, error) {
				events <- "main-started"
				return false, nil
			})
		})

		It("is not successful", func() {
			Expect(stepOk).ToNot(BeTrue())
		})

		It("does not run the abort hook", func() {
			Expect(events).To(Receive(Equal("main-started")))
			Consistently(events).ShouldNot(Receive())
		})
	})

	Context("when the step errors", func() {
		disaster := errors.New("disaster")

		BeforeEach(func() {
			step = stepFunc(func(context.Context, exec.RunState) (bool, error) {
				events <- "main-started"
				return false, disaster
			})
		})

		It("returns the error", func() {
			Expect(stepErr).To(Equal(disaster))
		})

		It("does not run the abort hook", func() {
			Expect(events).To(Receive(Equal("main-started")))
			Consistently(events).ShouldNot(Receive())
		})
	})
})
