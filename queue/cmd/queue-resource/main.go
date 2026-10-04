// Command queue-resource runs the queue as a CI resource type: one binary,
// installed as check, in and out, reading a JSON request on stdin and writing
// JSON on stdout, logs on stderr. Source mode candidate takes one queue step on
// each check and hands out the run being tested; mode verdict records the test
// job's pass or fail. Nothing is kept in memory between calls.
package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/concourse/concourse/queue/adapters/git"
	"github.com/concourse/concourse/queue/config"
	"github.com/concourse/concourse/queue/core"
	"github.com/concourse/concourse/queue/wire"
)

// source is the same in both resources but for mode.
type source struct {
	Mode       string `json:"mode"` // candidate or verdict
	URI        string `json:"uri"`
	PrivateKey string `json:"private_key"`
	KnownHosts string `json:"known_hosts"` // required with private_key
	Main       string `json:"main"`
	Config     string `json:"config"`      // the queue config, inline
	ConfigFile string `json:"config_file"` // or a path to it
	Owner      string `json:"owner"`       // the fixed lease owner, so each check renews the lease
	WaitCap    string `json:"wait_cap"`    // how long a run waits for a verdict; default 1h
}

type request struct {
	Source  source            `json:"source"`
	Version map[string]string `json:"version"`
	Params  struct {
		Verdict string `json:"verdict"`
		RunDir  string `json:"run_dir"`  // the candidate get's dir, under the sources dir
		HookDir string `json:"hook_dir"` // optional: a dir holding one git bundle of the hook's commit on the candidate
	} `json:"params"`
}

var (
	fullSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)
	runID   = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]*$`)
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	as, args := filepath.Base(os.Args[0]), os.Args[1:]
	if as != "check" && as != "in" && as != "out" && len(args) > 0 { // run as a subcommand
		as, args = args[0], args[1:]
	}
	code := entry(ctx, as, args, os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// entry runs one call; every write goes through a redacting writer.
func entry(ctx context.Context, as string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	out, errw := core.NewRedactWriter(stdout), core.NewRedactWriter(stderr)
	defer out.Flush()
	defer errw.Flush()
	log.SetOutput(errw)
	if err := call(ctx, as, args, stdin, out, errw); err != nil {
		fmt.Fprintln(errw, "queue-resource:", core.Redact(err.Error()))
		return 1
	}
	return 0
}

func call(ctx context.Context, as string, args []string, stdin io.Reader, out, errw io.Writer) error {
	if as != "check" && as != "in" && as != "out" {
		return fmt.Errorf("run as check, in or out, not %q", as)
	}
	var req request
	if err := json.NewDecoder(stdin).Decode(&req); err != nil {
		return errors.New("the request is not valid JSON")
	}
	s := req.Source
	if s.Mode != "candidate" && s.Mode != "verdict" {
		return errors.New("source.mode must be candidate or verdict")
	}
	if (as == "in" || as == "out") && len(args) != 1 {
		return fmt.Errorf("%s takes one directory", as)
	}
	cleanup, err := sshKey(s)
	if err != nil {
		return err
	}
	defer cleanup()
	c, err := loadConfig(s)
	if err != nil {
		return err
	}
	waitCap, err := time.ParseDuration(cmp.Or(s.WaitCap, "1h"))
	if err != nil || waitCap <= 0 {
		return errors.New("source.wait_cap must be a positive duration")
	}
	enc := json.NewEncoder(out)
	switch {
	case as == "check" && s.Mode == "verdict":
		return enc.Encode([]map[string]string{})
	case as == "in" && s.Mode == "verdict":
		return enc.Encode(map[string]any{"version": req.Version})
	case as == "out" && s.Mode == "candidate":
		return errors.New("a verdict is put to the verdict source, not the candidate source")
	case as == "check":
		vs, err := check(ctx, c, s.Owner, waitCap, errw)
		if err != nil {
			return err
		}
		return enc.Encode(vs)
	case as == "in":
		if err := get(ctx, c.Repository.URI, req.Version, args[0]); err != nil {
			return err
		}
		return enc.Encode(map[string]any{"version": req.Version})
	}
	p := req.Params
	v, err := put(ctx, git.NewRunner(c.Repository.URI, waitCap), p.Verdict, args[0], p.RunDir, p.HookDir)
	if err != nil {
		return err
	}
	return enc.Encode(map[string]any{"version": v})
}

// check takes one queue step and reports the newest run in flight, if any.
func check(ctx context.Context, c config.Config, owner string, waitCap time.Duration, errw io.Writer) ([]map[string]string, error) {
	r := &capped{Runner: git.NewRunner(c.Repository.URI, waitCap), expired: map[string]bool{}}
	d, closeFn, err := wire.Driver(c, errw, log.New(errw, "", log.LstdFlags).Printf, r)
	if err != nil {
		return nil, err
	}
	defer closeFn()
	d.Owner = cmp.Or(owner, "queue-resource")
	if err := d.Step(ctx); err != nil {
		return nil, err
	}
	snap, err := git.NewStore(c).Load(ctx)
	if err != nil {
		return nil, err
	}
	vs := []map[string]string{}
	if n := len(snap.InFlight); n > 0 {
		f := snap.InFlight[n-1]
		vs = append(vs, map[string]string{"run": f.Run.ID, "candidate": f.Candidate, "fence": strconv.FormatUint(f.Fence, 10)})
	}
	return vs, nil
}

// get checks the run's candidate out at dest and writes its run, candidate and fence files there.
func get(ctx context.Context, uri string, v map[string]string, dest string) error {
	if !runID.MatchString(v["run"]) || !fullSHA.MatchString(v["candidate"]) {
		return errors.New("the version needs a run id and a full candidate sha")
	}
	if _, err := runGit(ctx, "", "init", "-q", dest); err != nil {
		return err
	}
	if _, err := runGit(ctx, dest, "fetch", "-q", "--no-tags", "--depth=1", "--end-of-options", uri, "refs/mq/runs/"+v["run"]); err != nil {
		return err
	}
	if got, err := runGit(ctx, dest, "rev-parse", "FETCH_HEAD^{commit}"); err != nil || got != v["candidate"] {
		return cmp.Or(err, fmt.Errorf("run %s no longer tests %s", v["run"], v["candidate"]))
	}
	if _, err := runGit(ctx, dest, "checkout", "-q", "--detach", v["candidate"]); err != nil {
		return err
	}
	for _, f := range []string{"run", "candidate", "fence"} {
		if _, err := os.Lstat(filepath.Join(dest, f)); err == nil {
			return fmt.Errorf("the candidate holds a file named %s, which the get writes", f)
		}
		if err := os.WriteFile(filepath.Join(dest, f), []byte(v[f]+"\n"), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// put records the verdict for the run the get in runDir fetched; it never lands.
// A pass with hookDir also records the hook's commit, in the same push.
func put(ctx context.Context, r *git.Runner, verdict, sources, runDir, hookDir string) (map[string]string, error) {
	if verdict != string(core.Pass) && verdict != string(core.Fail) {
		return nil, errors.New("params.verdict must be pass or fail")
	}
	if !filepath.IsLocal(runDir) || hookDir != "" && !filepath.IsLocal(hookDir) {
		return nil, errors.New("params.run_dir and params.hook_dir must name dirs among the job's inputs")
	}
	v := map[string]string{"verdict": verdict}
	for _, f := range []string{"run", "candidate"} {
		b, err := os.ReadFile(filepath.Join(sources, runDir, f))
		if err != nil {
			return nil, fmt.Errorf("params.run_dir: %s: %w", f, err)
		}
		v[f] = strings.TrimSpace(string(b))
	}
	if !runID.MatchString(v["run"]) {
		return nil, errors.New("params.run_dir holds no run id")
	}
	if hookDir == "" || verdict != string(core.Pass) {
		return v, r.RecordVerdict(ctx, v["run"], v["candidate"], core.Verdict(verdict))
	}
	bundles, err := filepath.Glob(filepath.Join(sources, hookDir, "*.bundle"))
	if err != nil || len(bundles) != 1 {
		return nil, errors.New("params.hook_dir must hold exactly one .bundle file")
	}
	bundle, err := filepath.Abs(bundles[0])
	if err != nil {
		return nil, err
	}
	return v, r.RecordPassHooked(ctx, v["run"], v["candidate"], bundle)
}

// loadConfig reads the queue config, inline or from a file; source.uri and source.main, if set, replace the config's.
func loadConfig(s source) (config.Config, error) {
	data := []byte(s.Config)
	if (s.Config == "") == (s.ConfigFile == "") {
		return config.Config{}, errors.New("give one of source.config or source.config_file")
	}
	if s.ConfigFile != "" {
		b, err := os.ReadFile(s.ConfigFile)
		if err != nil {
			return config.Config{}, errors.New("source.config_file cannot be read")
		}
		data = b
	}
	if err := config.URL("source.uri", s.URI); err != nil {
		return config.Config{}, err
	}
	return config.ParseWith(data, func(c *config.Config) {
		c.Repository.URI, c.Repository.Main = cmp.Or(s.URI, c.Repository.URI), cmp.Or(s.Main, c.Repository.Main)
	})
}

// sshKey writes the private key and known hosts to 0600 files git's ssh uses, removed by cleanup.
func sshKey(s source) (cleanup func(), err error) {
	if s.PrivateKey == "" {
		return func() {}, nil
	}
	for line := range strings.SplitSeq(s.PrivateKey, "\n") {
		if l := strings.TrimSpace(line); l != "" && !strings.HasPrefix(l, "-----") {
			core.Secrets.Add(l)
		}
	}
	if s.KnownHosts == "" {
		return nil, errors.New("source.known_hosts is required with source.private_key")
	}
	dir, err := os.MkdirTemp("", "queue-resource-ssh-")
	if err != nil {
		return nil, err
	}
	cleanup = func() { os.RemoveAll(dir) }
	key, hosts := filepath.Join(dir, "key"), filepath.Join(dir, "known_hosts")
	if err := errors.Join(os.WriteFile(key, []byte(strings.TrimSpace(s.PrivateKey)+"\n"), 0o600),
		os.WriteFile(hosts, []byte(s.KnownHosts+"\n"), 0o600)); err != nil {
		cleanup()
		return nil, err
	}
	os.Setenv("GIT_SSH_COMMAND", "ssh -i "+key+" -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile="+hosts)
	return cleanup, nil
}

// capped marks a run whose poll gave no verdict and no error: with the git
// runner, only its wait cap running out does that.
type capped struct {
	core.Runner
	expired map[string]bool
}

func (c *capped) Poll(ctx context.Context, id string) (core.Verdict, bool, error) {
	v, done, err := c.Runner.Poll(ctx, id)
	c.expired[id] = err == nil && done && v == core.None
	return v, done, err
}

func (c *capped) Expired(id string) bool { return c.expired[id] }

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	b, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %v: %s", args[0], err, core.Redact(strings.TrimSpace(string(b))))
	}
	return strings.TrimSpace(string(b)), nil
}
