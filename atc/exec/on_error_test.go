package exec_test

import (
	"context"
	"errors"

	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/tracing"
	"github.com/concourse/concourse/vars"
	"github.com/hashicorp/go-multierror"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

var _ = Describe("On Error Step", func() {
	var (
		ctx    context.Context
		cancel func()

		step stepFunc
		hook stepFunc

		state  exec.RunState
		events chan string

		stepOk  bool
		stepErr error

		disaster error
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())

		events = make(chan string, 2)
		step = stepFunc(func(context.Context, exec.RunState) (bool, error) {
			events <- "main-started"
			return false, nil
		})
		hook = stepFunc(func(context.Context, exec.RunState) (bool, error) {
			events <- "error-hook-started"
			return true, nil
		})

		state = exec.NewRunState(noopStepper, vars.StaticVariables{})

		stepErr = nil

		disaster = multierror.Append(nil, errors.New("disaster"))
	})

	AfterEach(func() {
		cancel()
	})

	JustBeforeEach(func() {
		stepOk, stepErr = exec.OnError(step, hook).Run(ctx, state)
	})

	Context("when the step errors", func() {
		BeforeEach(func() {
			step = stepFunc(func(context.Context, exec.RunState) (bool, error) {
				events <- "main-started"
				return false, disaster
			})
		})

		It("runs the error hook", func() {
			Expect(stepErr).To(Equal(disaster))
			Expect(events).To(Receive(Equal("main-started")))
			Expect(events).To(Receive(Equal("error-hook-started")))
		})
	})

	Context("when the step error is retriable", func() {
		BeforeEach(func() {
			disaster = multierror.Append(disaster, exec.Retriable{})
			step = stepFunc(func(context.Context, exec.RunState) (bool, error) {
				events <- "main-started"
				return false, disaster
			})
		})

		It("does not run the error hook", func() {
			Expect(stepErr).To(Equal(disaster))
			Expect(events).To(Receive(Equal("main-started")))
			Consistently(events).ShouldNot(Receive())
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

		It("does not run the error hook", func() {
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

		It("does not run the error hook", func() {
			Expect(events).To(Receive(Equal("main-started")))
			Consistently(events).ShouldNot(Receive())
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
				return false, errors.New("step error")
			})
			hook = stepFunc(func(context.Context, exec.RunState) (bool, error) {
				events <- "error-hook-started"
				return true, nil
			})
		})

		AfterEach(func() {
			tracing.Configured = false
		})

		It("creates a span for the on_error hook", func() {
			ended := spanRecorder.Ended()
			Expect(ended).To(HaveLen(1))
			Expect(ended[0].Name()).To(Equal("hook.on_error"))
		})
	})
})
