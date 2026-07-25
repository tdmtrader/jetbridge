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

type AgentCostsCommand struct {
	GroupBy string `long:"group-by" default:"day" choice:"day" choice:"user" choice:"ticket" choice:"workflow" choice:"model" choice:"step" description:"Rollup dimension"`
	Since   string `long:"since" description:"Start (YYYY-MM-DD or RFC3339); default 30 days ago"`
	Until   string `long:"until" description:"End, exclusive (YYYY-MM-DD or RFC3339)"`
	Json    bool   `long:"json" description:"Print command result as JSON"`
}

func (command *AgentCostsCommand) Execute([]string) error {
	target, err := rc.LoadTarget(Fly.Target, Fly.Verbose)
	if err != nil {
		return err
	}
	if err := target.Validate(); err != nil {
		return err
	}

	resp, err := target.Client().AgentCostRollup(command.GroupBy, command.Since, command.Until)
	if err != nil {
		return err
	}

	if command.Json {
		return displayhelpers.JsonPrint(resp)
	}

	table := ui.Table{Headers: ui.TableRow{
		{Contents: command.GroupBy, Color: color.New(color.Bold)},
		{Contents: "entries", Color: color.New(color.Bold)},
		{Contents: "turns", Color: color.New(color.Bold)},
		{Contents: "cost (usd)", Color: color.New(color.Bold)},
	}}
	for _, row := range resp.Rows {
		table.Data = append(table.Data, ui.TableRow{
			{Contents: row.Key},
			{Contents: strconv.Itoa(row.Entries)},
			{Contents: strconv.FormatInt(row.Turns, 10)},
			{Contents: strconv.FormatFloat(row.CostUSD, 'f', 4, 64)},
		})
	}
	if err := table.Render(os.Stdout, Fly.PrintTableHeaders); err != nil {
		return err
	}
	if resp.Summary.CapUSD > 0 {
		fmt.Printf("daily cap $%.2f, spent today $%.2f, remaining $%.2f\n",
			resp.Summary.CapUSD, resp.Summary.SpentUSD, resp.Summary.RemainingUSD)
	}
	return nil
}
