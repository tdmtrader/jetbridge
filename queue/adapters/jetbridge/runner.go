package jetbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/concourse/concourse/queue/core"
)

// Runner tests a candidate in steps, one per Poll and with no goroutines:
// Start asks the resource to check; then Poll pins the candidate's version,
// triggers the job and waits for the build. Pass or Fail is reported only for a
// build JetBridge lists as having had the pinned version as an input
// (input_to), so a job may get the resource under any input name. The
// candidate must be reachable by the resource's configured branch. The pin is
// per resource, so a new Start supersedes the previous run: its build is
// aborted and unpinned, errors only logged. Two-writer safety is the core lease's.
type Runner struct {
	Config Config
	Client *http.Client
	Now    func() time.Time
	Log    func(format string, args ...any)
	runs   map[string]*run
}

type run struct {
	candidate             string
	started               time.Time
	check, version, build int // version and build are 0 until the job is triggered
}

type item struct {
	ID      int
	Status  string
	Version struct{ Ref string }
}

// New returns a Runner with a 30s HTTP timeout, the wall clock and log.Printf.
func New(c Config) *Runner {
	return &Runner{Config: c, Client: &http.Client{Timeout: 30 * time.Second}, Now: time.Now, Log: log.Printf,
		runs: map[string]*run{}}
}

// Start begins a run, first releasing any run in flight (best effort).
func (j *Runner) Start(ctx context.Context, r core.Run, candidate string) error {
	for id, old := range j.runs {
		delete(j.runs, id)
		if old.build != 0 {
			j.try(ctx, "PUT", fmt.Sprintf("/api/v1/builds/%d/abort", old.build))
		}
		j.try(ctx, "PUT", j.res("/unpin"))
	}
	var b item
	if err := j.call(ctx, "POST", j.res("/check"), &b); err != nil {
		return err
	}
	j.runs[r.ID] = &run{candidate: candidate, started: j.Now(), check: b.ID}
	return nil
}

// Poll advances a run by one step. An error is no verdict, never red.
func (j *Runner) Poll(ctx context.Context, id string) (core.Verdict, bool, error) {
	r, ok := j.runs[id]
	if !ok {
		return core.None, true, fmt.Errorf("run %q was not started by this runner", id)
	}
	if j.Now().Sub(r.started) > j.Config.WaitCap {
		delete(j.runs, id)
		if r.build != 0 {
			j.try(ctx, "PUT", fmt.Sprintf("/api/v1/builds/%d/abort", r.build))
			j.try(ctx, "PUT", j.res("/unpin"))
		}
		return core.None, true, nil
	}
	if r.build == 0 {
		return j.trigger(ctx, id, r)
	}
	st, err := j.status(ctx, r.build)
	if err != nil || st == "pending" || st == "started" {
		return core.None, false, err
	}
	v := core.None
	if verdict, ok := map[string]core.Verdict{"succeeded": core.Pass, "failed": core.Fail}[st]; ok {
		var in []item // the builds the pinned version was an input to
		if err := j.call(ctx, "GET", j.res(fmt.Sprintf("/versions/%d/input_to", r.version)), &in); err != nil {
			return core.None, false, err
		}
		if slices.ContainsFunc(in, func(b item) bool { return b.ID == r.build }) {
			v = verdict
		}
	}
	delete(j.runs, id)
	j.try(ctx, "PUT", j.res("/unpin"))
	return v, true, nil
}

func (j *Runner) trigger(ctx context.Context, id string, r *run) (core.Verdict, bool, error) {
	st, err := j.status(ctx, r.check)
	if err != nil || st == "pending" || st == "started" {
		return core.None, false, err
	}
	var vs []item
	if st == "succeeded" {
		if err := j.call(ctx, "GET", j.res("/versions?filter="+url.QueryEscape("ref:"+r.candidate)), &vs); err != nil {
			return core.None, false, err
		}
	}
	for _, v := range vs {
		if v.Version.Ref != r.candidate {
			continue
		}
		var b item
		if err := j.call(ctx, "PUT", j.res(fmt.Sprintf("/versions/%d/pin", v.ID)), nil); err != nil {
			return core.None, false, err
		}
		if err := j.call(ctx, "POST", j.pipe("/jobs/"+url.PathEscape(j.Config.Job)+"/builds"), &b); err != nil {
			j.try(ctx, "PUT", j.res("/unpin"))
			return core.None, false, err
		}
		r.version, r.build = v.ID, b.ID
		return core.None, false, nil
	}
	delete(j.runs, id) // the check failed or never found the candidate
	return core.None, true, nil
}

func (j *Runner) status(ctx context.Context, build int) (string, error) {
	var b item
	err := j.call(ctx, "GET", fmt.Sprintf("/api/v1/builds/%d", build), &b)
	return b.Status, err
}

// call sends one request with the bearer token, read on every call so it can
// be replaced while running, and decodes a JSON reply into out.
func (j *Runner) call(ctx context.Context, method, path string, out any) error {
	token, err := j.Config.secret()
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, j.Config.URL+path, strings.NewReader("{}"))
	if err != nil {
		return errors.New(core.Redact(err.Error())) // url.Parse quotes the url, password and all
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := j.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %s", method, path, resp.Status)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// try is a best-effort call: a failure is logged, never a verdict.
func (j *Runner) try(ctx context.Context, method, path string) {
	if err := j.call(ctx, method, path, nil); err != nil {
		j.Log("%s %s: %v (ignored)", method, path, err)
	}
}

func (j *Runner) pipe(p string) string {
	return "/api/v1/teams/" + url.PathEscape(j.Config.Team) + "/pipelines/" + url.PathEscape(j.Config.Pipeline) + p
}

func (j *Runner) res(p string) string {
	return j.pipe("/resources/" + url.PathEscape(j.Config.Resource) + p)
}
