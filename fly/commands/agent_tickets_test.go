package commands

import (
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/fly/rc"
)

const agentTicketsLargeWorkflowRunID = snapshot.WorkflowRunID(9007199254740993)

func TestAgentTicketsWorkflowRunFormattingIsLossless(t *testing.T) {
	pipelineRunID := 321
	ticket := tickets.Ticket{
		ID:            7,
		WorkflowName:  "review/source",
		WorkflowRunID: workflowRunIDPtr(agentTicketsLargeWorkflowRunID),
		PipelineRunID: &pipelineRunID,
	}

	if got := agentTicketWorkflowRunCell(ticket); got != "9007199254740993" {
		t.Fatalf("workflow-run list cell = %q, want lossless durable ID", got)
	}
	got := strings.Join(agentTicketRunLines(ticket, "dev"), "\n")
	want := "workflow run: 9007199254740993 · inspect with: fly -t dev agent workflows show-run review/source 9007199254740993\npipeline run: 321"
	if got != want {
		t.Fatalf("show run lines = %q, want %q", got, want)
	}
}

func TestAgentTicketsPipelineOnlyTicketHasNoDurableIdentity(t *testing.T) {
	pipelineRunID := 321
	ticket := tickets.Ticket{ID: 7, PipelineRunID: &pipelineRunID}

	if got := agentTicketWorkflowRunCell(ticket); got != "" {
		t.Fatalf("workflow-run list cell = %q, want empty", got)
	}
	got := strings.Join(agentTicketRunLines(ticket, "dev"), "\n")
	if got != "pipeline run: 321" {
		t.Fatalf("show run lines = %q, want diagnostic pipeline line only", got)
	}
	for _, forbidden := range []string{"workflow run:", "inspect with", "show-run", "agent-ticket-", "watch"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("pipeline-only output %q contains forbidden durable identity %q", got, forbidden)
		}
	}
}

func TestAgentTicketsWatchBuildsDurableShowRunCommand(t *testing.T) {
	ticket := tickets.Ticket{
		ID:            7,
		WorkflowName:  "review/source",
		WorkflowRunID: workflowRunIDPtr(agentTicketsLargeWorkflowRunID),
	}

	command, err := agentTicketWatchShowRunCommand(ticket)
	if err != nil {
		t.Fatal(err)
	}
	if command.Args.Workflow != "review/source" || command.Args.RunID != "9007199254740993" {
		t.Fatalf("show-run args = %#v", command.Args)
	}
	if !command.Follow || command.Outputs || command.Wait || command.Json {
		t.Fatalf("show-run flags = outputs:%t wait:%t follow:%t json:%t", command.Outputs, command.Wait, command.Follow, command.Json)
	}
}

func TestAgentTicketsWatchRejectsMissingWorkflowRunID(t *testing.T) {
	_, err := agentTicketWatchShowRunCommand(tickets.Ticket{ID: 7, WorkflowName: "review"})
	if err == nil || !strings.Contains(err.Error(), "ticket 7 has no workflow run") {
		t.Fatalf("error = %v, want stable missing durable ID error", err)
	}
}

func TestAgentTicketsWatchRejectsBlankWorkflowName(t *testing.T) {
	_, err := agentTicketWatchShowRunCommand(tickets.Ticket{
		ID:            7,
		WorkflowRunID: workflowRunIDPtr(agentTicketsLargeWorkflowRunID),
	})
	if err == nil || !strings.Contains(err.Error(), "ticket 7 has workflow run 9007199254740993 but no workflow name") {
		t.Fatalf("error = %v, want data-integrity workflow-name error", err)
	}
}

func TestAgentTicketsDispatchFormattingSeparatesDurableAndPipelineIDs(t *testing.T) {
	pipelineRunID := 321
	line, err := agentTicketDispatchLine(7, tickets.DispatchResponse{
		WorkflowRunID: agentTicketsLargeWorkflowRunID,
		PipelineRunID: &pipelineRunID,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "dispatched ticket #7 as workflow run 9007199254740993 (pipeline run 321)"
	if line != want {
		t.Fatalf("dispatch line = %q, want %q", line, want)
	}
}

// The pipeline run is a diagnostic. Its absence is reported by saying nothing
// about it, never by inventing a zero.
func TestAgentTicketsDispatchFormattingOmitsAnAbsentPipelineRun(t *testing.T) {
	line, err := agentTicketDispatchLine(7, tickets.DispatchResponse{
		WorkflowRunID: agentTicketsLargeWorkflowRunID,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "dispatched ticket #7 as workflow run 9007199254740993"
	if line != want {
		t.Fatalf("dispatch line = %q, want %q", line, want)
	}
}

func TestAgentTicketsDispatchRejectsMalformedSuccessWithoutWorkflowRunID(t *testing.T) {
	pipelineRunID := 321
	_, err := agentTicketDispatchLine(7, tickets.DispatchResponse{PipelineRunID: &pipelineRunID})
	if err == nil || !strings.Contains(err.Error(), "dispatch response for ticket 7 omitted workflow_run_id") {
		t.Fatalf("error = %v, want missing workflow_run_id", err)
	}
}

func TestAgentTicketsShowRunValidatesBeforeLoadingTarget(t *testing.T) {
	tests := []struct {
		name    string
		command WorkflowsShowRunCommand
	}{
		{
			name: "invalid flag combination",
			command: func() WorkflowsShowRunCommand {
				var command WorkflowsShowRunCommand
				command.Args.Workflow = "review"
				command.Args.RunID = "1"
				command.Outputs = true
				command.Follow = true
				return command
			}(),
		},
		{
			name: "invalid run id",
			command: func() WorkflowsShowRunCommand {
				var command WorkflowsShowRunCommand
				command.Args.Workflow = "review"
				command.Args.RunID = "not-an-id"
				return command
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loads := 0
			err := tt.command.executeWithTargetLoader(func() (rc.Target, error) {
				loads++
				return nil, nil
			})
			if err == nil {
				t.Fatal("expected local validation error")
			}
			if loads != 0 {
				t.Fatalf("target loader called %d times, want zero", loads)
			}
		})
	}
}

func workflowRunIDPtr(id snapshot.WorkflowRunID) *snapshot.WorkflowRunID {
	return &id
}

// The server accepts 1..500 and, for anything outside that range, silently
// falls back to its own default of 100 — HTTP 200, no warning, no cursor. So
// `--limit 1000` printed exactly 100 rows and exited 0, and an operator
// inventorying a 300-ticket queue had no way to tell 200 were missing. The
// request must be rejected before it is sent.
func TestAgentTicketsListRejectsLimitsTheServerWouldSilentlyIgnore(t *testing.T) {
	for _, limit := range []int{0, -1, agentTicketsListMaxLimit + 1, 1000} {
		command := &AgentTicketsListCommand{Limit: limit}
		err := command.Execute(nil)
		if err == nil {
			t.Fatalf("--limit %d was accepted; the server would have ignored it and returned its own default", limit)
		}
		if !strings.Contains(err.Error(), "--limit must be between 1 and 500") {
			t.Fatalf("--limit %d error = %q, want the accepted range", limit, err)
		}
	}
}
