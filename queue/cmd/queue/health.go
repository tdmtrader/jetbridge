package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/concourse/concourse/queue/config"
	"github.com/concourse/concourse/queue/core"
)

// health prints one line and returns 0 if the queue is healthy, else 3 with the first reason it is not.
// It only loads the snapshot, so a pipeline job can run it on a timer and go red.
func health(c config.Config, load func(context.Context) (core.Snapshot, error), now time.Time, out io.Writer) int {
	s, err := load(context.Background())
	why := ""
	if err != nil {
		why = "queue state damaged: " + err.Error()
	} else {
		why = unhealthy(s, c.Health, c.Pause.Cooldown, now)
	}
	if why == "" {
		fmt.Fprintln(out, "healthy")
		return 0
	}
	fmt.Fprintln(out, "unhealthy:", core.Redact(why))
	return 3
}

// unhealthy returns why the queue is red at now, or "" when it is not.
func unhealthy(s core.Snapshot, h config.Health, cooldown time.Duration, now time.Time) string {
	if s.Paused {
		paused := now.Sub(s.PausedAt)
		autoResumes := strings.HasPrefix(s.Why, "no verdict after ") && cooldown > 0
		switch {
		case !autoResumes && paused > h.PauseAfter:
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
