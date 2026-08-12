package exec_test

import (
	"context"
	"errors"
	"fmt"

	"code.cloudfoundry.org/clock"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/engine"
	. "github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/policy"
	"github.com/concourse/concourse/vars"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("LogErrorStep", func() {
	var (
		ctx    context.Context
		cancel func()

		fakeStep *scriptedStep

		fixture         *execDBFixture
		build           db.Build
		delegateFactory BuildStepDelegateFactory

		state RunState

		step Step
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())

		fakeStep = new(scriptedStep)

		fixture = useExecDB()
		_, _, _, build = createExecJobBuild(
			fixture,
			"log-error-team",
			atc.PipelineRef{Name: "some-pipeline"},
			atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}},
			"some-user",
		)

		delegateFactory = buildStepDelegateFactory(func(state RunState) BuildStepDelegate {
			return engine.NewBuildStepDelegate(build, "some-plan-id", state, clock.NewClock(), policy.NoopChecker{}, false)
		})

		state = NewRunState(noopStepper, vars.StaticVariables{})

		step = LogError(fakeStep, delegateFactory)
	})

	AfterEach(func() {
		cancel()
	})

	Describe("Run", func() {
		var runOk bool
		var runErr error

		JustBeforeEach(func() {
			runOk, runErr = step.Run(ctx, state)
		})

		Context("when the inner step does not error", func() {
			BeforeEach(func() {
				fakeStep.RunReturns(true, nil)
			})

			It("returns true", func() {
				Expect(runOk).Should(BeTrue())
			})

			It("returns nil", func() {
				Expect(runErr).To(BeNil())
			})

			It("does not log", func() {
				Expect(execBuildErrorMessages(fixture, build)).To(BeEmpty())
			})
		})

		Context("when the inner step has failed", func() {
			BeforeEach(func() {
				fakeStep.RunReturns(false, nil)
			})

			It("returns false", func() {
				Expect(runOk).Should(BeFalse())
			})

			It("returns nil", func() {
				Expect(runErr).To(BeNil())
			})

			It("does not log", func() {
				Expect(execBuildErrorMessages(fixture, build)).To(BeEmpty())
			})
		})

		Context("when aborted", func() {
			var canceled = fmt.Errorf("wrapped: %w", context.Canceled)

			BeforeEach(func() {
				fakeStep.RunReturns(false, canceled)
			})

			It("propagates the error", func() {
				Expect(runErr).To(Equal(canceled))
			})

			It("logs 'interrupted'", func() {
				Expect(execBuildErrorMessages(fixture, build)).To(Equal([]string{"interrupted"}))
			})
		})

		Context("when timed out", func() {
			var timedOut = fmt.Errorf("wrapped: %w", context.DeadlineExceeded)

			BeforeEach(func() {
				fakeStep.RunReturns(false, timedOut)
			})

			It("propagates the error", func() {
				Expect(runErr).To(Equal(timedOut))
			})

			It("logs 'timeout exceeded'", func() {
				Expect(execBuildErrorMessages(fixture, build)).To(Equal([]string{"timeout exceeded"}))
			})
		})

		Context("when the inner step returns any other error", func() {
			disaster := errors.New("disaster")

			BeforeEach(func() {
				fakeStep.RunReturns(false, disaster)
			})

			It("propagates the error", func() {
				Expect(runErr).To(Equal(disaster))
			})

			It("logs the error", func() {
				Expect(execBuildErrorMessages(fixture, build)).To(Equal([]string{"disaster"}))
			})
		})
	})
})
