package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/fly/commands/internal/displayhelpers"
	"github.com/concourse/concourse/fly/commands/internal/flaghelpers"
	"github.com/concourse/concourse/fly/rc"
	"github.com/concourse/concourse/fly/ui"
	"github.com/concourse/concourse/go-concourse/concourse"
	"github.com/fatih/color"
)

type pipelineRunLister interface {
	PipelineRuns(string, concourse.Page) ([]atc.PipelineRun, concourse.Pagination, error)
}

type RunsCommand struct {
	Count    int                      `short:"c" long:"count" default:"50" description:"Number of runs to return"`
	Json     bool                     `long:"json" description:"Print command result as JSON"`
	Pipeline flaghelpers.PipelineFlag `short:"p" long:"pipeline" required:"true" description:"Name of the template pipeline"`
	Team     flaghelpers.TeamFlag     `long:"team" description:"Name of the team to which the pipeline belongs, if different from the target default"`

	now func() time.Time
}

func (command *RunsCommand) Execute([]string) error {
	if err := command.validate(); err != nil {
		return err
	}

	target, err := rc.LoadTarget(Fly.Target, Fly.Verbose)
	if err != nil {
		return err
	}
	if err := target.Validate(); err != nil {
		return err
	}

	team, err := command.Team.LoadTeam(target)
	if err != nil {
		return err
	}
	return command.run(team, os.Stdout)
}

func (command *RunsCommand) run(client pipelineRunLister, output io.Writer) error {
	if err := command.validate(); err != nil {
		return err
	}

	runs, err := command.list(client)
	if err != nil {
		return err
	}

	// The table cannot show ids, timestamps, config hash or reclaimed state, so
	// JSON is the only path a script has to the run record.
	if command.Json {
		return displayhelpers.JsonPrint(runs)
	}

	table := ui.Table{Headers: ui.TableRow{
		{Contents: "number", Color: color.New(color.Bold)},
		{Contents: "params", Color: color.New(color.Bold)},
		{Contents: "status", Color: color.New(color.Bold)},
		{Contents: "start", Color: color.New(color.Bold)},
		{Contents: "duration", Color: color.New(color.Bold)},
	}}
	for _, run := range runs {
		table.Data = append(table.Data, ui.TableRow{
			{Contents: strconv.Itoa(run.Number)},
			{Contents: pipelineRunParams(run.Params)},
			{Contents: string(run.Status)},
			{Contents: pipelineRunStart(run.CreatedAt)},
			{Contents: command.pipelineRunDuration(run)},
		})
	}

	return table.Render(output, Fly.PrintTableHeaders)
}

// list collects the requested count a page at a time. The server clamps a
// caller-supplied limit to atc.PaginationAPIMaxLimit without saying so, so
// sending -c straight through returned a short list that looked complete. The
// user asked for N rows and the keyset cursors exist for exactly this, so
// follow Pagination.Next rather than capping locally.
func (command *RunsCommand) list(client pipelineRunLister) ([]atc.PipelineRun, error) {
	var runs []atc.PipelineRun
	page := concourse.Page{Limit: min(command.Count, atc.PaginationAPIMaxLimit)}
	for {
		batch, pagination, err := client.PipelineRuns(command.Pipeline.Name, page)
		if err != nil {
			return nil, err
		}
		runs = append(runs, batch...)

		remaining := command.Count - len(runs)
		if remaining <= 0 || len(batch) == 0 || pagination.Next == nil {
			break
		}
		page = *pagination.Next
		page.Limit = min(remaining, atc.PaginationAPIMaxLimit)
	}
	// A server that answered a larger page than asked for must not turn -c
	// into a floor.
	if len(runs) > command.Count {
		runs = runs[:command.Count]
	}
	return runs, nil
}

func (command *RunsCommand) validate() error {
	if command.Count <= 0 {
		return fmt.Errorf("count must be positive")
	}
	if len(command.Pipeline.InstanceVars) > 0 {
		return fmt.Errorf("cannot list runs for an instanced pipeline")
	}
	_, err := command.Pipeline.Validate()
	return err
}

func pipelineRunParams(params *atc.Params) string {
	if params == nil || len(*params) == 0 {
		return "n/a"
	}

	fields := make([]string, 0, len(*params))
	for name, value := range *params {
		fields = append(fields, fmt.Sprintf("%s:%s", name, pipelineRunParamValue(value)))
	}
	sort.Strings(fields)
	return strings.Join(fields, ",")
}

// pipelineRunParamValue renders a scalar the way it was supplied. Params decode
// out of the run record as float64, and %v prints float64(1e6) as "1e+06", so a
// number outside ~1e-5..1e21 was unreadable in the only column that shows it.
func pipelineRunParamValue(value any) string {
	switch typed := value.(type) {
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case json.Number:
		return typed.String()
	default:
		return fmt.Sprintf("%v", value)
	}
}

func pipelineRunStart(start time.Time) string {
	if start.IsZero() {
		return "n/a"
	}
	return start.Format(timeDateLayout)
}

func (command *RunsCommand) pipelineRunDuration(run atc.PipelineRun) string {
	if run.CreatedAt.IsZero() {
		return "n/a"
	}
	if run.CompletedAt != nil {
		return run.CompletedAt.Sub(run.CreatedAt).String()
	}
	now := time.Now
	if command.now != nil {
		now = command.now
	}
	return roundSecondsOffDuration(now().Sub(run.CreatedAt)).String() + "+"
}
