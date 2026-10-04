// Command queue runs one merge queue and lets an operator admit changes to it
// and read its state. Every subcommand reads one config file.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/concourse/concourse/queue/adapters/git"
	"github.com/concourse/concourse/queue/adapters/jetbridge"
	"github.com/concourse/concourse/queue/adapters/lognotify"
	"github.com/concourse/concourse/queue/config"
	"github.com/concourse/concourse/queue/core"
)

const usage = "usage: queue run|admit|resume|status|stats|view --config <file> [--every 5s] [--window 1h] [--once] [id sha]"

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

// run returns the exit code: 0 done, 2 admission refused (an unsafe id), 1 anything else.
func run(ctx context.Context, args []string, out, errw io.Writer) int {
	fail := func(err error) int { fmt.Fprintln(errw, "queue:", core.Redact(err.Error())); return 1 }
	if len(args) == 0 || !slices.Contains([]string{"run", "admit", "resume", "status", "stats", "view"}, args[0]) {
		return fail(errors.New(usage))
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(errw)
	file := fs.String("config", "", "the queue's config file")
	every := fs.Duration("every", 5*time.Second, "run: time between steps")
	once := fs.Bool("once", false, "run: take one step and exit")
	window := fs.Duration("window", time.Hour, "stats: the span to count over")
	if err := fs.Parse(args[1:]); err != nil {
		return 1
	}
	if args[0] == "run" && *every <= 0 {
		return fail(errors.New("--every must be positive"))
	}
	data, err := os.ReadFile(*file)
	if err != nil {
		return fail(err)
	}
	scan(data)
	c, err := config.Parse(data)
	if err != nil {
		return fail(err)
	}
	register(data, c)
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
	if args[0] == "resume" {
		return fail2(git.Resume(ctx, c, "."), fail)
	}
	if args[0] == "status" {
		s, err := git.NewStore(c).Load(ctx)
		if err != nil {
			return fail(err)
		}
		return fail2(json.NewEncoder(out).Encode(summary(s)), fail)
	}
	if args[0] == "stats" { // read-only: Load, never Save or the lease
		s, err := git.NewStore(c).Load(ctx)
		if err != nil {
			return fail(err)
		}
		return fail2(json.NewEncoder(out).Encode(core.Stats(s, time.Now(), *window)), fail)
	}
	if args[0] == "view" { // read-only: Load, never Save or the lease
		s, err := git.NewStore(c).Load(ctx)
		if err != nil {
			return fail(err)
		}
		now := time.Now()
		b, err := core.PanelView(s, core.Stats(s, now, time.Hour), now)
		if err != nil {
			return fail(err)
		}
		_, err = fmt.Fprintln(out, string(b))
		return fail2(err, fail)
	}
	d, closeFn, err := newDriver(c, out, errw)
	if err != nil {
		return fail(err)
	}
	defer closeFn()
	if *once {
		return fail2(d.Step(ctx), fail)
	}
	if err := d.Run(ctx, *every); !errors.Is(err, context.Canceled) {
		return fail(err)
	}
	return 0
}

// register gives core.Secrets every credential the config holds or names, before any
// adapter runs: the userinfo of each URL in it, under any key, and the runner's token.
func register(data []byte, c config.Config) {
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		if u, err := url.Parse(n.Value); err == nil && u.User != nil {
			pw, _ := u.User.Password()
			auth, _, _ := strings.Cut(n.Value[strings.Index(n.Value, "://")+3:], "/")
			for _, s := range []string{auth[:max(0, strings.LastIndex(auth, "@"))], u.User.String(), pw, u.User.Username() + ":" + pw} {
				core.Secrets.Add(strings.TrimSuffix(s, ":")) // as written, canonical, password, user:password
			}
		}
		for _, k := range n.Content {
			walk(k)
		}
	}
	var doc yaml.Node
	if yaml.Unmarshal(data, &doc) == nil {
		walk(&doc)
	}
	if rc, err := jetbridge.Parse(&c.Runner); err == nil {
		token, _ := rc.Secret()
		core.Secrets.Add(token)
	}
}

// rawURL is scheme:// then userinfo, which runs to the last @ before a space, quote or line end.
var rawURL = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://([^\s"']*)@`)

// scan registers the userinfo of every URL in the raw config text, its user and password
// parts too, without parsing: it works before Parse, and on a URL that does not parse.
func scan(data []byte) {
	for _, m := range rawURL.FindAllSubmatch(data, -1) {
		user, pw, _ := strings.Cut(string(m[1]), ":")
		for _, s := range []string{string(m[1]), user, pw} {
			core.Secrets.Add(s)
		}
	}
}

func fail2(err error, fail func(error) int) int {
	if err != nil {
		return fail(err)
	}
	return 0
}

// newDriver wires the driver; its logs go to errw and a "-" log notifier to out.
func newDriver(c config.Config, out, errw io.Writer) (d *core.Driver, closeFn func(), err error) {
	rc, err := jetbridge.Parse(&c.Runner)
	if err != nil {
		return nil, nil, err
	}
	logf := log.New(errw, "", log.LstdFlags).Printf
	notifier, err := newNotifier(c.Notify, out)
	if err != nil {
		return nil, nil, err
	}
	lander, err := git.New(c)
	if err != nil {
		return nil, nil, err
	}
	b := c.Batch
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	host, _ := os.Hostname()
	return &core.Driver{
			Store: git.NewStore(c), Composer: git.NewComposer(c), Runner: jetbridge.New(rc, logf), Lander: lander, Notifier: notifier,
			NewStrategy: func() core.Strategy {
				s := &core.Serial{Max: b.Max, Policy: core.Policy{RetryNone: b.RetryNone}}
				if a := b.Adaptive; a != nil {
					s.Adaptive = &core.Adaptive{Start: a.Start, Min: a.Min, GrowAfter: a.GrowAfter}
				}
				return s
			},
			Main: c.Repository.Main, Log: logf, Slots: 1, TTL: time.Minute, MaxFailures: c.Lander.MaxFailures,
			Admissions: &git.Admissions{Lander: lander, Prefix: c.Admission.Prefix},
			Resumes:    &git.Resumes{Lander: lander, Prefix: c.Admission.ControlPrefix + "resume-"},
			Owner:      fmt.Sprintf("%s-%d-%s", host, os.Getpid(), hex.EncodeToString(suffix)),
		}, func() {
			lander.Close()
			if c, ok := notifier.(io.Closer); ok {
				c.Close()
			}
		}, nil
}

type nopNotifier struct{}

func (nopNotifier) Notify(context.Context, core.Event) error { return nil }

func newNotifier(n yaml.Node, out io.Writer) (core.Notifier, error) {
	var k struct{ Kind string }
	if err := n.Decode(&k); err != nil {
		return nil, err
	}
	switch k.Kind {
	case "":
		return nopNotifier{}, nil
	case "log":
		c, err := lognotify.Parse(&n)
		if err != nil {
			return nil, err
		}
		return lognotify.Open(c, out)
	}
	return nil, fmt.Errorf("notify.kind %q is not supported by this command", k.Kind)
}

type change struct{ ID, Commit string }

type ejection struct {
	ID    string
	Why   string    `json:",omitempty"`
	Cause string    `json:",omitempty"`
	At    time.Time `json:",omitzero"`
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
		ejected = append(ejected, ejection{id, r.Why, r.Cause, r.At})
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
