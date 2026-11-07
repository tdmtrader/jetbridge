package commands

import (
	"errors"
	"fmt"
	"strings"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/fly/commands/internal/flaghelpers"
	"github.com/concourse/concourse/fly/commands/internal/interaction"
	"github.com/concourse/concourse/fly/rc"
	"github.com/concourse/concourse/fly/ui"
	"github.com/concourse/concourse/go-concourse/concourse"
)

type DestroyPipelineCommand struct {
	Pipeline        flaghelpers.PipelineFlag `short:"p"  long:"pipeline" required:"true" description:"Pipeline to destroy"`
	SkipInteractive bool                     `short:"n"  long:"non-interactive"          description:"Destroy the pipeline without confirmation"`
	PurgeRunHistory bool                     `long:"purge-run-history"                   description:"Destroy a template that has Runs together with every Run of it and their durable history (builds, logs, results, captured outputs, bound inputs and input uploads, credential handoffs, cancellation records). Cannot be undone. Asks you to type the pipeline name to confirm"`
	Team            flaghelpers.TeamFlag     `long:"team" description:"Name of the team to which the pipeline belongs, if different from the target default"`
}

func (command *DestroyPipelineCommand) Validate() error {
	return validatePipelineFlag(&command.Pipeline)
}

func (command *DestroyPipelineCommand) Execute(args []string) error {
	err := command.Validate()
	if err != nil {
		return err

	}
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

	pipelineRef := command.Pipeline.Ref()

	pipeline, found, err := team.Pipeline(pipelineRef)
	if err != nil {
		return err
	}

	if !found {
		fmt.Printf("`%s` does not exist\n", pipelineRef.String())
		return nil
	}

	if command.PurgeRunHistory {
		return command.purge(team, pipeline, pipelineRef)
	}

	fmt.Printf("!!! this will remove all data for pipeline `%s`\n\n", pipelineRef.String())

	if !command.SkipInteractive {
		confirm, err := interaction.Confirm("are you sure?")
		if err != nil || !confirm {
			fmt.Println("bailing out")
			return err
		}
	}

	_, err = team.DeletePipeline(pipelineRef)
	if err != nil {
		if strings.Contains(err.Error(), atc.ErrPipelineTemplateHasRunHistory.Error()) {
			return fmt.Errorf("`%s` is a template with durable Run history, so it was not destroyed.\n"+
				"--purge-run-history also permanently deletes every Run of it and all their durable history;\n"+
				"archive-pipeline keeps it instead", pipelineRef.String())
		}
		return err
	}

	fmt.Printf("`%s` deleted\n", pipelineRef.String())

	return nil
}

func (command *DestroyPipelineCommand) purge(team concourse.Team, pipeline atc.Pipeline, pipelineRef atc.PipelineRef) error {
	name := pipelineRef.String()

	warn := func(format string, a ...any) {
		fmt.Println(ui.WarningColor("!!! "+format, a...))
	}
	warn("this will PERMANENTLY DELETE pipeline `%s` AND EVERY RUN OF IT, with all their durable history:", name)
	warn("  - each Run's builds and build logs")
	warn("  - its results and captured outputs")
	warn("  - its bound inputs and input uploads (their Hangar claims are released, so the stored trees become reclaimable)")
	warn("  - its credential handoffs and cancellation records")
	warn("  - landing queue entries that used these Runs keep their outcome but lose the link to them")
	if runs, ok := command.countRuns(team, pipeline); ok {
		warn("`%s` has %s.", name, runs)
	}
	warn("this cannot be undone. it is not archive-pipeline, which keeps the pipeline and its history.")
	fmt.Println()

	if !command.SkipInteractive {
		confirm, err := interaction.Input("type the pipeline name to confirm", false)
		if err != nil {
			return err
		}
		if confirm != name {
			return errors.New("incorrect pipeline name; bailing out")
		}
	}

	found, purged, err := team.DeletePipelineAndRunHistory(pipelineRef)
	if err != nil {
		return err
	}
	if !found {
		fmt.Printf("`%s` does not exist\n", name)
		return nil
	}

	fmt.Printf("`%s` deleted; %s purged\n", name, pluralRuns(purged))

	return nil
}

// countRuns says how many Runs a template has, from one page of the Runs
// listing `fly runs` reads. A template with more than one page says "at least"
// rather than paging through all of them; a count it cannot learn is left out.
func (command *DestroyPipelineCommand) countRuns(team concourse.Team, pipeline atc.Pipeline) (string, bool) {
	if pipeline.Template == nil || !*pipeline.Template {
		return "no Runs: it is not a template", true
	}
	runs, pagination, err := team.PipelineRuns(pipeline.Name, concourse.Page{Limit: atc.PaginationAPIMaxLimit})
	if err != nil {
		return "", false
	}
	if pagination.Next != nil {
		return fmt.Sprintf("at least %d Runs", len(runs)), true
	}
	return pluralRuns(len(runs)), true
}

func pluralRuns(n int) string {
	if n == 1 {
		return "1 Run"
	}
	return fmt.Sprintf("%d Runs", n)
}
