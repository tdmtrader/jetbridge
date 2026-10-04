package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/concourse/concourse/queue/config"
	"github.com/concourse/concourse/queue/core"
	"github.com/concourse/concourse/queue/wire"
)

// health prints one line and returns 0 if the queue is healthy, else 3 with the first reason it is not;
// when the state cannot be read it returns 1, unknown, and prints nothing on out.
// It only loads the snapshot, so a pipeline job can run it on a timer and go red.
// head reads main's sha now; when it cannot, the per-main auto-resume is judged by the pause's reason only.
func health(c config.Config, load func(context.Context) (core.Snapshot, error), head func(context.Context) (string, error), now time.Time, out, errw io.Writer) int {
	s, err := load(context.Background())
	if err != nil { // unknown, not unhealthy: no verdict on stdout
		fmt.Fprintln(errw, "queue: health: the state could not be read:", core.Redact(err.Error()))
		return 1
	}
	main, _ := head(context.Background())
	why := unhealthy(s, c.Health, c.Pause.Cooldown, now, main)
	if why == "" {
		fmt.Fprintln(out, "healthy")
		return 0
	}
	fmt.Fprintln(out, "unhealthy:", core.Redact(why))
	return 3
}

// unhealthy returns why the queue is red at now, or "" when it is not.
func unhealthy(s core.Snapshot, h config.Health, cooldown time.Duration, now time.Time, main string) string {
	if s.Paused {
		paused := now.Sub(s.PausedAt)
		noVerdict, held := core.PauseReason(s.Why)
		autoResumes := noVerdict && cooldown > 0
		// the one auto-resume per main is spent: the pause holds until an operator acts
		spent := held || (autoResumes && main != "" && s.ResumedOnMain == main)
		switch {
		case spent, !autoResumes && paused > h.PauseAfter:
			return fmt.Sprintf("paused for %s: %s; nothing auto-resumes it; run `queue resume`", paused.Round(time.Second), s.Why)
		case autoResumes && paused > cooldown+h.ResumeOverdue:
			return fmt.Sprintf("paused for %s: auto-resumes after %s, but is overdue", paused.Round(time.Second), cooldown)
		}
	}
	for _, f := range s.InFlight {
		if age := now.Sub(f.Started); !f.Started.IsZero() && age > h.MaxInFlight {
			return fmt.Sprintf("run %s in flight for %s, over %s", f.Run.ID, age.Round(time.Second), h.MaxInFlight)
		}
	}
	return ""
}

// loadConfig reads --config, or for health without it the queue resource's
// source JSON ({"source": {...}}) from --source or stdin, as the resource does.
func loadConfig(verb, file, srcFile string) (config.Config, func(), error) {
	none := func() {}
	if verb != "health" || file != "" {
		if srcFile != "" {
			return config.Config{}, none, errors.New("--source is for health without --config")
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return config.Config{}, none, err
		}
		c, err := config.Parse(data)
		return c, none, err
	}
	in := io.Reader(os.Stdin)
	if srcFile != "" {
		f, err := os.Open(srcFile)
		if err != nil {
			return config.Config{}, none, err
		}
		defer f.Close()
		in = f
	}
	var req struct{ Source wire.Source }
	if err := json.NewDecoder(in).Decode(&req); err != nil {
		return config.Config{}, none, errors.New("the source is not valid JSON")
	}
	cleanup, err := wire.SSHKey(req.Source)
	if err != nil {
		return config.Config{}, none, err
	}
	c, err := wire.LoadConfig(req.Source)
	if err != nil {
		cleanup()
		return config.Config{}, none, err
	}
	return c, cleanup, nil
}
