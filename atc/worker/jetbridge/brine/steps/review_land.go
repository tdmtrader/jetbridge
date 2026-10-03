package steps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	reviewclient "github.com/concourse/concourse/agent/review/client"
	"github.com/concourse/concourse/atc/db"
)

// A landing drives `jb review land` end to end: git against a local bare
// origin, and the review through the same API, admitted Run, Hangar upload and
// Run worker a submitted review uses. Only the model executable is
// substituted; its one finding is "high".
type landing struct {
	auth                                    *AuthFixture
	change                                  ReviewChange
	origin, state, team, template, authFile string
}

type landProcess struct {
	cmd            *exec.Cmd
	stdout, stderr bytes.Buffer
	done           chan error
	exit           error
}

func exerciseReviewLand(in RunInputAdmission, auth *AuthFixture, change ReviewChange, mode string, rec *brine.Recorder, res brine.Resources) error {
	var err error
	// A landing's review receives the owner's credentials, which are
	// delivered only into an operator-pinned worker image.
	if in.Template, err = pinProducerImage(in.Source.Start.DB.TeamFactory, in.Template, brineCredentialWorkerImage); err != nil {
		return err
	}
	l := landing{auth: auth, change: change, origin: filepath.Join(change.Workspace.Root, "origin.git"), state: filepath.Join(change.Workspace.Root, "land"), team: "output-start", template: "input-review", authFile: filepath.Join(change.Workspace.Root, "auth.json")}
	if _, err = change.git("init", "-q", "--bare", l.origin); err != nil {
		return err
	}
	if _, err = change.git("remote", "add", "origin", l.origin); err != nil {
		return err
	}
	if _, err = change.git("push", "-q", "origin", change.Base+":refs/heads/core"); err != nil {
		return err
	}
	jdb := in.Source.Start.DB
	count := func() (int, error) {
		var n int
		err := jdb.Conn.QueryRow(`SELECT count(*) FROM pipeline_runs WHERE template_pipeline_id=$1`, in.Template.ID()).Scan(&n)
		return n, err
	}
	before, err := count()
	if err != nil {
		return err
	}

	// The first landing is interrupted once its Run is admitted; the Run
	// number it prints is the handle to that Run.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	first, err := l.start(ctx, "blocker")
	if err != nil {
		return err
	}
	var receipt struct {
		RunID  int `json:"run_id"`
		Number int `json:"number"`
	}
	var receiptPath string
	for receipt.RunID == 0 {
		if files, _ := filepath.Glob(filepath.Join(l.state, "*", "receipt.json")); len(files) == 1 {
			if data, err := os.ReadFile(files[0]); err == nil && json.Unmarshal(data, &receipt) == nil && receipt.RunID > 0 {
				receiptPath = files[0]
				break
			}
		}
		select {
		case <-first.done:
			return fmt.Errorf("landing exited without an admitted receipt: %s", first.stderr.Bytes())
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err = first.cmd.Process.Signal(os.Interrupt); err != nil {
		return err
	}
	interrupted, err := first.outcome(ctx)
	if err != nil {
		return err
	}
	if first.exit == nil || interrupted.Outcome != "" || interrupted.RunNumber != receipt.Number || !strings.Contains(interrupted.Message, "context canceled") {
		return fmt.Errorf("interrupted landing lost its admitted Run: %v, %+v", first.exit, interrupted)
	}
	if err = l.requireCore(change.Base); err != nil {
		return err
	}
	after, err := count()
	if err != nil || after != before+1 {
		return fmt.Errorf("landing admitted %d Runs, want one: %v", after-before, err)
	}

	pending := reviewclient.Submission{RunID: receipt.RunID, Handle: reviewclient.Handle{Team: l.team, Template: l.template, Number: receipt.Number}}
	change.Input = filepath.Join(filepath.Dir(receiptPath), "bundle")
	return finishSubmittedReview(in, auth, change, pending, l.surface(mode, pending, jdb.Conn), rec, res)
}

// surface resumes the review through a second landing, which runs on past
// readiness to the decision and the push.
func (l landing) surface(mode string, pending reviewclient.Submission, conn db.DbConn) reviewSurface {
	var second *landProcess
	blockAt := map[string]string{"a landing below the floor": "blocker", "a landing at the floor": "high", "a landing after core moved": "blocker"}[mode]
	var moved string
	return reviewSurface{
		Resume: func(ctx context.Context) error {
			if blockAt == "" {
				return fmt.Errorf("unknown landing case %q", mode)
			}
			var err error
			if second, err = l.start(ctx, blockAt); err != nil {
				return err
			}
			for {
				var ready int
				if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM pipeline_run_credential_handoffs WHERE run_id=$1 AND ready_at IS NOT NULL`, pending.RunID).Scan(&ready); err != nil {
					return err
				}
				if ready == 1 {
					break
				}
				select {
				case <-second.done:
					return fmt.Errorf("resumed landing exited before its Run was ready: %s", second.stderr.Bytes())
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(25 * time.Millisecond):
				}
			}
			if mode == "a landing after core moved" {
				// Another change lands on core while this one is under review.
				if moved, err = l.change.git("commit-tree", l.change.Base+"^{tree}", "-p", l.change.Base, "-m", "landed elsewhere"); err != nil {
					return err
				}
				if _, err = l.change.git("push", "-q", "origin", moved+":refs/heads/core"); err != nil {
					return err
				}
			}
			return nil
		},
		Read: func(ctx context.Context) error {
			got, err := second.outcome(ctx)
			if err != nil {
				return err
			}
			want := reviewclient.LandOutcome{Outcome: "landed", RunNumber: pending.Handle.Number, BaseCommit: l.change.Base, HeadCommit: l.change.Head, Verdict: "findings", Blocking: []string{}}
			core := l.change.Head
			switch mode {
			case "a landing at the floor":
				want.Outcome, want.Reason, want.Blocking, core = "refused", "findings at or above high", []string{"f-001"}, l.change.Base
			case "a landing after core moved":
				want.Outcome, want.Reason, core = "refused", "core moved; rebase and re-review", moved
			}
			if !reflect.DeepEqual(got, want) || (second.exit == nil) != (want.Outcome == "landed") {
				return fmt.Errorf("landing decided %+v (exit %v), want %+v: %s", got, second.exit, want, second.stderr.Bytes())
			}
			if err = l.requireCore(core); err != nil {
				return err
			}
			if mode != "a landing at the floor" {
				return nil
			}
			// A refused landing is re-evaluated at another floor from the
			// same Run's report: the rerun resumes rather than reviewing again.
			third, err := l.start(ctx, "blocker")
			if err != nil {
				return err
			}
			if got, err = third.outcome(ctx); err != nil {
				return err
			}
			want.Outcome, want.Reason, want.Blocking = "landed", "", []string{}
			if !reflect.DeepEqual(got, want) || third.exit != nil {
				return fmt.Errorf("rerun at a lower floor decided %+v (exit %v), want %+v: %s", got, third.exit, want, third.stderr.Bytes())
			}
			return l.requireCore(l.change.Head)
		},
	}
}

func (l landing) start(ctx context.Context, blockAt string) (*landProcess, error) {
	p := &landProcess{done: make(chan error, 1)}
	p.cmd = exec.CommandContext(ctx, l.change.Binaries.CLI, "review", "land", "--target", "auth", "--team", l.team, "--template", l.template, "--auth-file", l.authFile, "--repo", l.change.Repo, "--state", l.state, "--block-at", blockAt)
	p.cmd.Env = append(os.Environ(), "FLY_HOME="+l.auth.Home, "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	p.cmd.Stdout, p.cmd.Stderr = &p.stdout, &p.stderr
	if err := p.cmd.Start(); err != nil {
		return nil, err
	}
	go func() { p.done <- p.cmd.Wait() }()
	return p, nil
}

// outcome waits for the landing to exit and decodes the one line it printed.
func (p *landProcess) outcome(ctx context.Context) (reviewclient.LandOutcome, error) {
	var got reviewclient.LandOutcome
	select {
	case p.exit = <-p.done:
	case <-ctx.Done():
		return got, fmt.Errorf("landing did not finish: %w: %s", ctx.Err(), p.stderr.Bytes())
	}
	d := json.NewDecoder(bytes.NewReader(p.stdout.Bytes()))
	d.DisallowUnknownFields()
	if err := d.Decode(&got); err != nil {
		return got, fmt.Errorf("landing printed no outcome (exit %v): %w: %s", p.exit, err, p.stderr.Bytes())
	}
	if d.More() {
		return got, errors.New("landing printed more than one outcome")
	}
	return got, nil
}

func (l landing) requireCore(want string) error {
	got, err := l.change.git("--git-dir", l.origin, "rev-parse", "refs/heads/core")
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("origin core is %s, want %s", got, want)
	}
	return nil
}
