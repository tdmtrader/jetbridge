package exec_test

import (
	"context"
	"errors"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/vars"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("RunState", func() {
	var (
		stepper     exec.Stepper
		steppedPlan atc.Plan
		fakeStep    *scriptedStep

		credVars vars.Variables

		state exec.RunState
	)

	BeforeEach(func() {
		fakeStep = new(scriptedStep)
		stepper = func(plan atc.Plan) exec.Step {
			steppedPlan = plan
			return fakeStep
		}

		credVars = vars.StaticVariables{"k1": "v1", "k2": "v2", "k3": "v3"}

		state = exec.NewRunState(stepper, credVars)
	})

	Describe("Run", func() {
		var ctx context.Context
		var plan atc.Plan

		var runOk bool
		var runErr error

		BeforeEach(func() {
			ctx = context.Background()
			plan = atc.Plan{
				ID: "some-plan",
				LoadVar: &atc.LoadVarPlan{
					Name: "foo",
					File: "bar",
				},
			}

			fakeStep.RunReturns(true, nil)
		})

		JustBeforeEach(func() {
			runOk, runErr = state.Run(ctx, plan)
		})

		It("constructs and runs a step for the plan", func() {
			Expect(steppedPlan).To(Equal(plan))
			Expect(fakeStep.RunCallCount()).To(Equal(1))
			runCtx, runState := fakeStep.RunArgsForCall(0)
			Expect(runCtx).To(Equal(ctx))
			Expect(runState).To(Equal(state))
		})

		Context("when the step succeeds", func() {
			BeforeEach(func() {
				fakeStep.RunReturns(true, nil)
			})

			It("succeeds", func() {
				Expect(runOk).To(BeTrue())
			})
		})

		Context("when the step fails", func() {
			BeforeEach(func() {
				fakeStep.RunReturns(false, nil)
			})

			It("fails", func() {
				Expect(runOk).To(BeFalse())
			})
		})

		Context("when the step errors", func() {
			disaster := errors.New("nope")

			BeforeEach(func() {
				fakeStep.RunReturns(false, disaster)
			})

			It("returns the error", func() {
				Expect(runErr).To(Equal(disaster))
			})
		})
	})
})
