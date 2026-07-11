package commands

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/fly/commands/internal/flaghelpers"
	"github.com/concourse/concourse/fly/rc"
	"github.com/concourse/concourse/fly/ui"
	"github.com/fatih/color"
)

type RunsCommand struct {
	Pipeline flaghelpers.PipelineFlag `short:"p" long:"pipeline" required:"true" description:"Template pipeline whose runs to list"`
	Team     flaghelpers.TeamFlag     `long:"team" description:"Name of the team, if different from the target default"`
}

func (command *RunsCommand) Execute(args []string) error {
	target, err := rc.LoadTarget(Fly.Target, Fly.Verbose)
	if err != nil {
		return err
	}

	err = target.Validate()
	if err != nil {
		return err
	}

	team, err := command.Team.LoadTeam(target)
	if err != nil {
		return err
	}

	runs, err := team.ListPipelineRuns(command.Pipeline.Ref())
	if err != nil {
		return err
	}

	table := ui.Table{Headers: ui.TableRow{
		{Contents: "number", Color: color.New(color.Bold)},
		{Contents: "status", Color: color.New(color.Bold)},
		{Contents: "params", Color: color.New(color.Bold)},
		{Contents: "duration", Color: color.New(color.Bold)},
	}}

	for _, run := range runs {
		table.Data = append(table.Data, ui.TableRow{
			{Contents: fmt.Sprintf("%d", run.Number)},
			{Contents: run.Status},
			{Contents: formatRunParams(run.Params)},
			{Contents: formatRunDuration(run)},
		})
	}

	return table.Render(os.Stdout, Fly.PrintTableHeaders)
}

func formatRunParams(params map[string]any) string {
	if len(params) == 0 {
		return "n/a"
	}
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, fmt.Sprintf("%s=%v", k, params[k]))
	}
	return strings.Join(pairs, ",")
}

func formatRunDuration(run atc.PipelineRun) string {
	if run.CompletedAt == 0 {
		return "n/a"
	}
	return time.Duration((run.CompletedAt-run.CreatedAt)*int64(time.Second)).String()
}
