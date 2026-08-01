package commands

import (
	"errors"
	"io"
	"strings"
	"testing"

	workflowrunsapi "github.com/concourse/concourse/agent/api/workflowruns"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/event"
	"github.com/concourse/concourse/fly/rc/rcfakes"
	"github.com/concourse/concourse/go-concourse/concourse/concoursefakes"
	"github.com/vito/go-sse/sse"
)

func TestFailureReasonPrefersTheLastErrorEvent(t *testing.T) {
	events := []event.Error{
		{Message: "an earlier, superseded failure"},
		{Message: `snapshot: validate output "review": required regular file "record.json" is missing`},
	}
	reason := failureReasonFromErrorEvents(events)
	if reason != `snapshot: validate output "review": required regular file "record.json" is missing` {
		t.Fatalf("reason = %q", reason)
	}
}

func TestFailureReasonIsEmptyWithoutErrorEvents(t *testing.T) {
	if reason := failureReasonFromErrorEvents(nil); reason != "" {
		t.Fatalf("reason = %q, want empty", reason)
	}
}

func TestFailureReasonIsTrimmedToOneReadableLine(t *testing.T) {
	reason := failureReasonFromErrorEvents([]event.Error{{Message: "  line one\nline two  \n"}})
	if strings.Contains(reason, "\n") || !strings.HasPrefix(reason, "line one") {
		t.Fatalf("reason = %q", reason)
	}
}

// fakeEventSource is a minimal concourse.Events implementation for testing
// runFailureReason's event-stream consumption without a real ATC.
type fakeEventSource struct {
	events  []atc.Event
	nextErr error
	index   int
	closed  bool
}

func (source *fakeEventSource) NextEvent() (atc.Event, error) {
	if source.index >= len(source.events) {
		if source.nextErr != nil {
			return nil, source.nextErr
		}
		return nil, io.EOF
	}
	next := source.events[source.index]
	source.index++
	return next, nil
}

func (source *fakeEventSource) NextEventRaw() (sse.Event, error) {
	return sse.Event{}, io.EOF
}

func (source *fakeEventSource) Close() error {
	source.closed = true
	return nil
}

func TestRunFailureReasonSkipsEventFetchForSucceededRun(t *testing.T) {
	client := new(concoursefakes.FakeClient)
	target := new(rcfakes.FakeTarget)
	target.ClientReturns(client)

	buildID := int64(42)
	run := workflowrunsapi.RunSummary{Status: db.AgentWorkflowRunStatusSucceeded, PlannedBuildID: &buildID}
	if reason := runFailureReason(target, run); reason != "" {
		t.Fatalf("reason = %q, want empty", reason)
	}
	if client.BuildEventsCallCount() != 0 {
		t.Fatalf("BuildEvents called %d times, want 0", client.BuildEventsCallCount())
	}
}

func TestRunFailureReasonSkipsEventFetchForNonTerminalRun(t *testing.T) {
	client := new(concoursefakes.FakeClient)
	target := new(rcfakes.FakeTarget)
	target.ClientReturns(client)

	buildID := int64(42)
	run := workflowrunsapi.RunSummary{Status: db.AgentWorkflowRunStatusRunning, PlannedBuildID: &buildID}
	if reason := runFailureReason(target, run); reason != "" {
		t.Fatalf("reason = %q, want empty", reason)
	}
	if client.BuildEventsCallCount() != 0 {
		t.Fatalf("BuildEvents called %d times, want 0", client.BuildEventsCallCount())
	}
}

func TestRunFailureReasonReturnsEmptyForNilPlannedBuildID(t *testing.T) {
	client := new(concoursefakes.FakeClient)
	target := new(rcfakes.FakeTarget)
	target.ClientReturns(client)

	run := workflowrunsapi.RunSummary{Status: db.AgentWorkflowRunStatusFailed, PlannedBuildID: nil}
	if reason := runFailureReason(target, run); reason != "" {
		t.Fatalf("reason = %q, want empty", reason)
	}
	if client.BuildEventsCallCount() != 0 {
		t.Fatalf("BuildEvents called %d times, want 0", client.BuildEventsCallCount())
	}
}

func TestRunFailureReasonReturnsEmptyWhenBuildEventsErrors(t *testing.T) {
	client := new(concoursefakes.FakeClient)
	client.BuildEventsReturns(nil, errors.New("boom"))
	target := new(rcfakes.FakeTarget)
	target.ClientReturns(client)

	buildID := int64(7)
	run := workflowrunsapi.RunSummary{Status: db.AgentWorkflowRunStatusErrored, PlannedBuildID: &buildID}
	if reason := runFailureReason(target, run); reason != "" {
		t.Fatalf("reason = %q, want empty", reason)
	}
}

func TestRunFailureReasonReturnsEmptyOnUnparseableStream(t *testing.T) {
	client := new(concoursefakes.FakeClient)
	source := &fakeEventSource{nextErr: errors.New("bad event")}
	client.BuildEventsReturns(source, nil)
	target := new(rcfakes.FakeTarget)
	target.ClientReturns(client)

	buildID := int64(9)
	run := workflowrunsapi.RunSummary{Status: db.AgentWorkflowRunStatusAborted, PlannedBuildID: &buildID}
	if reason := runFailureReason(target, run); reason != "" {
		t.Fatalf("reason = %q, want empty", reason)
	}
	if !source.closed {
		t.Fatal("event source was not closed after a stream error")
	}
}

func TestRunFailureReasonReadsTheTerminalErrorEventFromTheStream(t *testing.T) {
	client := new(concoursefakes.FakeClient)
	source := &fakeEventSource{
		events: []atc.Event{
			event.Error{Message: "an earlier, superseded failure"},
			event.FinishTask{ExitStatus: 1},
			event.Error{Message: `snapshot: validate output "review": required regular file "record.json" is missing`},
		},
	}
	client.BuildEventsReturns(source, nil)
	target := new(rcfakes.FakeTarget)
	target.ClientReturns(client)

	buildID := int64(11)
	run := workflowrunsapi.RunSummary{Status: db.AgentWorkflowRunStatusFailed, PlannedBuildID: &buildID}
	reason := runFailureReason(target, run)
	want := `snapshot: validate output "review": required regular file "record.json" is missing`
	if reason != want {
		t.Fatalf("reason = %q, want %q", reason, want)
	}
	if !source.closed {
		t.Fatal("event source was not closed")
	}
	if got := client.BuildEventsArgsForCall(0); got != "11" {
		t.Fatalf("BuildEvents called with %q, want %q", got, "11")
	}
}
