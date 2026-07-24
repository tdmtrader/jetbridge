package commands

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/api/principals"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/fly/commands/internal/displayhelpers"
	"github.com/concourse/concourse/fly/ui"
	"github.com/concourse/concourse/go-concourse/concourse"
	"github.com/fatih/color"
)

// AgentPrincipalsCommand groups the admin-only principal-management
// subcommands. Principals are minted by admins and carry a closed set of
// scopes; the raw token is shown exactly once, at mint time.
type AgentPrincipalsCommand struct {
	List   AgentPrincipalsListCommand   `command:"list" alias:"ls" description:"List agent principals (active and revoked)"`
	Mint   AgentPrincipalsMintCommand   `command:"mint" alias:"create" description:"Mint a new agent principal and print its one-time token"`
	Revoke AgentPrincipalsRevokeCommand `command:"revoke" description:"Revoke an agent principal"`
}

type AgentPrincipalsListCommand struct {
	Json bool `long:"json" description:"Print command result as JSON"`
}

func (command *AgentPrincipalsListCommand) Execute([]string) error {
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}

	list, err := target.Client().ListAgentPrincipals()
	if err != nil {
		return err
	}

	if command.Json {
		return displayhelpers.JsonPrint(list)
	}

	table := ui.Table{Headers: ui.TableRow{
		{Contents: "id", Color: color.New(color.Bold)},
		{Contents: "name", Color: color.New(color.Bold)},
		{Contents: "scopes", Color: color.New(color.Bold)},
		{Contents: "team", Color: color.New(color.Bold)},
		{Contents: "created", Color: color.New(color.Bold)},
		{Contents: "expires", Color: color.New(color.Bold)},
		{Contents: "revoked", Color: color.New(color.Bold)},
		{Contents: "last-used", Color: color.New(color.Bold)},
	}}
	for _, p := range list {
		row := ui.TableRow{
			{Contents: strconv.Itoa(p.ID)},
			{Contents: p.Name},
			{Contents: strings.Join(p.Scopes, ",")},
			{Contents: p.TeamName},
			{Contents: humanizePrincipalTime(&p.CreatedAt)},
			{Contents: humanizePrincipalTime(p.ExpiresAt)},
			{Contents: humanizePrincipalTime(p.RevokedAt)},
			{Contents: humanizePrincipalTime(p.LastUsedAt)},
		}
		// Dim revoked principals so the live ones stand out.
		if p.RevokedAt != nil {
			for i := range row {
				row[i].Color = color.New(color.Faint)
			}
		}
		table.Data = append(table.Data, row)
	}
	return table.Render(os.Stdout, Fly.PrintTableHeaders)
}

type AgentPrincipalsMintCommand struct {
	Name        string         `long:"name" required:"true" description:"Principal name (unique)"`
	Description string         `long:"description" description:"What this principal is for"`
	Scopes      []string       `long:"scope" required:"true" description:"Scope to grant; repeatable. One of: reviews:write, tickets:read, tickets:write, metrics:write, costs:write"`
	Team        string         `long:"team" default:"main" description:"Team the principal belongs to"`
	ExpiresIn   *time.Duration `long:"expires-in" description:"Optional lifetime, e.g. 720h. Omit for a non-expiring principal."`
	Json        bool           `long:"json" description:"Print the created principal, including the one-time token, as JSON"`
}

func (command *AgentPrincipalsMintCommand) Execute([]string) error {
	// Validate scopes against the closed vocabulary before hitting the API so
	// an obvious typo fails fast with a clear, local message.
	var invalid []string
	for _, s := range command.Scopes {
		if !principals.ValidScopes[s] {
			invalid = append(invalid, s)
		}
	}
	if len(invalid) > 0 {
		return fmt.Errorf("unknown scope(s): %s\nvalid scopes are: %s",
			strings.Join(invalid, ", "), strings.Join(sortedScopes(), ", "))
	}

	// An explicit zero or negative expiry would silently mint a
	// never-expiring principal; reject it loudly instead. A pointer field
	// distinguishes "flag omitted" (nil, meaning no expiry) from an explicit
	// non-positive value.
	if command.ExpiresIn != nil && *command.ExpiresIn <= 0 {
		return errors.New("--expires-in must be a positive duration")
	}

	target, err := loadAgentTarget()
	if err != nil {
		return err
	}

	spec := atc.AgentPrincipalCreateSpec{
		Name:        command.Name,
		Description: command.Description,
		Scopes:      command.Scopes,
		TeamName:    command.Team,
	}
	if command.ExpiresIn != nil {
		exp := time.Now().Add(*command.ExpiresIn).Unix()
		spec.ExpiresAt = &exp
	}

	created, err := target.Client().CreateAgentPrincipal(spec)
	if err != nil {
		return err
	}

	if command.Json {
		return displayhelpers.JsonPrint(created)
	}

	fmt.Printf("minted principal %d (%s)\n", created.ID, created.Name)
	fmt.Printf("  scopes:  %s\n", strings.Join(created.Scopes, ", "))
	fmt.Printf("  team:    %s\n", created.TeamName)
	if created.ExpiresAt != nil {
		fmt.Printf("  expires: %s\n", time.Unix(*created.ExpiresAt, 0).Local().Format("2006-01-02 15:04"))
	}
	fmt.Println()
	// The raw token is returned exactly once — make it impossible to miss.
	fmt.Println(ui.ErroredColor.Sprint("STORE THIS TOKEN NOW — it will not be shown again:"))
	fmt.Printf("\n    %s\n\n", created.Token)
	return nil
}

type AgentPrincipalsRevokeCommand struct {
	ID   int `long:"id" description:"ID of the principal to revoke"`
	Args struct {
		ID int `positional-arg-name:"ID" description:"ID of the principal to revoke"`
	} `positional-args:"yes"`
}

func (command *AgentPrincipalsRevokeCommand) Execute([]string) error {
	id := command.ID
	if id == 0 {
		id = command.Args.ID
	}
	if id == 0 {
		return fmt.Errorf("principal id is required (pass as an argument or with --id)")
	}

	target, err := loadAgentTarget()
	if err != nil {
		return err
	}

	err = target.Client().RevokeAgentPrincipal(id)
	if errors.Is(err, concourse.ErrAgentPrincipalNotFound) {
		return fmt.Errorf("no such principal: %d", id)
	}
	if err != nil {
		return err
	}

	fmt.Printf("revoked principal %d\n", id)
	return nil
}

// sortedScopes returns the closed scope vocabulary in a stable order for
// display in error messages.
func sortedScopes() []string {
	scopes := make([]string, 0, len(principals.ValidScopes))
	for s := range principals.ValidScopes {
		scopes = append(scopes, s)
	}
	sort.Strings(scopes)
	return scopes
}

// humanizePrincipalTime renders a Unix-epoch-seconds pointer as a compact
// relative time ("3d ago", "5h from now"), a date for anything older/further
// than 30 days, or "-" when the timestamp is absent or zero.
func humanizePrincipalTime(ts *int64) string {
	if ts == nil || *ts == 0 {
		return "-"
	}
	t := time.Unix(*ts, 0)
	d := time.Since(t)

	suffix := "ago"
	if d < 0 {
		d = -d
		suffix = "from now"
	}

	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm %s", int(d.Minutes()), suffix)
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh %s", int(d.Hours()), suffix)
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd %s", int(d.Hours()/24), suffix)
	default:
		return t.Local().Format("2006-01-02")
	}
}
