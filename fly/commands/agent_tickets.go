package commands

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/concourse/concourse/agent/api/outcomes"
	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/dispatch"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/fly/eventstream"
	"github.com/concourse/concourse/fly/rc"
	"github.com/concourse/concourse/fly/ui"
	"github.com/concourse/concourse/go-concourse/concourse"
	"github.com/fatih/color"
)

type AgentTicketsCommand struct {
	List       AgentTicketsListCommand       `command:"list" description:"List agent tickets"`
	Create     AgentTicketsCreateCommand     `command:"create" description:"File a new agent ticket (state: draft)"`
	Show       AgentTicketsShowCommand       `command:"show" description:"Show one ticket with its spec and plan"`
	Queue      AgentTicketsQueueCommand      `command:"queue" description:"Queue a draft ticket for dispatch"`
	Transition AgentTicketsTransitionCommand `command:"transition" description:"Move a ticket along the lifecycle (single-writer state machine)"`
	Dispatch   AgentTicketsDispatchCommand   `command:"dispatch" description:"Dispatch a queued ticket as a pipeline run (manual trigger)"`
	Watch      AgentTicketsWatchCommand      `command:"watch" description:"Follow the build events of a ticket's dispatched run"`
	Close      AgentTicketsCloseCommand      `command:"close" description:"Close a reviewed ticket to a terminal disposition (default: concluded)"`
	Dispose    AgentTicketsDisposeCommand    `command:"dispose" description:"Record a terminal disposition (with reason taxonomy) on a reviewed ticket"`
}

// ticketPipelineName is the deterministic template-pipeline name dispatch
// renders a ticket into (agent/dispatch/dispatch.go). The entry job is
// always "run".
func ticketPipelineName(id int) string { return fmt.Sprintf("agent-ticket-%d", id) }

// printSpecLintWarnings surfaces advisory spec-lint findings (ticket #46:
// vocabulary known to trigger claude CLI usage-policy false refusals) on
// stderr, one "spec-lint:" line each. Purely informational — callers never
// alter their exit code or flow on them.
func printSpecLintWarnings(warnings []string) {
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "spec-lint: %s\n", w)
	}
}

// assignWorkflow realizes the documented "decided at dispatch" semantics
// (WF-5): a ticket created with an empty workflow can have one assigned via
// --workflow / --workflow-version on queue or dispatch, closing the
// empty-workflow dead-end (DispatchOne's ErrNoWorkflow). It is a no-op (and
// returns a no-op restore) when neither flag is set.
//
// The assignment is COMPENSATING so a failed follow-up action never leaves the
// ticket worse than before: it first reads the ticket's current workflow, then
// applies the requested one, and returns restore() that reverts to the prior
// value. Callers invoke restore() when the subsequent dispatch/queue fails, so
// a bad --workflow value (e.g. a typo that DispatchOne cannot resolve) does not
// clobber a previously valid assignment — the ticket is returned to exactly its
// prior workflow, and the user can retry with a correct name. restore() is a
// no-op when nothing was changed.
func assignWorkflow(client concourse.Client, id int, workflow string, workflowVer int) (restore func(), err error) {
	noop := func() {}
	if workflow == "" && workflowVer <= 0 {
		return noop, nil
	}

	// Capture the prior workflow so a failed follow-up can be rolled back.
	prior, found, err := client.GetAgentTicket(id)
	if err != nil {
		return noop, fmt.Errorf("read ticket #%d before assigning workflow: %w", id, err)
	}
	if !found {
		return noop, fmt.Errorf("ticket #%d not found", id)
	}
	priorName := prior.Ticket.WorkflowName
	priorVersion := prior.Ticket.WorkflowVersion

	var req tickets.UpdateRequest
	if workflow != "" {
		req.WorkflowName = &workflow
	}
	if workflowVer > 0 {
		req.WorkflowVersion = &workflowVer
	}
	if _, err := client.UpdateAgentTicket(id, req); err != nil {
		return noop, fmt.Errorf("assign workflow to #%d failed: %w", id, err)
	}
	switch {
	case workflow != "" && workflowVer > 0:
		fmt.Printf("assigned workflow %q (version %d) to ticket #%d\n", workflow, workflowVer, id)
	case workflow != "":
		fmt.Printf("assigned workflow %q to ticket #%d\n", workflow, id)
	default:
		fmt.Printf("pinned workflow version %d on ticket #%d\n", workflowVer, id)
	}

	restore = func() {
		// Revert only the fields we changed. WorkflowName is always a settable
		// string (an empty prior restores the un-assigned state). A pin we added
		// can be reverted to a concrete prior pin; a prior "live" (nil) pin cannot
		// be re-expressed through the partial UpdateRequest, so a pin we added on
		// top of a live prior is left in place — inert without a resolvable name.
		var rb tickets.UpdateRequest
		if req.WorkflowName != nil {
			rb.WorkflowName = &priorName
		}
		if req.WorkflowVersion != nil && priorVersion != nil {
			rb.WorkflowVersion = priorVersion
		}
		if rb.WorkflowName == nil && rb.WorkflowVersion == nil {
			return
		}
		if _, err := client.UpdateAgentTicket(id, rb); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not restore ticket #%d workflow after a failed action: %v\n", id, err)
		}
	}
	return restore, nil
}

type AgentTicketsListCommand struct {
	State  string `long:"state" description:"Filter by lifecycle state (draft, queued, running, needs_review, merged, merged_with_fixes, sent_back, abandoned, concluded, failed, errored)"`
	Repo   string `long:"repo" description:"Filter by repo slug (e.g. tdmtrader/concourse)"`
	Origin string `long:"origin" description:"Filter by origin (web, fly, jira, retrospective)"`
	Limit  int    `long:"limit" default:"50" description:"Maximum tickets to list"`
}

func (command *AgentTicketsListCommand) Execute([]string) error {
	target, err := rc.LoadTarget(Fly.Target, Fly.Verbose)
	if err != nil {
		return err
	}
	if err := target.Validate(); err != nil {
		return err
	}

	list, err := target.Client().ListAgentTickets(tickets.ListFilter{
		State:  tickets.State(command.State),
		Repo:   command.Repo,
		Origin: command.Origin,
		Limit:  command.Limit,
	})
	if err != nil {
		return err
	}

	table := ui.Table{Headers: ui.TableRow{
		{Contents: "id", Color: color.New(color.Bold)},
		{Contents: "state", Color: color.New(color.Bold)},
		{Contents: "repo", Color: color.New(color.Bold)},
		{Contents: "title", Color: color.New(color.Bold)},
		{Contents: "user", Color: color.New(color.Bold)},
		{Contents: "run", Color: color.New(color.Bold)},
	}}
	for _, t := range list {
		run := ""
		if t.PipelineRunID != nil {
			run = ticketPipelineName(t.ID)
		}
		table.Data = append(table.Data, ui.TableRow{
			{Contents: strconv.Itoa(t.ID)},
			{Contents: string(t.State)},
			{Contents: t.Repo},
			{Contents: t.Title},
			{Contents: t.UserName},
			{Contents: run},
		})
	}
	return table.Render(os.Stdout, Fly.PrintTableHeaders)
}

type AgentTicketsCreateCommand struct {
	Title        string  `long:"title" required:"true" description:"Ticket title"`
	Body         string  `long:"body" short:"m" description:"Markdown problem statement"`
	Repo         string  `long:"repo" required:"true" description:"Target repo slug (e.g. tdmtrader/concourse)"`
	TargetBranch string  `long:"target-branch" default:"main" description:"Branch the work targets"`
	Workflow     string  `long:"workflow" description:"Workflow definition name (empty = decided at dispatch)"`
	WorkflowVer  int     `long:"workflow-version" description:"Pin a workflow definition version (0 = live version)"`
	Budget       float64 `long:"budget" description:"Per-ticket budget in USD (0 = workflow default)"`
	Queue        bool    `long:"queue" description:"Queue the ticket immediately after creating it"`
	Dispatch     bool    `long:"dispatch" description:"Queue and dispatch the ticket immediately (implies --queue)"`
}

func (command *AgentTicketsCreateCommand) Execute([]string) error {
	target, err := rc.LoadTarget(Fly.Target, Fly.Verbose)
	if err != nil {
		return err
	}
	if err := target.Validate(); err != nil {
		return err
	}

	req := tickets.CreateRequest{
		Title:        command.Title,
		Body:         command.Body,
		Origin:       "fly",
		Repo:         command.Repo,
		TargetBranch: command.TargetBranch,
		WorkflowName: command.Workflow,
	}
	if command.WorkflowVer > 0 {
		req.WorkflowVersion = &command.WorkflowVer
	}
	if command.Budget > 0 {
		req.BudgetUSD = &command.Budget
	}

	client := target.Client()
	created, err := client.CreateAgentTicket(req)
	if err != nil {
		return err
	}
	fmt.Printf("created ticket #%d (%s)\n", created.ID, created.State)

	// Each follow-up is an independent server-validated call; on failure
	// we report which step failed and stop, so the user resumes with the
	// single-purpose queue/dispatch commands rather than guessing state.
	if command.Queue || command.Dispatch {
		queued, err := client.TransitionAgentTicket(created.ID, tickets.TransitionRequest{
			From: tickets.StateDraft, To: tickets.StateQueued,
		})
		if err != nil {
			return fmt.Errorf("created #%d (draft); queue failed: %w", created.ID, err)
		}
		fmt.Printf("ticket #%d is now %s\n", queued.ID, queued.State)
	}
	if command.Dispatch {
		res, err := client.DispatchAgentTicket(created.ID)
		if err != nil {
			return fmt.Errorf("created #%d (queued); dispatch failed: %w", created.ID, err)
		}
		fmt.Printf("dispatched ticket #%d as run %d (pipeline %s)\n", created.ID, res.RunID, res.PipelineName)
	}
	return nil
}

type AgentTicketsQueueCommand struct {
	ID          int    `long:"id" required:"true" description:"Ticket id"`
	Workflow    string `long:"workflow" description:"Assign this workflow before queueing (fixes an empty-workflow ticket)"`
	WorkflowVer int    `long:"workflow-version" description:"Pin a specific workflow definition version (omit to use the live version)"`
}

func (command *AgentTicketsQueueCommand) Execute([]string) error {
	target, err := rc.LoadTarget(Fly.Target, Fly.Verbose)
	if err != nil {
		return err
	}
	if err := target.Validate(); err != nil {
		return err
	}
	client := target.Client()

	restore, err := assignWorkflow(client, command.ID, command.Workflow, command.WorkflowVer)
	if err != nil {
		return err
	}

	updated, err := client.TransitionAgentTicket(command.ID, tickets.TransitionRequest{
		From: tickets.StateDraft, To: tickets.StateQueued,
	})
	if err != nil {
		// Roll back a workflow we just assigned so a failed queue transition
		// never leaves the ticket worse than before (WF-5).
		restore()
		return err
	}
	fmt.Printf("ticket #%d is now %s\n", updated.ID, updated.State)
	// Client-side advisory lint at queue time (ticket #46): the queue
	// transition response carries the ticket prose, so warn here before
	// any dollars are spent. Never affects the exit code.
	printSpecLintWarnings(dispatch.SpecLint(updated.Title, updated.Body))
	return nil
}

type AgentTicketsTransitionCommand struct {
	ID          int    `long:"id" required:"true" description:"Ticket id"`
	From        string `long:"from" description:"Expected current state (optimistic concurrency guard; default: read from server)"`
	To          string `long:"to" required:"true" description:"Target state"`
	Branch      string `long:"branch" description:"Branch to record (needs_review transitions)"`
	ErrorDetail string `long:"error-detail" description:"Error detail to record (errored transitions)"`
}

func (command *AgentTicketsTransitionCommand) Execute([]string) error {
	target, err := rc.LoadTarget(Fly.Target, Fly.Verbose)
	if err != nil {
		return err
	}
	if err := target.Validate(); err != nil {
		return err
	}
	client := target.Client()

	from := tickets.State(command.From)
	if from == "" {
		// Convenience read, NOT a client-side guess: whatever we read is
		// still sent as `from` and the server's from-guard remains the sole
		// authority. If state moves between this read and the write, the
		// existing 409 fires exactly as with an explicit --from.
		detail, found, err := client.GetAgentTicket(command.ID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("ticket %d not found", command.ID)
		}
		from = detail.Ticket.State
	}

	updated, err := client.TransitionAgentTicket(command.ID, tickets.TransitionRequest{
		From:        from,
		To:          tickets.State(command.To),
		Branch:      command.Branch,
		ErrorDetail: command.ErrorDetail,
	})
	if err != nil {
		return err
	}
	fmt.Printf("ticket #%d is now %s\n", updated.ID, updated.State)
	return nil
}

type AgentTicketsDispatchCommand struct {
	ID          int    `long:"id" required:"true" description:"Ticket id (must be queued)"`
	Workflow    string `long:"workflow" description:"Assign this workflow before dispatch (fixes an empty-workflow ticket)"`
	WorkflowVer int    `long:"workflow-version" description:"Pin a specific workflow definition version (omit to use the live version)"`
}

func (command *AgentTicketsDispatchCommand) Execute([]string) error {
	target, err := rc.LoadTarget(Fly.Target, Fly.Verbose)
	if err != nil {
		return err
	}
	if err := target.Validate(); err != nil {
		return err
	}
	client := target.Client()

	restore, err := assignWorkflow(client, command.ID, command.Workflow, command.WorkflowVer)
	if err != nil {
		return err
	}

	res, err := client.DispatchAgentTicket(command.ID)
	if err != nil {
		// A workflow we just assigned must not outlive a failed dispatch (e.g. a
		// typo the server cannot resolve): roll it back so the ticket is never
		// left worse than before (WF-5).
		restore()
		// Actionable hint for the empty-workflow dead-end: the ticket names no
		// workflow and none was supplied here (WF-5). Never rewrites other errors.
		if command.Workflow == "" && strings.Contains(err.Error(), "no workflow_name") {
			return fmt.Errorf("%w (assign one with: fly agent tickets dispatch --id %d --workflow <name>)", err, command.ID)
		}
		return err
	}
	fmt.Printf("dispatched ticket #%d as run %d (pipeline %s)\n", command.ID, res.RunID, res.PipelineName)
	// Server-computed advisory lint (ticket #46) rides the dispatch
	// response; surface it without touching the exit code.
	printSpecLintWarnings(res.Warnings)
	return nil
}

type AgentTicketsShowCommand struct {
	ID int `long:"id" required:"true" description:"Ticket id"`
}

func (command *AgentTicketsShowCommand) Execute([]string) error {
	target, err := rc.LoadTarget(Fly.Target, Fly.Verbose)
	if err != nil {
		return err
	}
	if err := target.Validate(); err != nil {
		return err
	}

	detail, found, err := target.Client().GetAgentTicket(command.ID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("ticket %d not found", command.ID)
	}

	t := detail.Ticket
	fmt.Printf("ticket #%d: %s\n", t.ID, t.Title)
	fmt.Printf("state: %s · origin: %s · repo: %s @ %s\n", t.State, t.Origin, t.Repo, t.TargetBranch)
	if t.BudgetUSD != nil {
		fmt.Printf("budget: $%.2f\n", *t.BudgetUSD)
	}
	if t.Branch != "" {
		fmt.Printf("branch: %s\n", t.Branch)
	}
	if t.PipelineRunID != nil {
		// The run is a deterministic function of the ticket id; surface it
		// so the user can reach the build without cluster access.
		fmt.Printf("run: %s (run %d) · follow with: fly -t %s agent tickets watch --id %d\n",
			ticketPipelineName(t.ID), *t.PipelineRunID, Fly.Target, t.ID)
	}
	if t.Body != "" {
		fmt.Printf("\n%s\n", t.Body)
	}
	if detail.Spec != nil {
		fmt.Printf("\nspec v%d: %s\n", detail.Spec.Version, detail.Spec.Title)
	}
	if len(detail.Tasks) > 0 {
		fmt.Println("\nplan:")
		for _, task := range detail.Tasks {
			fmt.Printf("  %d. [%s] %s\n", task.Ordering, task.Status, task.Title)
		}
	}
	return nil
}

type AgentTicketsWatchCommand struct {
	ID        int  `long:"id" required:"true" description:"Ticket id"`
	Timestamp bool `long:"timestamps" description:"Print with local timestamps"`
}

// Execute follows the dispatched run's build events. Dispatch renders the
// ticket into a TEMPLATE pipeline agent-ticket-<id> and CreateRun
// materializes a per-run INSTANCE of it (instance var run:<n>); the entry
// job "run" builds on that instance, never on the bare template. The
// ticket row carries the global pipeline_runs.id, not the per-template
// instance number, so we resolve the latest matching build by scanning
// the team's builds (newest-first) for pipeline agent-ticket-<id>.
func (command *AgentTicketsWatchCommand) Execute([]string) error {
	target, err := rc.LoadTarget(Fly.Target, Fly.Verbose)
	if err != nil {
		return err
	}
	if err := target.Validate(); err != nil {
		return err
	}

	client := target.Client()
	team := client.Team(atc.DefaultTeamName)
	pipelineName := ticketPipelineName(command.ID)

	buildID := 0
	page := concourse.Page{Limit: 100}
	for buildID == 0 {
		builds, pagination, err := team.Builds(page)
		if err != nil {
			return err
		}
		for _, b := range builds {
			if b.PipelineName == pipelineName && b.JobName == "run" {
				buildID = b.ID
				break
			}
		}
		if buildID != 0 || pagination.Next == nil {
			break
		}
		page = *pagination.Next
	}
	if buildID == 0 {
		return fmt.Errorf("no dispatched run found for ticket %d (pipeline %s) — has it been dispatched?", command.ID, pipelineName)
	}

	eventSource, err := client.BuildEvents(strconv.Itoa(buildID))
	if err != nil {
		return err
	}
	exitCode := eventstream.Render(os.Stdout, eventSource, eventstream.RenderOptions{
		ShowTimestamp: command.Timestamp,
	})
	eventSource.Close()
	os.Exit(exitCode)
	return nil
}

type AgentTicketsCloseCommand struct {
	ID          int    `long:"id" required:"true" description:"Ticket id"`
	Disposition string `long:"disposition" default:"concluded" description:"Terminal disposition from needs_review (concluded, merged, merged_with_fixes, sent_back, abandoned)"`
	Branch      string `long:"branch" description:"Branch to record on the needs_review hop"`
	Reason      string `long:"reason" choice:"wrong_approach" choice:"incomplete" choice:"defective" choice:"superseded" choice:"not_needed" choice:"style" choice:"research_complete" choice:"other" description:"Reason taxonomy (§1.11) for sent_back/abandoned/concluded (default: research_complete for concluded, other otherwise)"`
	Notes       string `long:"notes" description:"Free-text notes for sent_back/abandoned/concluded"`
}

// Execute closes a reviewed ticket to a terminal state. A running ticket
// is walked running→needs_review→<disposition>; a ticket already in
// needs_review skips the first hop. It re-reads state between hops rather
// than assuming success, because running→needs_review has two writers
// (harvest and the run-completion reconciler, contracts §2.1) — the
// server's from-guard stays the sole authority, so a concurrent mover
// surfaces as a 409, not a silent overwrite.
//
// The terminal hop for sent_back/abandoned/concluded goes through the
// disposition route (§1.11.1 writer reconciliation, D-3) so the outcome
// row records the taxonomy/notes/disposed_by attribution; the server
// walks the lifecycle edge itself. merged/merged_with_fixes stay raw
// transitions — the manual override remains legal and the outcome
// watcher independently detects the real git merge.
func (command *AgentTicketsCloseCommand) Execute([]string) error {
	target, err := rc.LoadTarget(Fly.Target, Fly.Verbose)
	if err != nil {
		return err
	}
	if err := target.Validate(); err != nil {
		return err
	}
	client := target.Client()

	detail, found, err := client.GetAgentTicket(command.ID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("ticket %d not found", command.ID)
	}
	state := detail.Ticket.State

	if state == tickets.StateRunning {
		// The first hop is not a disposition; it stays a raw transition.
		updated, err := client.TransitionAgentTicket(command.ID, tickets.TransitionRequest{
			From: tickets.StateRunning, To: tickets.StateNeedsReview, Branch: command.Branch,
		})
		if err != nil {
			return err
		}
		fmt.Printf("ticket #%d is now %s\n", updated.ID, updated.State)
		state = updated.State // re-check, do not assume the hop landed us in needs_review
	}

	if state != tickets.StateNeedsReview {
		return fmt.Errorf("ticket %d is %s; close only acts on running or needs_review tickets", command.ID, state)
	}

	if outcomes.ValidDisposition(command.Disposition) {
		reason := command.Reason
		if reason == "" {
			// The server's taxonomy validation stays authoritative; these
			// are just sensible defaults for the flag-less close flow.
			if command.Disposition == outcomes.DispositionConcluded {
				reason = "research_complete"
			} else {
				reason = "other"
			}
		}
		outcome, err := client.SetAgentTicketDisposition(command.ID, outcomes.DispositionRequestFor(
			command.Disposition, reason, command.Notes,
		))
		if err != nil {
			return err
		}
		fmt.Printf("ticket #%d is now %s\n", command.ID, outcome.Disposition)
		return nil
	}

	updated, err := client.TransitionAgentTicket(command.ID, tickets.TransitionRequest{
		From: tickets.StateNeedsReview, To: tickets.State(command.Disposition), Branch: command.Branch,
	})
	if err != nil {
		return err
	}
	fmt.Printf("ticket #%d is now %s\n", updated.ID, updated.State)
	return nil
}

type AgentTicketsDisposeCommand struct {
	ID          int    `long:"id" required:"true" description:"Ticket id"`
	Disposition string `long:"disposition" required:"true" choice:"sent_back" choice:"abandoned" choice:"concluded" description:"Terminal disposition"`
	Reason      string `long:"reason" choice:"wrong_approach" choice:"incomplete" choice:"defective" choice:"superseded" choice:"not_needed" choice:"style" choice:"research_complete" choice:"other" description:"Reason taxonomy (§1.11)"`
	Notes       string `long:"notes" description:"Free-text notes"`
}

// Execute records a human's terminal verdict through the disposition
// route: the server walks the needs_review→<disposition> lifecycle edge
// AND stamps the outcome row (taxonomy, notes, disposed_by) in one call.
// Reason validation is the server's; an omitted --reason surfaces its 400.
func (command *AgentTicketsDisposeCommand) Execute([]string) error {
	target, err := rc.LoadTarget(Fly.Target, Fly.Verbose)
	if err != nil {
		return err
	}
	if err := target.Validate(); err != nil {
		return err
	}

	outcome, err := target.Client().SetAgentTicketDisposition(command.ID, outcomes.DispositionRequestFor(
		command.Disposition, command.Reason, command.Notes,
	))
	if err != nil {
		return err
	}
	fmt.Printf("ticket #%d disposed: %s (%s)\n", command.ID, outcome.Disposition, outcome.MergeState)
	return nil
}
