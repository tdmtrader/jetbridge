package commands

import (
	"fmt"
	"os"
	"strconv"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/fly/rc"
	"github.com/concourse/concourse/fly/ui"
	"github.com/fatih/color"
)

type AgentTicketsCommand struct {
	List   AgentTicketsListCommand   `command:"list" description:"List agent tickets"`
	Create AgentTicketsCreateCommand `command:"create" description:"File a new agent ticket (state: draft)"`
	Show   AgentTicketsShowCommand   `command:"show" description:"Show one ticket with its spec and plan"`
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
	}}
	for _, t := range list {
		table.Data = append(table.Data, ui.TableRow{
			{Contents: strconv.Itoa(t.ID)},
			{Contents: string(t.State)},
			{Contents: t.Repo},
			{Contents: t.Title},
			{Contents: t.UserName},
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

	created, err := target.Client().CreateAgentTicket(req)
	if err != nil {
		return err
	}
	fmt.Printf("created ticket #%d (%s)\n", created.ID, created.State)
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
