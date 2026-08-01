package commands

import (
	"errors"
	"io"
	"os"
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

func TestFailureReasonSkipsBlankMessagesToFindTheRealOne(t *testing.T) {
	reason := failureReasonFromErrorEvents([]event.Error{{Message: "  "}, {Message: "real"}})
	if reason != "real" {
		t.Fatalf("reason = %q, want %q", reason, "real")
	}
}

func TestExitStatusReasonFromFinishTaskEventsPrefersTheLastNonZeroExit(t *testing.T) {
	reason := exitStatusReasonFromFinishTaskEvents([]event.FinishTask{
		{ExitStatus: 1},
		{ExitStatus: 0},
		{ExitStatus: 2},
	})
	if reason != "agent step exited 2" {
		t.Fatalf("reason = %q", reason)
	}
}

func TestExitStatusReasonFromFinishTaskEventsIsEmptyWithoutANonZeroExit(t *testing.T) {
	if reason := exitStatusReasonFromFinishTaskEvents(nil); reason != "" {
		t.Fatalf("reason = %q, want empty", reason)
	}
	if reason := exitStatusReasonFromFinishTaskEvents([]event.FinishTask{{ExitStatus: 0}}); reason != "" {
		t.Fatalf("reason = %q, want empty", reason)
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

func TestRunFailureReasonSkipsEventFetchForNonTerminalUnsuccessfulStatuses(t *testing.T) {
	for _, status := range []db.AgentWorkflowRunStatus{
		db.AgentWorkflowRunStatusSucceeded,
		db.AgentWorkflowRunStatusRunning,
		db.AgentWorkflowRunStatusAdmitting,
		db.AgentWorkflowRunStatusCanceling,
	} {
		t.Run(string(status), func(t *testing.T) {
			client := new(concoursefakes.FakeClient)
			target := new(rcfakes.FakeTarget)
			target.ClientReturns(client)

			buildID := int64(42)
			run := workflowrunsapi.RunSummary{Status: status, PlannedBuildID: &buildID}
			if reason := runFailureReason(target, run); reason != "" {
				t.Fatalf("reason = %q, want empty", reason)
			}
			if client.BuildEventsCallCount() != 0 {
				t.Fatalf("BuildEvents called %d times, want 0", client.BuildEventsCallCount())
			}
		})
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

// TestRunFailureReasonFallsBackToExitStatusForAPlainFailedRun pins the case
// this feature exists to serve: a step whose process exits non-zero without
// the step itself erroring (e.g. `agent-runner: claude: exit status 1`)
// produces event.FinishTask but no event.Error at all. Without the fallback,
// a plain `failed` run would report nothing.
func TestRunFailureReasonFallsBackToExitStatusForAPlainFailedRun(t *testing.T) {
	client := new(concoursefakes.FakeClient)
	source := &fakeEventSource{
		events: []atc.Event{
			event.FinishTask{ExitStatus: 1},
		},
	}
	client.BuildEventsReturns(source, nil)
	target := new(rcfakes.FakeTarget)
	target.ClientReturns(client)

	buildID := int64(12)
	run := workflowrunsapi.RunSummary{Status: db.AgentWorkflowRunStatusFailed, PlannedBuildID: &buildID}
	reason := runFailureReason(target, run)
	if reason != "agent step exited 1" {
		t.Fatalf("reason = %q, want %q", reason, "agent step exited 1")
	}
}

func TestRunFailureReasonPrefersAnErrorMessageOverAnExitStatus(t *testing.T) {
	client := new(concoursefakes.FakeClient)
	source := &fakeEventSource{
		events: []atc.Event{
			event.FinishTask{ExitStatus: 1},
			event.Error{Message: "the real cause"},
		},
	}
	client.BuildEventsReturns(source, nil)
	target := new(rcfakes.FakeTarget)
	target.ClientReturns(client)

	buildID := int64(13)
	run := workflowrunsapi.RunSummary{Status: db.AgentWorkflowRunStatusFailed, PlannedBuildID: &buildID}
	if reason := runFailureReason(target, run); reason != "the real cause" {
		t.Fatalf("reason = %q, want %q", reason, "the real cause")
	}
}

func TestRunFailureReasonGivesUpAtTheScanLimitInsteadOfHanging(t *testing.T) {
	original := runFailureReasonEventScanLimit
	runFailureReasonEventScanLimit = 2
	defer func() { runFailureReasonEventScanLimit = original }()

	client := new(concoursefakes.FakeClient)
	source := &fakeEventSource{
		events: []atc.Event{
			event.FinishTask{ExitStatus: 1},
			event.FinishTask{ExitStatus: 1},
			// The real error arrives after the scan limit and must be missed.
			event.Error{Message: "arrives too late to be read"},
		},
	}
	client.BuildEventsReturns(source, nil)
	target := new(rcfakes.FakeTarget)
	target.ClientReturns(client)

	buildID := int64(14)
	run := workflowrunsapi.RunSummary{Status: db.AgentWorkflowRunStatusFailed, PlannedBuildID: &buildID}
	reason := runFailureReason(target, run)
	if reason != "agent step exited 1" {
		t.Fatalf("reason = %q, want the exit-status evidence gathered before the cutoff", reason)
	}
	if source.index != 2 {
		t.Fatalf("events read = %d, want the scan to stop at the limit (2)", source.index)
	}
}

// captureStdout redirects os.Stdout for the duration of fn and returns
// everything written to it. Used to cover printAgentWorkflowRunDetail's
// user-visible "failure:" and "full log:" lines, which no other test
// exercises.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = write
	defer func() { os.Stdout = original }()

	fn()

	if err := write.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(read)
	if err != nil {
		t.Fatal(err)
	}
	return string(output)
}

func TestPrintAgentWorkflowRunDetailPrintsFailureAndFullLogForAFailedRun(t *testing.T) {
	client := new(concoursefakes.FakeClient)
	source := &fakeEventSource{
		events: []atc.Event{
			event.Error{Message: "the real cause"},
		},
	}
	client.BuildEventsReturns(source, nil)
	target := new(rcfakes.FakeTarget)
	target.ClientReturns(client)

	buildID := int64(21)
	detail := workflowrunsapi.RunDetail{
		RunSummary: workflowrunsapi.RunSummary{
			WorkflowName: "code-review", Status: db.AgentWorkflowRunStatusFailed, PlannedBuildID: &buildID,
		},
	}
	output := captureStdout(t, func() {
		if err := printAgentWorkflowRunDetail(target, detail, false); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(output, "failure: the real cause\n") {
		t.Fatalf("output = %q, want a failure line", output)
	}
	if !strings.Contains(output, "full log: fly -t "+string(Fly.Target)+" watch -b 21\n") {
		t.Fatalf("output = %q, want a full log line", output)
	}
}

// TestPrintAgentWorkflowRunDetailPrintsFullLogEvenWithoutAReason pins
// Important-2: the "full log:" hint must not be nested inside "found a
// reason" — a plain `failed` run with no readable evidence at all (an empty
// event stream: no event.Error, no non-zero event.FinishTask) must still
// point the user at `fly watch` instead of leaving them with nothing beyond
// the bare status line.
func TestPrintAgentWorkflowRunDetailPrintsFullLogEvenWithoutAReason(t *testing.T) {
	client := new(concoursefakes.FakeClient)
	client.BuildEventsReturns(&fakeEventSource{}, nil)
	target := new(rcfakes.FakeTarget)
	target.ClientReturns(client)

	buildID := int64(22)
	detail := workflowrunsapi.RunDetail{
		RunSummary: workflowrunsapi.RunSummary{
			WorkflowName: "code-review", Status: db.AgentWorkflowRunStatusFailed, PlannedBuildID: &buildID,
		},
	}
	output := captureStdout(t, func() {
		if err := printAgentWorkflowRunDetail(target, detail, false); err != nil {
			t.Fatal(err)
		}
	})
	if strings.Contains(output, "failure:") {
		t.Fatalf("output = %q, want no failure line when no evidence was found", output)
	}
	if !strings.Contains(output, "full log: fly -t "+string(Fly.Target)+" watch -b 22\n") {
		t.Fatalf("output = %q, want a full log line", output)
	}
}

func TestPrintAgentWorkflowRunDetailPrintsNeitherLineForASucceededRun(t *testing.T) {
	client := new(concoursefakes.FakeClient)
	target := new(rcfakes.FakeTarget)
	target.ClientReturns(client)

	buildID := int64(23)
	detail := workflowrunsapi.RunDetail{
		RunSummary: workflowrunsapi.RunSummary{
			WorkflowName: "code-review", Status: db.AgentWorkflowRunStatusSucceeded, PlannedBuildID: &buildID,
		},
	}
	output := captureStdout(t, func() {
		if err := printAgentWorkflowRunDetail(target, detail, false); err != nil {
			t.Fatal(err)
		}
	})
	if strings.Contains(output, "failure:") || strings.Contains(output, "full log:") {
		t.Fatalf("output = %q, want neither line for a succeeded run", output)
	}
	if client.BuildEventsCallCount() != 0 {
		t.Fatalf("BuildEvents called %d times, want 0", client.BuildEventsCallCount())
	}
}
