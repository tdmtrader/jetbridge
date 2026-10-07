package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"text/tabwriter"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/fly/commands/internal/flaghelpers"
	"github.com/concourse/concourse/fly/rc"
)

// landingQueueClient is what the three landing queue commands need of a team.
type landingQueueClient interface {
	SetLandingQueue(queueName string, config []byte) (bool, error)
	SubmitLanding(queueName string, submission atc.LandingSubmission) (bool, error)
	LandingQueue(queueName string) (atc.LandingQueueStatus, bool, error)
}

// SetLandingQueueCommand sets a team's landing queue from its YAML (ADR-0009).
type SetLandingQueueCommand struct {
	Queue  string               `short:"q" long:"queue" required:"true" description:"Name of the landing queue"`
	Config atc.PathFlag         `short:"c" long:"config" required:"true" description:"Landing queue configuration file, \"-\" stands for stdin"`
	Team   flaghelpers.TeamFlag `long:"team" description:"Name of the team the queue belongs to, if different from the target default"`
}

func (command *SetLandingQueueCommand) Execute([]string) error {
	team, err := loadTeam(command.Team)
	if err != nil {
		return err
	}
	return command.run(team, os.Stdin, os.Stdout)
}

func (command *SetLandingQueueCommand) run(client landingQueueClient, stdin io.Reader, out io.Writer) error {
	var body []byte
	var err error
	if string(command.Config) == "-" {
		body, err = io.ReadAll(stdin)
	} else {
		body, err = os.ReadFile(string(command.Config))
	}
	if err != nil {
		return err
	}
	if _, err := atc.ParseLandingQueueConfig(body); err != nil {
		return err
	}
	created, err := client.SetLandingQueue(command.Queue, body)
	if err != nil {
		return err
	}
	if created {
		fmt.Fprintf(out, "landing queue %q created\n", command.Queue)
	} else {
		fmt.Fprintf(out, "landing queue %q updated\n", command.Queue)
	}
	return nil
}

// LandCommand submits a commit to a landing queue: it pushes the commit to
// the queue's submit ref with the user's own git, so the compose step can
// reach it, then submits the entry.
type LandCommand struct {
	Queue  string               `short:"q" long:"queue" required:"true" description:"Name of the landing queue"`
	ID     string               `long:"id" description:"Entry id (one ref component); the commit's short sha when omitted"`
	Repo   string               `long:"repo" default:"." description:"Local repository holding the commit"`
	Remote string               `long:"remote" default:"origin" description:"Remote to push the submit ref to"`
	Team   flaghelpers.TeamFlag `long:"team" description:"Name of the team the queue belongs to, if different from the target default"`
	Args   struct {
		Commit string `positional-arg-name:"commit" description:"Commit or ref to land (default HEAD)"`
	} `positional-args:"yes"`
}

func (command *LandCommand) Execute([]string) error {
	team, err := loadTeam(command.Team)
	if err != nil {
		return err
	}
	return command.run(team, os.Stdout)
}

func (command *LandCommand) run(client landingQueueClient, out io.Writer) error {
	ref := command.Args.Commit
	if ref == "" {
		ref = "HEAD"
	}
	sha, err := gitOutput(command.Repo, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return fmt.Errorf("resolve %s: %w", ref, err)
	}
	id := command.ID
	if id == "" {
		id = sha[:12]
	}
	if !atc.ValidLandingEntryID(id) {
		return fmt.Errorf("entry id %q is not one safe ref component", id)
	}
	submitRef := "refs/queue/submit/" + id
	if _, err := gitOutput(command.Repo, "push", "--quiet", "--no-follow-tags", "--", command.Remote, sha+":"+submitRef); err != nil {
		return fmt.Errorf("push %s: %w", submitRef, err)
	}
	created, err := client.SubmitLanding(command.Queue, atc.LandingSubmission{ID: id, Commit: sha})
	if err != nil {
		return err
	}
	if created {
		fmt.Fprintf(out, "submitted %s as %s to landing queue %q\n", sha, id, command.Queue)
	} else {
		fmt.Fprintf(out, "%s is already queued as %s in landing queue %q\n", sha, id, command.Queue)
	}
	return nil
}

// LandingQueueCommand prints a landing queue's entries and the state of its
// landings.
type LandingQueueCommand struct {
	Queue string               `short:"q" long:"queue" required:"true" description:"Name of the landing queue"`
	Team  flaghelpers.TeamFlag `long:"team" description:"Name of the team the queue belongs to, if different from the target default"`
	JSON  bool                 `long:"json" description:"Print the queue as JSON"`
}

func (command *LandingQueueCommand) Execute([]string) error {
	team, err := loadTeam(command.Team)
	if err != nil {
		return err
	}
	return command.run(team, os.Stdout)
}

func (command *LandingQueueCommand) run(client landingQueueClient, out io.Writer) error {
	status, found, err := client.LandingQueue(command.Queue)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("landing queue %q not found", command.Queue)
	}
	if command.JSON {
		body, err := json.MarshalIndent(status, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, string(body))
		return err
	}
	fmt.Fprintf(out, "landing queue %s: %s on %s\n", status.Name, status.Config.Trunk, status.Config.Repository)
	if status.FailedLands > 0 {
		fmt.Fprintf(out, "failed landings in a row: %d (last: %s)\n", status.FailedLands, status.LastError)
	}
	table := tabwriter.NewWriter(out, 0, 8, 2, ' ', 0)
	fmt.Fprintln(table, "id\tcommit\tstate\tcompose\tland\treason")
	for _, entry := range status.Entries {
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\n", entry.ID, entry.Commit[:12], entry.State, runNumber(entry.ComposeRun), runNumber(entry.LandRun), entry.SettleReason)
	}
	return table.Flush()
}

func runNumber(n int) string {
	if n == 0 {
		return "-"
	}
	return fmt.Sprintf("#%d", n)
}

func loadTeam(flag flaghelpers.TeamFlag) (landingQueueClient, error) {
	target, err := rc.LoadTarget(Fly.Target, Fly.Verbose)
	if err != nil {
		return nil, err
	}
	if err := target.Validate(); err != nil {
		return nil, err
	}
	return flag.LoadTeam(target)
}

// gitOutput runs one git command in the repository with the user's own
// environment, so their credentials and remotes apply, and returns its
// trimmed stdout.
func gitOutput(repo string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr, cmd.Stdin = &stdout, &stderr, nil
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return "", err
		}
		return "", errors.New(msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}
