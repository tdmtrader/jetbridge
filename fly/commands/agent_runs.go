package commands

import (
	"fmt"
	"os"
	"strconv"

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
		table.Data = append(table.Data, ui.TableRow{
			{Contents: r.StepName},
			{Contents: workflow},
			{Contents: r.Status, Color: agentStatusColor(r.Status)},
			{Contents: fmt.Sprintf("$%.2f", r.CostUSD)},
			{Contents: fmt.Sprintf("%d/%d", r.Usage.InputTokens, r.Usage.OutputTokens)},
			{Contents: strconv.Itoa(r.Turns)},
			{Contents: ticket},
		})
	}
	return table.Render(os.Stdout, Fly.PrintTableHeaders)
}

func agentStatusColor(status string) *color.Color {
	switch status {
	case "ok":
		return color.New(color.FgGreen)
	case "failed":
		return color.New(color.FgYellow)
	case "parked":
		return color.New(color.FgBlue)
	case "error":
		return color.New(color.FgRed)
	default:
		return nil
	}
}
