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

var _ = Describe("Ensure Step", func() {
	var (
		ctx    context.Context
		cancel func()

		step stepFunc
		hook stepFunc

		state        exec.RunState
		events       chan string
		stepContexts chan context.Context
		hookContexts chan context.Context

		stepOk  bool
		stepErr error
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())

		events = make(chan string, 2)
		stepContexts = make(chan context.Context, 1)
		hookContexts = make(chan context.Context, 1)

		step = stepFunc(func(ctx context.Context, state exec.RunState) (bool, error) {
			events <- "main-started"
			stepContexts <- ctx
			return true, ctx.Err()
		})

		hook = stepFunc(func(ctx context.Context, state exec.RunState) (bool, error) {
			events <- "ensure-hook-started"
			hookContexts <- ctx
			return true, ctx.Err()
		})

		state = exec.NewRunState(noopStepper, vars.StaticVariables{})
	})

	JustBeforeEach(func() {
		stepOk, stepErr = exec.Ensure(step, hook).Run(ctx, state)
	})

	Context("when the step succeeds", func() {
		BeforeEach(func() {
			step = stepFunc(func(ctx context.Context, state exec.RunState) (bool, error) {
				events <- "main-started"
				stepContexts <- ctx
				return true, nil
			})
		})

		It("returns nil", func() {
			Expect(stepErr).ToNot(HaveOccurred())
		})

		It("runs the ensure hook", func() {
			Expect(events).To(Receive(Equal("main-started")))
			Expect(events).To(Receive(Equal("ensure-hook-started")))
		})
	})

	Context("when the step fails", func() {
		BeforeEach(func() {
			step = stepFunc(func(ctx context.Context, state exec.RunState) (bool, error) {
				events <- "main-started"
				stepContexts <- ctx
				return false, nil
			})
		})

		It("returns nil", func() {
			Expect(stepErr).ToNot(HaveOccurred())
		})

		It("runs the ensure hook", func() {
			Expect(events).To(Receive(Equal("main-started")))
			Expect(events).To(Receive(Equal("ensure-hook-started")))
		})
	})

	Context("when the step errors", func() {
		disaster := errors.New("disaster")

		BeforeEach(func() {
			step = stepFunc(func(ctx context.Context, state exec.RunState) (bool, error) {
				events <- "main-started"
				stepContexts <- ctx
				return false, disaster
			})
		})

		It("returns the error", func() {
			Expect(stepErr).To(HaveOccurred())
			Expect(stepErr.Error()).To(ContainSubstring("disaster"))
		})

		It("runs the ensure hook", func() {
			Expect(events).To(Receive(Equal("main-started")))
			Expect(events).To(Receive(Equal("ensure-hook-started")))
		})
	})

	Context("when the context is canceled during the first step", func() {
		BeforeEach(func() {
			cancel()
		})

		It("returns context.Canceled", func() {
			Expect(stepErr).To(Equal(context.Canceled))
		})

		It("cancels the first step and runs the hook (without canceling it)", func() {
			Expect(events).To(Receive(Equal("main-started")))
			Expect(events).To(Receive(Equal("ensure-hook-started")))

			stepCtx := <-stepContexts
			Expect(stepCtx.Err()).To(Equal(context.Canceled))

			hookCtx := <-hookContexts
			Expect(hookCtx.Err()).ToNot(HaveOccurred())
		})
	})

	Context("when the context is canceled during the hook", func() {
		BeforeEach(func() {
			hook = stepFunc(func(hookCtx context.Context, state exec.RunState) (bool, error) {
				events <- "ensure-hook-started"
				hookContexts <- hookCtx
				cancel()
				return false, ctx.Err()
			})
		})

		It("returns context.Canceled", func() {
			Expect(stepErr).To(Equal(context.Canceled))
		})

		It("allows canceling the hook if the first step has not been canceled", func() {
			Expect(events).To(Receive(Equal("main-started")))
			Expect(events).To(Receive(Equal("ensure-hook-started")))

			stepCtx := <-stepContexts
			Expect(stepCtx.Err()).To(Equal(context.Canceled))

			hookCtx := <-hookContexts
			Expect(hookCtx.Err()).To(Equal(context.Canceled))
		})
	})

	Context("when both step and hook succeed", func() {
		BeforeEach(func() {
			step = stepFunc(func(context.Context, exec.RunState) (bool, error) { return true, nil })
			hook = stepFunc(func(context.Context, exec.RunState) (bool, error) { return true, nil })
		})

		It("succeeds", func() {
			Expect(stepOk).To(BeTrue())
		})
	})

	Context("when step succeeds and hook fails", func() {
		BeforeEach(func() {
			step = stepFunc(func(context.Context, exec.RunState) (bool, error) { return true, nil })
			hook = stepFunc(func(context.Context, exec.RunState) (bool, error) { return false, nil })
		})

		It("does not succeed", func() {
			Expect(stepOk).To(BeFalse())
		})
	})

	Context("when step fails and hook succeeds", func() {
		BeforeEach(func() {
			step = stepFunc(func(context.Context, exec.RunState) (bool, error) { return false, nil })
			hook = stepFunc(func(context.Context, exec.RunState) (bool, error) { return true, nil })
		})

		It("does not succeed", func() {
			Expect(stepOk).To(BeFalse())
		})
	})

	Context("when both step and hook fail", func() {
		BeforeEach(func() {
			step = stepFunc(func(context.Context, exec.RunState) (bool, error) { return false, nil })
			hook = stepFunc(func(context.Context, exec.RunState) (bool, error) { return false, nil })
		})

		It("does not succeed", func() {
			Expect(stepOk).To(BeFalse())
		})
	})

	Context("when tracing is enabled", func() {
		var spanRecorder *tracetest.SpanRecorder

		BeforeEach(func() {
			spanRecorder = new(tracetest.SpanRecorder)
			tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
			tracing.ConfigureTraceProvider(tp)

			step = stepFunc(func(context.Context, exec.RunState) (bool, error) { return true, nil })
			hook = stepFunc(func(context.Context, exec.RunState) (bool, error) { return true, nil })
		})

		AfterEach(func() {
			tracing.Configured = false
		})

		It("creates a span for the ensure hook", func() {
			ended := spanRecorder.Ended()
			Expect(ended).To(HaveLen(1))
			Expect(ended[0].Name()).To(Equal("hook.ensure"))
		})
	})
})
