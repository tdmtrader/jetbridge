package exec_test

import (
	"context"
	"errors"

	. "github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/vars"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Try Step", func() {
	var (
		ctx    context.Context
		cancel func()

		runStep stepFunc

		state RunState

		stepOk  bool
		stepErr error
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())

		runStep = stepFunc(func(context.Context, RunState) (bool, error) {
			return false, nil
		})

		state = NewRunState(noopStepper, vars.StaticVariables{})
	})

	JustBeforeEach(func() {
		stepOk, stepErr = Try(runStep).Run(ctx, state)
	})

	AfterEach(func() {
		cancel()
	})

	Context("when the inner step fails", func() {
		BeforeEach(func() {
			runStep = stepFunc(func(context.Context, RunState) (bool, error) {
				return false, nil
			})
		})

		It("succeeds anyway", func() {
			Expect(stepErr).NotTo(HaveOccurred())
			Expect(stepOk).To(BeTrue())
		})
	})

	Context("when interrupted", func() {
		BeforeEach(func() {
			runStep = stepFunc(func(context.Context, RunState) (bool, error) {
				return false, context.Canceled
			})
		})

		It("propagates the error and does not succeed", func() {
			Expect(stepErr).To(Equal(context.Canceled))
			Expect(stepOk).To(BeFalse())
		})
	})

	Context("when the inner step returns any other error", func() {
		BeforeEach(func() {
			runStep = stepFunc(func(context.Context, RunState) (bool, error) {
				return false, errors.New("some error")
			})
		})

		It("swallows the error", func() {
			Expect(stepErr).NotTo(HaveOccurred())
			Expect(stepOk).To(BeTrue())
		})
	})
})
