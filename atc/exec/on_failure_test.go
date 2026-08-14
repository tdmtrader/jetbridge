package exec_test

import (
	"context"
	"errors"

	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/tracing"
	"github.com/concourse/concourse/vars"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

var _ = Describe("On Failure Step", func() {
	var (
		ctx    context.Context
		cancel func()

		step stepFunc
		hook stepFunc

		state        exec.RunState
		events       chan string
		mainContexts chan context.Context
		hookContexts chan context.Context
		hookStates   chan exec.RunState

		stepOk  bool
		stepErr error
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())

		events = make(chan string, 2)
		mainContexts = make(chan context.Context, 1)
		hookContexts = make(chan context.Context, 1)
		hookStates = make(chan exec.RunState, 1)
		step = stepFunc(func(ctx context.Context, _ exec.RunState) (bool, error) {
			events <- "main-started"
			mainContexts <- ctx
			return false, nil
		})
		hook = stepFunc(func(ctx context.Context, state exec.RunState) (bool, error) {
			events <- "failure-hook-started"
			hookContexts <- ctx
			hookStates <- state
			return true, nil
		})

		state = exec.NewRunState(noopStepper, vars.StaticVariables{})
	})

	AfterEach(func() {
		cancel()
	})

	JustBeforeEach(func() {
		stepOk, stepErr = exec.OnFailure(step, hook).Run(ctx, state)
	})

	Context("when the step fails", func() {
		BeforeEach(func() {
			step = stepFunc(func(ctx context.Context, _ exec.RunState) (bool, error) {
				events <- "main-started"
				mainContexts <- ctx
				return false, nil
			})
		})

		It("runs the failure hook", func() {
			Expect(events).To(Receive(Equal("main-started")))
			Expect(events).To(Receive(Equal("failure-hook-started")))
		})

		It("runs the hook with the run state", func() {
			Expect(hookStates).To(Receive(Equal(state)))
		})

		It("propagates the context to the hook", func() {
			Expect(hookContexts).To(Receive(Equal(ctx)))
		})

		It("does not error", func() {
			Expect(stepErr).ToNot(HaveOccurred())
		})
	})

	Context("when the step errors", func() {
		disaster := errors.New("disaster")

		BeforeEach(func() {
			step = stepFunc(func(ctx context.Context, _ exec.RunState) (bool, error) {
				events <- "main-started"
				mainContexts <- ctx
				return false, disaster
			})
		})

		It("does not run the failure hook", func() {
			Expect(events).To(Receive(Equal("main-started")))
			Consistently(events).ShouldNot(Receive())
		})

		It("returns the error", func() {
			Expect(stepErr).To(Equal(disaster))
		})
	})

	Context("when the step succeeds", func() {
		BeforeEach(func() {
			step = stepFunc(func(ctx context.Context, _ exec.RunState) (bool, error) {
				events <- "main-started"
				mainContexts <- ctx
				return true, nil
			})
		})

		It("does not run the failure hook", func() {
			Expect(events).To(Receive(Equal("main-started")))
			Consistently(events).ShouldNot(Receive())
		})

		It("returns nil", func() {
			Expect(stepErr).To(BeNil())
		})
	})

	It("propagates the context to the step", func() {
		Expect(mainContexts).To(Receive(Equal(ctx)))
	})

	Context("when step fails and hook fails", func() {
		BeforeEach(func() {
			step = stepFunc(func(context.Context, exec.RunState) (bool, error) {
				events <- "main-started"
				return false, nil
			})
			hook = stepFunc(func(context.Context, exec.RunState) (bool, error) {
				events <- "failure-hook-started"
				return false, nil
			})
		})

		It("fails", func() {
			Expect(stepOk).To(BeFalse())
		})
	})

	Context("when step fails and hook succeeds", func() {
		BeforeEach(func() {
			step = stepFunc(func(context.Context, exec.RunState) (bool, error) {
				events <- "main-started"
				return false, nil
			})
			hook = stepFunc(func(context.Context, exec.RunState) (bool, error) {
				events <- "failure-hook-started"
				return true, nil
			})
		})

		It("fails", func() {
			Expect(stepOk).To(BeFalse())
		})
	})

	Context("when step succeeds", func() {
		BeforeEach(func() {
			step = stepFunc(func(context.Context, exec.RunState) (bool, error) {
				events <- "main-started"
				return true, nil
			})
		})

		It("succeeds", func() {
			Expect(stepOk).To(BeTrue())
		})
	})

	Context("when tracing is enabled", func() {
		var spanRecorder *tracetest.SpanRecorder

		BeforeEach(func() {
			spanRecorder = new(tracetest.SpanRecorder)
			tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
			tracing.ConfigureTraceProvider(tp)

			step = stepFunc(func(context.Context, exec.RunState) (bool, error) {
				events <- "main-started"
				return false, nil
			})
			hook = stepFunc(func(context.Context, exec.RunState) (bool, error) {
				events <- "failure-hook-started"
				return true, nil
			})
		})

		AfterEach(func() {
			tracing.Configured = false
		})

		It("creates a span for the on_failure hook", func() {
			ended := spanRecorder.Ended()
			Expect(ended).To(HaveLen(1))
			Expect(ended[0].Name()).To(Equal("hook.on_failure"))
		})
	})
})
