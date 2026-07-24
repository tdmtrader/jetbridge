package commands

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/fly/commands/internal/interaction"
	"github.com/concourse/concourse/fly/rc"
	"github.com/concourse/concourse/go-concourse/concourse"
)

// legacyTicketPipelineName matches ONLY the retired per-ticket dispatch
// naming convention agent-ticket-<id> with a positive decimal id. It is
// deliberately strict: agent-ticket-0, agent-ticket-<non-numeric>, and the
// v3 agent-workflow-* pipelines never match, so they can never be selected
// for archival.
var legacyTicketPipelineName = regexp.MustCompile(`^agent-ticket-[1-9][0-9]*$`)

// AgentCleanupLegacyPipelinesCommand is a one-time, main-team-only operational
// command that archives the retired agent-ticket-<id> pipelines left behind by
// the removed ticket-named pipeline lifecycle (v3-only cleanup). It defaults to
// a dry run; --apply performs the archival through the ordinary pipeline API
// (the same db.Pipeline.Archive that fly archive-pipeline drives).
type AgentCleanupLegacyPipelinesCommand struct {
	Apply           bool `long:"apply" description:"Archive the legacy pipelines (default is a dry run that only lists them)"`
	SkipInteractive bool `short:"n" long:"non-interactive" description:"With --apply, skip the confirmation prompt"`
}

func (command *AgentCleanupLegacyPipelinesCommand) Execute([]string) error {
	target, err := rc.LoadTarget(Fly.Target, Fly.Verbose)
	if err != nil {
		return err
	}
	if err := target.Validate(); err != nil {
		return err
	}
	return cleanupLegacyPipelines(target.Team(), command.Apply, command.SkipInteractive, os.Stdout, interaction.Confirm)
}

// cleanupLegacyPipelines is the testable core. It refuses to run against any
// team other than main (BEFORE any listing or mutation), enumerates the exact
// unarchived agent-ticket-<id> targets in lexical order, prints every one
// BEFORE any mutation, and only then — in --apply mode, after confirmation —
// archives exactly those already-printed refs.
func cleanupLegacyPipelines(
	team concourse.Team,
	apply, skipInteractive bool,
	out io.Writer,
	confirm func(prompt string) (bool, error),
) error {
	// Main-team-only, checked before listing or mutating anything: dispatch
	// only ever created per-ticket pipelines on the main team, and an unpinned
	// name match could archive another team's identically-named pipeline.
	if team.Name() != atc.DefaultTeamName {
		return fmt.Errorf(
			"cleanup-legacy-pipelines must be run against the %q team, but the target team is %q",
			atc.DefaultTeamName, team.Name())
	}

	pipelines, err := team.ListPipelines()
	if err != nil {
		return err
	}

	var targets []atc.PipelineRef
	for _, p := range pipelines {
		if p.Archived || !legacyTicketPipelineName.MatchString(p.Name) {
			continue
		}
		targets = append(targets, p.Ref())
	}
	sort.Slice(targets, func(i, j int) bool {
		return targets[i].String() < targets[j].String()
	})

	if len(targets) == 0 {
		fmt.Fprintln(out, "no legacy agent-ticket-<id> pipelines to archive")
		return nil
	}

	fmt.Fprintf(out, "legacy agent-ticket-<id> pipelines on the %s team (%d):\n", atc.DefaultTeamName, len(targets))
	for _, ref := range targets {
		fmt.Fprintf(out, "  %s\n", ref.String())
	}

	if !apply {
		fmt.Fprintf(out,
			"\ndry run: nothing was archived. Re-run with --apply to archive the %d pipeline(s) listed above.\n",
			len(targets))
		return nil
	}

	if !skipInteractive {
		confirmed, err := confirm(fmt.Sprintf("archive the %d legacy pipeline(s) listed above?", len(targets)))
		if err != nil {
			return err
		}
		if !confirmed {
			fmt.Fprintln(out, "bailing out")
			return nil
		}
	}

	for _, ref := range targets {
		found, err := team.ArchivePipeline(ref)
		if err != nil {
			return err
		}
		if !found {
			fmt.Fprintf(out, "pipeline '%s' not found (already gone)\n", ref.String())
			continue
		}
		fmt.Fprintf(out, "archived '%s'\n", ref.String())
	}
	return nil
}
