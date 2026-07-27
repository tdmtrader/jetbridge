package commands

import (
	"fmt"
	"os"
	"strconv"

	agentschema "github.com/concourse/concourse/agent/schema"
	"github.com/concourse/concourse/fly/commands/internal/displayhelpers"
	"github.com/concourse/concourse/fly/rc"
	"github.com/concourse/concourse/fly/ui"
	"github.com/fatih/color"
)

type AgentRunsCommand struct {
	Limit int  `long:"limit" default:"50" description:"Maximum number of recent runs to show"`
	Json  bool `long:"json" description:"Print command result as JSON"`
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

	table := ui.Table{Headers: ui.TableRow{
		{Contents: "step", Color: color.New(color.Bold)},
		{Contents: "function", Color: color.New(color.Bold)},
		{Contents: "status", Color: color.New(color.Bold)},
		{Contents: "cost", Color: color.New(color.Bold)},
		{Contents: "tokens (in/out)", Color: color.New(color.Bold)},
		{Contents: "turns", Color: color.New(color.Bold)},
		{Contents: "run", Color: color.New(color.Bold)},
	}}
	for _, r := range runs {
		run := runLabel(r)
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
			{Contents: r.FunctionID},
			statusCell,
			{Contents: fmt.Sprintf("$%.2f", r.CostUSD)},
			{Contents: fmt.Sprintf("%d/%d", r.Usage.InputTokens, r.Usage.OutputTokens)},
			{Contents: strconv.Itoa(r.Turns)},
			{Contents: run},
		})
	}
	return table.Render(os.Stdout, Fly.PrintTableHeaders)
}

// runLabel renders a metric's durable identity: the workflow run and the
// function that produced the step, or "CI" for an unbound CI invocation (a
// build with no planned workflow run — never joined back to a ticket).
func runLabel(r agentschema.RunMetrics) string {
	if r.WorkflowRunID == nil {
		return "CI"
	}
	label := "#" + r.WorkflowRunID.String()
	if r.FunctionID != "" {
		label += " " + r.FunctionID
	}
	return label
}

// agentOutcomeColor colors the fused outcome with fly's build-status
// conventions, plus the agent-specific states: warn-yellow for no_output
// (green build that delivered nothing) and amber for unrecorded (delivered,
// but the flight recording is missing — L-1).
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
