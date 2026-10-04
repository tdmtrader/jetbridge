// Command queue runs one merge queue and lets an operator admit changes to it
// and read its state. Every subcommand reads one config file.
package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/concourse/concourse/queue/adapters/git"
	"github.com/concourse/concourse/queue/adapters/jetbridge"
	"github.com/concourse/concourse/queue/config"
	"github.com/concourse/concourse/queue/core"
	"github.com/concourse/concourse/queue/wire"
)

const usage = "usage: queue run|admit|withdraw|resolve|resume|promote|status|stats|view|health|list|ejected|explain|drain --config <file>|--source <file> [--every 5s] [--window 1h] [--once] [--owner name] [--json] [id sha]"

// exitCodes is printed by --help.
const exitCodes = `exit codes:
  health: 0 healthy, 3 unhealthy (the reason on stdout),
          any other non-zero: the state could not be read, unknown, nothing on stdout
  drain:  0 the lines printed whole, any other non-zero: nothing on stdout
  admit:  2 refused, an unsafe id
  else:   0 done, 1 failed
health and drain read --config, or else the resource's source JSON from --source or stdin.`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := entry(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// entry is the one place process output is wired: stdout, stderr, the flag and
// log packages and every logger below all write through one redacting writer each.
func entry(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	out, errw := core.NewRedactWriter(stdout), core.NewRedactWriter(stderr)
	defer out.Flush()
	defer errw.Flush()
	flag.CommandLine.SetOutput(errw)
	log.SetOutput(errw)
	return run(ctx, args, out, errw)
}

// run returns the exit code: 0 done, 2 admission refused (an unsafe id), 3 health: unhealthy, 1 anything else (see exitCodes).
func run(ctx context.Context, args []string, out, errw io.Writer) int {
	fail := func(err error) int { fmt.Fprintln(errw, "queue:", core.Redact(err.Error())); return 1 }
	if len(args) == 0 || !slices.Contains([]string{"run", "admit", "withdraw", "resolve", "resume", "promote", "status", "stats", "view", "health", "list", "ejected", "explain", "drain"}, args[0]) {
		return fail(errors.New(usage))
	}
	for _, a := range args { // before any error can quote one; a --flag=value is checked whole and as its value
		_, v, _ := strings.Cut(a, "=")
		for _, s := range []string{a, v} {
			if err := config.URL("argument", s); err != nil {
				return fail(err)
			}
		}
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(errw)
	file := fs.String("config", "", "the queue's config file")
	srcFile := fs.String("source", "", "health, drain: the queue resource's source JSON, instead of --config; default stdin")
	every := fs.Duration("every", 5*time.Second, "run: time between steps")
	once := fs.Bool("once", false, "run: take one step and exit")
	owner := fs.String("owner", "", "run: the lease owner, fixed so a new process renews its lease; default unique per process")
	asJSON := fs.Bool("json", false, "list, ejected, explain: print JSON")
	window := fs.Duration("window", time.Hour, "stats: the span to count over")
	fs.Usage = func() { fmt.Fprintln(errw, usage); fs.PrintDefaults(); fmt.Fprintln(errw, exitCodes) }
	if err := fs.Parse(args[1:]); errors.Is(err, flag.ErrHelp) {
		return 0
	} else if err != nil {
		return 1
	}
	if args[0] == "run" && *every <= 0 {
		return fail(errors.New("--every must be positive"))
	}
	c, cleanup, err := loadConfig(args[0], *file, *srcFile)
	if err != nil {
		return fail(err)
	}
	defer cleanup()
	if err := register(c); err != nil {
		return fail(err)
	}
	if slices.Contains(readVerbs, args[0]) { // read-only: Load, never Save or the lease
		if (fs.NArg() == 1) != (args[0] == "explain") || fs.NArg() > 1 {
			return fail(errors.New(usage))
		}
		s, err := git.NewStore(c).Load(ctx)
		if err != nil {
			return fail(err)
		}
		return fail2(readVerb(args[0], c.Repository.Main, s, fs.Arg(0), *asJSON, out), fail)
	}
	if args[0] == "drain" { // read-only: one read of the snapshot and lease, printed whole or not at all
		if fs.NArg() != 0 {
			return fail(errors.New(usage))
		}
		s, l, err := git.NewStore(c).LoadLease(ctx)
		if err != nil {
			return fail(err)
		}
		var b bytes.Buffer
		drainLines(s, l, time.Now(), &b)
		_, err = out.Write(b.Bytes())
		return fail2(err, fail)
	}
	if args[0] == "health" { // read-only: Load, never Save or the lease
		head := func(ctx context.Context) (string, error) {
			l, err := git.New(c)
			if err != nil {
				return "", err
			}
			defer l.Close()
			return l.Head(ctx, c.Repository.Main)
		}
		return health(c, git.NewStore(c).Load, head, time.Now(), out, errw)
	}
	if args[0] == "admit" {
		if fs.NArg() != 2 {
			return fail(errors.New(usage))
		}
		if err := git.SafeID(fs.Arg(0)); err != nil {
			fail(err)
			return 2
		}
		if err := git.Admit(ctx, c, ".", fs.Arg(0), fs.Arg(1)); err != nil {
			return fail(err)
		}
		fmt.Fprintf(out, "admitted %s; the runner will queue it\n", fs.Arg(0))
		return 0
	}
	if verb := map[string]func(context.Context, config.Config, string, string) error{"withdraw": git.Withdraw, "resolve": git.Resolve}[args[0]]; verb != nil {
		if fs.NArg() != 1 {
			return fail(errors.New(usage))
		}
		if err := verb(ctx, c, ".", fs.Arg(0)); err != nil {
			return fail(err)
		}
		fmt.Fprintf(out, "asked the runner to %s %s\n", args[0], fs.Arg(0))
		return 0
	}
	if args[0] == "resume" {
		return fail2(git.Resume(ctx, c, "."), fail)
	}
	if args[0] == "promote" {
		if fs.NArg() != 1 {
			return fail(errors.New(usage))
		}
		return fail2(git.Promote(ctx, c, ".", fs.Arg(0)), fail)
	}
	if args[0] == "status" || args[0] == "stats" || args[0] == "view" { // read-only: Load, never Save or the lease
		s, err := git.NewStore(c).Load(ctx)
		if err != nil {
			return fail(err)
		}
		now := time.Now()
		switch args[0] {
		case "status":
			return fail2(json.NewEncoder(out).Encode(summary(s)), fail)
		case "stats":
			return fail2(json.NewEncoder(out).Encode(core.Stats(s, now, *window)), fail)
		}
		b, err := core.PanelView(s, core.Stats(s, now, time.Hour), now)
		if err == nil {
			_, err = fmt.Fprintln(out, string(b))
		}
		return fail2(err, fail)
	}
	d, closeFn, err := newDriver(c, out, errw)
	if err != nil {
		return fail(err)
	}
	defer closeFn()
	d.Owner = cmp.Or(*owner, d.Owner)
	if *once {
		return fail2(d.Step(ctx), fail)
	}
	if err := d.Run(ctx, *every); !errors.Is(err, context.Canceled) {
		return fail(err)
	}
	return 0
}

// register gives core.Secrets the runner's token before any adapter runs. config.Parse has
// refused any URL holding credentials; only run validates the whole runner section.
func register(c config.Config) error {
	if c.Runner.IsZero() {
		return nil
	}
	var rc jetbridge.Config
	if err := c.Runner.Decode(&rc); err != nil {
		return errors.New("runner: a value has the wrong type") // a yaml error may quote a value
	}
	token, _ := rc.Secret()
	core.Secrets.Add(token)
	return nil
}

func fail2(err error, fail func(error) int) int {
	if err != nil {
		return fail(err)
	}
	return 0
}

// newDriver wires the driver with the JetBridge runner; its logs go to errw and a "-" log notifier to out.
func newDriver(c config.Config, out, errw io.Writer) (d *core.Driver, closeFn func(), err error) {
	rc, err := jetbridge.Parse(&c.Runner)
	if err != nil {
		return nil, nil, err
	}
	logf := log.New(errw, "", log.LstdFlags).Printf
	return wire.Driver(c, out, logf, jetbridge.New(rc, logf))
}

type change struct{ ID, Commit string }

type ejection struct {
	ID    string
	Why   string    `json:",omitempty"`
	Cause string    `json:",omitempty"`
	Owner string    `json:",omitempty"`
	At    time.Time `json:",omitzero"`
	core.Failure
}

// summary is what status prints: ids and commits only, never the config.
func summary(s core.Snapshot) any {
	s = core.RedactSnapshot(s)
	queued, flying := []change{}, []change{}
	for _, e := range s.Queued {
		queued = append(queued, change{e.ID, e.Commit})
	}
	for _, f := range s.InFlight {
		flying = append(flying, change{f.Run.ID, f.Candidate})
	}
	why := map[string]core.SettleRecord{} // the latest eject record of each id
	for _, r := range s.Settled {
		if r.Kind == core.EjectedEvent {
			why[r.ID] = r
		}
	}
	ejected := []ejection{}
	for id := range s.Ejected {
		r := why[id]
		ejected = append(ejected, ejection{id, r.Why, r.Cause, r.Owner, r.At, r.Failure})
	}
	slices.SortFunc(ejected, func(a, b ejection) int { return strings.Compare(a.ID, b.ID) })
	landed := []string{}
	for id := range s.Landed {
		landed = append(landed, id)
	}
	slices.Sort(landed)
	flakes := slices.DeleteFunc(slices.Clone(s.Settled), func(r core.SettleRecord) bool { return r.Kind != core.FlakeEvent })
	flakes = flakes[max(0, len(flakes)-10):]
	return struct {
		Queued, InFlight []change
		Landed           []string
		Ejected          []ejection
		Paused           bool
		Why              string              `json:",omitempty"`
		Refused          []core.Refusal      `json:",omitempty"`
		Flakes           []core.SettleRecord `json:",omitempty"`
	}{queued, flying, landed, ejected, s.Paused, s.Why, s.Refused, flakes}
}
