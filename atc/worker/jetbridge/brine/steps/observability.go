package steps

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/metric"
)

// Startup contracts use real kubelet-backed pods in live_observability.go,
// live_image_pull.go and live_init_recovery.go; no executor or status is substituted.
// All trace assertions read the production OTLP exporter via a real collector.

// TracingResourceDefinition owns an actual OTLP collector and the production
// trace provider. Each scenario exports to its own file and restores globals.
func TracingResourceDefinition() brine.ResourceDefinition {
	return brine.ResourceDefinition{
		Name:  "span-capture",
		Scope: brine.ScopeScenario,
		Factory: func(map[string]any) (any, error) {
			return SpanCapture{lazy: &lazyResource[SpanCapture]{start: startTraceCapture}}, nil
		},
		Disposer: func(value any) error {
			capture, ok := value.(SpanCapture)
			if !ok {
				return fmt.Errorf("span-capture disposer got %T", value)
			}
			return capture.close()
		},
	}
}

// ObservabilityDefinitions carries the OE family.
func ObservabilityDefinitions() []brine.StepDefinition {
	return append([]brine.StepDefinition{

		// Keeps its own body: this is membership in a collection the FIRST
		// parameter selects. CheckMember lists what was there instead, which
		// is the timeline a reader opens a trace for — but it takes its member
		// from parameter 0, and here that is the span name while the event is
		// parameter 1. No membership combinator takes a key.
		Assert[SpansRecorded](
			"the {string} span records the event {string}",
			func(in SpansRecorded, args Args) error {
				spanName := args.String(0)

				eventName := args.String(1)

				if in.WaitErr != nil {
					return fmt.Errorf("the step failed before spans could be asserted: %w", in.WaitErr)
				}

				names, found, err := in.Capture.EventNames(spanName)
				if err != nil {
					return err
				}
				if !found {
					return fmt.Errorf("expected a %q span, none was recorded", spanName)
				}
				for _, n := range names {
					if n == eventName {
						return nil
					}
				}
				return fmt.Errorf("expected the %q span to record %q, got [%s]",
					spanName, eventName, strings.Join(names, ", "))
			},
		),

		// A step that errored has no exit status to compare, so that case
		// reaches the reader as the getter's error rather than a comparison.
		CheckInt[SpansRecorded]("the step exits {int}",
			"the step's exit status",
			func(in SpansRecorded) (int, error) {
				if in.WaitErr != nil {
					return 0, fmt.Errorf("the step errored rather than exiting: %w", in.WaitErr)
				}
				return in.ExitStatus, nil
			}),
	}, LiveObservabilityDefinitions()...)
}

func waitAndCapture(in ExecStepRunning, timeouts ...time.Duration) (SpansRecorded, error) {
	timeout := 15 * time.Second
	if len(timeouts) > 1 {
		return SpansRecorded{}, fmt.Errorf("at most one observation timeout is allowed")
	}
	if len(timeouts) == 1 {
		timeout = timeouts[0]
	}
	if timeout <= 0 {
		return SpansRecorded{}, fmt.Errorf("observation timeout must be positive")
	}
	ctx, cancel := context.WithTimeout(in.Ctx, timeout)
	defer cancel()
	result, waitErr := in.Process.Wait(ctx)
	return SpansRecorded{
		Capture:    in.Capture,
		ExitStatus: result.ExitStatus,
		WaitErr:    waitErr,
	}, nil
}

// ObservabilityExtraDefinitions covers the rest of the OE family: what the
// span says about scheduling, init containers, sidecars and phase changes.
//
// The consumer is whoever opens a trace because a step took eight minutes.
// The events are the timeline that tells them WHERE the time went — waiting
// for a node, pulling an image, running an init container — so each scenario
// asserts a moment a reader would look for.
func ObservabilityExtraDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		// Count occurrences of the selected event, not the collection length.
		// Failure spans are valid inputs too. Even an expected count of zero
		// requires the named span to exist, so absent telemetry cannot pass.
		Assert[SpansRecorded](
			"the {string} span records the event {string} exactly {int} time(s)",
			func(in SpansRecorded, args Args) error {
				spanName := args.String(0)
				eventName := args.String(1)

				names, found, err := in.Capture.EventNames(spanName)
				if err != nil {
					return err
				}
				if !found {
					return fmt.Errorf("expected a %q span, none was recorded", spanName)
				}
				n := 0
				for _, e := range names {
					if e == eventName {
						n++
					}
				}
				if want := args.Int(2); n != want {
					return fmt.Errorf("expected %q exactly %d time(s) on the %q span, found it %d times (all: %s)",
						eventName, want, spanName, n, strings.Join(names, ", "))
				}
				return nil
			},
		),
	}
}

// ObservabilityMetricDefinitions covers OE-08 and OE-10 — the phase timeline
// and the operator-facing metrics.
//
// Metrics are the only signal an operator has BEFORE anyone opens a build.
// A cluster that has started failing to pull images, or whose pods have got
// slow to start, shows up here first or not at all.
func ObservabilityMetricDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		CheckThat[SpansRecorded]("a pod startup duration was recorded",
			func(in SpansRecorded) error {
				if in.WaitErr != nil {
					return fmt.Errorf("the step failed before startup could be timed: %v", in.WaitErr)
				}
				if got := metric.Metrics.K8sPodStartupDuration.Max(); got <= 0 {
					return fmt.Errorf("expected a positive startup duration, got %v — "+
						"an operator watching this gauge would see nothing", got)
				}
				return nil
			}),
	}
}

// InitContainerDefinitions covers RF-14 — what a step is told when the init
// container that stages its inputs fails.
//
// This is a distinct failure from "the step failed": the step never ran at
// all. Without the init container's name, state and logs in the error, the
// user sees a red build with no output and nothing to act on.
func InitContainerDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		// Keeps its own body: the message says WHY the name has to be there —
		// that the step never ran at all — which is the whole point of the
		// check and is more than a generic "expected … to mention" can say.
		// A trailing detail func would not recover it: detail is appended
		// context, and what is wanted here is the sentence itself.
		Assert[SpansRecorded](
			"the step is told which init container failed, naming {string}",
			func(in SpansRecorded, args Args) error {
				name := args.String(0)

				if in.WaitErr == nil {
					return fmt.Errorf("expected the step to fail because its init container did; it succeeded")
				}
				if !strings.Contains(in.Message, name) {
					return fmt.Errorf(
						"expected the failure to name the init container %q so the user knows the step never ran; got %q",
						name, in.Message)
				}
				if in.live != nil {
					return checkLiveInitDiagnostics(in)
				}
				return nil
			},
		),
	}
}
