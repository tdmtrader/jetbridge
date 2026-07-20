package commands

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	agentschema "github.com/concourse/concourse/agent/schema"
	"github.com/concourse/concourse/fly/commands/internal/displayhelpers"
	"github.com/concourse/concourse/fly/rc"
	"github.com/concourse/concourse/fly/ui"
	"github.com/fatih/color"
)

type AgentRunsCommand struct {
	Limit int  `long:"limit" default:"50" description:"Maximum number of recent runs to show"`
	Json  bool `long:"json" description:"Print command result as JSON"`

	Transcript AgentRunTranscriptCommand `command:"transcript" description:"Print the raw tool-call transcript (ndjson) for a ticket's run"`
}

// AgentRunTranscriptCommand prints the raw agent tool-call transcript
// (ndjson) a runner captured for a ticket's run (ticket #43) — the record of
// which dir the agent edited and whether it committed to the workspace.
type AgentRunTranscriptCommand struct {
	Ticket int `long:"ticket" required:"true" description:"Ticket ID the run belongs to"`
	Build  int `long:"build" required:"true" description:"Build ID of the run"`
}

func (command *AgentRunTranscriptCommand) Execute([]string) error {
	target, err := rc.LoadTarget(Fly.Target, Fly.Verbose)
	if err != nil {
		return err
	}
	if err := target.Validate(); err != nil {
		return err
	}

	raw, err := target.Client().AgentRunTranscript(command.Ticket, command.Build)
	if err != nil {
		return err
	}

	_, err = os.Stdout.Write(raw)
	return err
}

func (command *AgentRunsCommand) Execute([]string) error {
	target, err := rc.LoadTarget(Fly.Target, Fly.Verbose)
	if err != nil {
		return err
	}
	if err := target.Validate(); err != nil {
		return err
	}

	runs, err := target.Client().AgentRunMetrics(command.Limit)
	if err != nil {
		return err
	}

	if command.Json {
		return displayhelpers.JsonPrint(runs)
	}

	// Ticket #45 (runner-image skew visibility): one advisory line on
	// stderr before the table when the agent step image lags the web
	// binary. Best-effort — older servers don't serve the endpoint, and a
	// skew warning must never break the listing itself.
	if info, err := target.Client().AgentPlatformInfo(); err == nil && info.ImageVersionSkew {
		fmt.Fprintf(os.Stderr,
			"note: agent step image %s lags web %s - rebuild build-agent-runner-image + bump home-infra\n",
			imageTag(info.AgentStepImage), info.WebVersion)
	}

	table := ui.Table{Headers: ui.TableRow{
		{Contents: "step", Color: color.New(color.Bold)},
		{Contents: "workflow", Color: color.New(color.Bold)},
		{Contents: "status", Color: color.New(color.Bold)},
		{Contents: "cost", Color: color.New(color.Bold)},
		{Contents: "tokens (in/out)", Color: color.New(color.Bold)},
		{Contents: "turns", Color: color.New(color.Bold)},
		{Contents: "ticket", Color: color.New(color.Bold)},
	}}
	for _, r := range runs {
		workflow := r.WorkflowName
		if r.WorkflowVersion != nil {
			workflow = fmt.Sprintf("%s@%d", r.WorkflowName, *r.WorkflowVersion)
		}
		ticket := "CI"
		if r.TicketID != nil {
			ticket = "#" + strconv.Itoa(*r.TicketID)
		}
		// U3 display truth: render the server-fused outcome, never the raw
		// step status — a green step inside a failed build must not show
		// "ok". Pre-outcome servers omit the field; derive the same fusion
		// locally. If even that is underivable (unknown vocabulary), fall
		// back to the raw step status, uncolored.
		outcome := r.Outcome
		if outcome == "" {
			outcome = r.DeriveOutcome()
		}
		statusCell := ui.TableCell{Contents: r.Status}
		if outcome != "" {
			statusCell = ui.TableCell{Contents: outcome, Color: agentOutcomeColor(outcome)}
		}
		table.Data = append(table.Data, ui.TableRow{
			{Contents: r.StepName},
			{Contents: workflow},
			statusCell,
			{Contents: fmt.Sprintf("$%.2f", r.CostUSD)},
			{Contents: fmt.Sprintf("%d/%d", r.Usage.InputTokens, r.Usage.OutputTokens)},
			{Contents: strconv.Itoa(r.Turns)},
			{Contents: ticket},
		})
	}
	return table.Render(os.Stdout, Fly.PrintTableHeaders)
}

// imageTag extracts the tag for the one-line skew advisory ("v0.2.167"
// from "registry.home/agent-runner:v0.2.167"). A ":" before the last "/"
// is a registry port, not a tag; falls back to the full ref when there is
// no tag (skew is only ever true for parseable tags anyway).
func imageTag(ref string) string {
	if i := strings.Index(ref, "@"); i >= 0 {
		ref = ref[:i]
	}
	slash := strings.LastIndex(ref, "/")
	if colon := strings.LastIndex(ref, ":"); colon > slash {
		return ref[colon+1:]
	}
	return ref
}

// agentOutcomeColor colors the fused outcome with fly's build-status
// conventions, plus the agent-specific states: blue for parked (waiting on a
// human), warn-yellow for no_output (green build that delivered nothing), and
// amber for unrecorded (delivered, but the flight recording is missing — L-1).
func agentOutcomeColor(outcome string) *color.Color {
	switch outcome {
	case agentschema.RunOutcomeOK:
		return ui.SucceededColor
	case agentschema.RunOutcomeNoOutput:
		return color.New(color.FgYellow)
	case agentschema.RunOutcomeUnrecorded:
		return color.New(color.FgYellow)
	case agentschema.RunOutcomeRunning:
		return ui.StartedColor
	case agentschema.RunOutcomeParked:
		return color.New(color.FgBlue)
	case agentschema.RunOutcomeFailed:
		return ui.FailedColor
	case agentschema.RunOutcomeErrored:
		return ui.ErroredColor
	case agentschema.RunOutcomeAborted:
		return ui.AbortedColor
	default:
		return nil
	}
}
