package commands

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/fly/rc"
	"github.com/concourse/concourse/fly/ui"
	"github.com/concourse/concourse/go-concourse/concourse"
	"github.com/fatih/color"
)

type AgentTicketsCommand struct {
	List       AgentTicketsListCommand       `command:"list" description:"List agent tickets"`
	Create     AgentTicketsCreateCommand     `command:"create" description:"File a new agent ticket (state: draft)"`
	Show       AgentTicketsShowCommand       `command:"show" description:"Show one ticket"`
	Queue      AgentTicketsQueueCommand      `command:"queue" description:"Queue a draft ticket for dispatch"`
	Transition AgentTicketsTransitionCommand `command:"transition" description:"Move a ticket along the queue lifecycle (single-writer state machine)"`
	Dispatch   AgentTicketsDispatchCommand   `command:"dispatch" description:"Dispatch a queued ticket as a durable workflow run (manual trigger)"`
	Watch      AgentTicketsWatchCommand      `command:"watch" description:"Follow a ticket's durable workflow run"`
}

// printDispatchWarnings surfaces the advisory warnings the SERVER returned
// with a dispatch (vocabulary in the ticket's title/body known to trigger
// claude CLI usage-policy false refusals) on stderr, one "work-item-lint:"
// line each. fly does not lint anything itself — the server owns the pattern
// table, so a fly binary can never disagree with the cluster it is talking to.
// Purely informational: callers never alter their exit code or flow on them.
func printDispatchWarnings(warnings []string) {
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "work-item-lint: %s\n", w)
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
	State  string `long:"state" description:"Filter by queue state (draft, queued, running, needs_review, closed)"`
	Repo   string `long:"repo" description:"Filter by repo slug (e.g. tdmtrader/concourse)"`
	Origin string `long:"origin" description:"Filter by origin (web, fly, retrospective)"`
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
		{Contents: "workflow run", Color: color.New(color.Bold)},
	}}
	for _, t := range list {
		table.Data = append(table.Data, ui.TableRow{
			{Contents: strconv.Itoa(t.ID)},
			{Contents: string(t.State)},
			{Contents: t.Repo},
			{Contents: t.Title},
			{Contents: t.UserName},
			{Contents: agentTicketWorkflowRunCell(t)},
		})
	}
	return table.Render(os.Stdout, Fly.PrintTableHeaders)
}

func agentTicketWorkflowRunCell(ticket tickets.Ticket) string {
	if ticket.WorkflowRunID == nil {
		return ""
	}
	return ticket.WorkflowRunID.String()
}

type AgentTicketsCreateCommand struct {
	Title        string `long:"title" required:"true" description:"Ticket title"`
	Body         string `long:"body" short:"m" description:"Markdown problem statement"`
	Repo         string `long:"repo" required:"true" description:"Target repo slug (e.g. tdmtrader/concourse)"`
	TargetBranch string `long:"target-branch" default:"main" description:"Branch the work targets"`
	Workflow     string `long:"workflow" description:"Workflow definition name (empty = decided at dispatch)"`
	WorkflowVer  int    `long:"workflow-version" description:"Pin a workflow definition version (0 = live version)"`
	Queue        bool   `long:"queue" description:"Queue the ticket immediately after creating it"`
	Dispatch     bool   `long:"dispatch" description:"Queue and dispatch the ticket immediately (implies --queue)"`
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
		line, err := agentTicketDispatchLine(created.ID, res)
		if err != nil {
			return fmt.Errorf("created #%d (queued); dispatch failed: %w", created.ID, err)
		}
		fmt.Println(line)
		printDispatchWarnings(res.Warnings)
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
	return nil
}

type AgentTicketsTransitionCommand struct {
	ID   int    `long:"id" required:"true" description:"Ticket id"`
	From string `long:"from" description:"Expected current state (optimistic concurrency guard; default: read from server)"`
	To   string `long:"to" required:"true" description:"Target queue state (draft, queued, running, needs_review, closed)"`
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
		From: from,
		To:   tickets.State(command.To),
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
	line, err := agentTicketDispatchLine(command.ID, res)
	if err != nil {
		return err
	}
	fmt.Println(line)
	// Advisory lint the SERVER computed rides the dispatch response; surface
	// it without touching the exit code.
	printDispatchWarnings(res.Warnings)
	return nil
}

// agentTicketDispatchLine reports the dispatch by its identity — the workflow
// run. The pipeline run is a diagnostic and may be absent, so it is appended
// only when the server sent one.
func agentTicketDispatchLine(ticketID int, response tickets.DispatchResponse) (string, error) {
	if response.WorkflowRunID.Validate() != nil {
		return "", fmt.Errorf("dispatch response for ticket %d omitted workflow_run_id", ticketID)
	}
	line := fmt.Sprintf(
		"dispatched ticket #%d as workflow run %s",
		ticketID,
		response.WorkflowRunID.String(),
	)
	if response.PipelineRunID != nil {
		line += fmt.Sprintf(" (pipeline run %d)", *response.PipelineRunID)
	}
	return line, nil
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
	for _, line := range agentTicketRunLines(t, string(Fly.Target)) {
		fmt.Println(line)
	}
	if t.Body != "" {
		fmt.Printf("\n%s\n", t.Body)
	}
	return nil
}

func agentTicketRunLines(ticket tickets.Ticket, target string) []string {
	lines := make([]string, 0, 2)
	if ticket.WorkflowRunID != nil {
		lines = append(lines, fmt.Sprintf(
			"workflow run: %s · inspect with: fly -t %s agent workflows show-run %s %s",
			ticket.WorkflowRunID.String(),
			target,
			ticket.WorkflowName,
			ticket.WorkflowRunID.String(),
		))
	}
	if ticket.PipelineRunID != nil {
		lines = append(lines, fmt.Sprintf("pipeline run: %d", *ticket.PipelineRunID))
	}
	return lines
}

type AgentTicketsWatchCommand struct {
	ID int `long:"id" required:"true" description:"Ticket id"`
}

func (command *AgentTicketsWatchCommand) Execute([]string) error {
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
	showRun, err := agentTicketWatchShowRunCommand(detail.Ticket)
	if err != nil {
		return err
	}
	prepared, err := showRun.prepare()
	if err != nil {
		return err
	}
	return showRun.executePreparedWithTarget(target, prepared)
}

func agentTicketWatchShowRunCommand(ticket tickets.Ticket) (*WorkflowsShowRunCommand, error) {
	if ticket.WorkflowRunID == nil {
		return nil, fmt.Errorf("ticket %d has no workflow run", ticket.ID)
	}
	if ticket.WorkflowName == "" {
		return nil, fmt.Errorf(
			"ticket %d has workflow run %s but no workflow name",
			ticket.ID,
			ticket.WorkflowRunID.String(),
		)
	}
	var command WorkflowsShowRunCommand
	command.Args.Workflow = ticket.WorkflowName
	command.Args.RunID = ticket.WorkflowRunID.String()
	command.Follow = true
	return &command, nil
}
