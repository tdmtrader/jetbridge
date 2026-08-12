package exec_test

import (
	"context"
	"errors"
	"net"
	"net/url"

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

var _ = Describe("RetryErrorStep", func() {
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
			"retry-error-team",
			atc.PipelineRef{Name: "some-pipeline"},
			atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}},
			"some-user",
		)

		delegateFactory = buildStepDelegateFactory(func(state RunState) BuildStepDelegate {
			return engine.NewBuildStepDelegate(build, "some-plan-id", state, clock.NewClock(), policy.NoopChecker{}, false)
		})

		state = NewRunState(noopStepper, vars.StaticVariables{})

		step = RetryError(fakeStep, delegateFactory)
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

			It("returns nil", func() {
				Expect(runErr).To(BeNil())
			})

			It("does not log", func() {
				Expect(execBuildErrorMessages(fixture, build)).To(BeEmpty())
			})
		})

		Context("when aborted", func() {
			BeforeEach(func() {
				fakeStep.RunReturns(false, context.Canceled)
			})

			It("propagates the error", func() {
				Expect(runErr).To(Equal(context.Canceled))
			})
		})

		Context("when url.Error error happened", func() {
			cause := &url.Error{Op: "error", URL: "err", Err: errors.New("error")}
			BeforeEach(func() {
				fakeStep.RunReturns(false, cause)
			})

			It("should return retriable", func() {
				Expect(runErr).To(Equal(Retriable{cause}))
			})

			It("logs the retry", func() {
				Expect(execBuildErrorMessages(fixture, build)).To(Equal([]string{cause.Error() + ", will retry ..."}))
			})
		})

		Context("when net.Error error happened", func() {
			cause := &net.OpError{Op: "read", Net: "test", Source: nil, Addr: nil, Err: errors.New("test")}
			BeforeEach(func() {
				fakeStep.RunReturns(false, cause)
			})

			It("should return retriable", func() {
				Expect(runErr).To(Equal(Retriable{cause}))
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
		})

		Context("when the wrapped step has succeeded", func() {
			BeforeEach(func() {
				fakeStep.RunReturns(true, nil)
			})

			It("returns true", func() {
				Expect(runOk).Should(BeTrue())
			})
		})

		Context("when the wrapped step has failed", func() {
			BeforeEach(func() {
				fakeStep.RunReturns(false, nil)
			})

			It("returns true", func() {
				Expect(runOk).Should(BeFalse())
			})
		})
	})
})
