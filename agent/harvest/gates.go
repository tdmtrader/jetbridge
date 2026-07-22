package harvest

import (
	"context"
	"encoding/json"

	functiongates "github.com/concourse/concourse/agent/functions/gates"
	schema "github.com/concourse/concourse/agent/schema"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

// GateOutcome remains the legacy harvest wire name while the canonical
// deterministic function emits the gate-results/v1 contract directly.
type GateOutcome = contracts.GateOutcome

// RunGates is the v1/v2 compatibility adapter around the reusable gate
// function. Version-3 workflows invoke the same runner as an explicit node.
func RunGates(policy GatePolicy, workspaceDir string, events *schema.EventWriter) ([]GateOutcome, error) {
	gates := make([]functiongates.Gate, 0, len(policy.Gates))
	for _, gate := range policy.Gates {
		gates = append(gates, functiongates.Gate{
			Name: gate.Gate, Scope: gate.Scope, Focus: gate.Focus,
			Timeout: gate.Timeout, Retries: gate.Retries,
		})
	}
	runner := functiongates.NewRunner(nil).WithObserver(gateEventObserver{events: events})
	document, err := runner.Run(context.Background(), workspaceDir, functiongates.Policy{
		Gates: gates, OnGateFailure: policy.OnGateFailure,
	})
	if err != nil {
		return nil, err
	}
	return append([]GateOutcome(nil), document.Gates...), nil
}

type gateEventObserver struct {
	events *schema.EventWriter
}

func (observer gateEventObserver) AttemptStarted(_ context.Context, event functiongates.AttemptEvent) {
	emitEvent(observer.events, schema.EventGateStart, schema.GateStartData{
		Gate: event.Gate.Name, Scope: event.Gate.Scope,
	})
}

func (observer gateEventObserver) AttemptFinished(
	_ context.Context,
	_ functiongates.AttemptEvent,
	outcome contracts.GateOutcome,
) {
	emitEvent(observer.events, schema.EventGateResult, schema.GateResultData{
		Gate: outcome.Gate, Scope: outcome.Scope, Status: outcome.Status,
		DurationSeconds: outcome.DurationSeconds,
		Summary:         truncate(outcome.Detail, 4096),
		Attempt:         outcome.Attempt,
		Flaky:           outcome.Flaky,
	})
}

// emitEvent writes one event to a nil-tolerant writer; marshal or write
// failures are ignored because evidence recording must not alter gate flow.
func emitEvent(events *schema.EventWriter, eventType schema.EventType, payload any) {
	if events == nil {
		return
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_ = events.Write(schema.Event{Type: eventType, Data: data})
}

func truncate(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	return value[:maximum]
}
