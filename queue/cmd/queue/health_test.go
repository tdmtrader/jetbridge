package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/queue/config"
	"github.com/concourse/concourse/queue/core"
)

var _ = Describe("health", func() {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	c := config.Defaults()
	check := func(s core.Snapshot, err error) (int, string) {
		var b bytes.Buffer
		var errw bytes.Buffer
		code := health(c, func(context.Context) (core.Snapshot, error) { return s, err }, func(context.Context) (string, error) { return "main1", nil }, now, &b, &errw)
		return code, b.String()
	}

	It("A healthy queue passes", func() {
		code, out := check(core.Snapshot{InFlight: []core.Flight{{Run: core.Run{ID: "r"}, Started: now.Add(-time.Minute)}}}, nil)
		Expect(code).To(Equal(0))
		Expect(out).To(Equal("healthy\n"))
		code, _ = check(core.Snapshot{InFlight: []core.Flight{{Run: core.Run{ID: "old"}}}}, nil) // no start kept: not judged
		Expect(code).To(Equal(0))
	})

	It("An unreadable main after a failed land counts toward the alarm and is no pause", func() {
		code, out := check(core.Snapshot{LandFails: 3, LandErr: "cannot tell whether main holds c1: main unreadable"}, nil)
		Expect(code).To(Equal(3))
		Expect(out).To(Equal("ALARM: 3 consecutive failed landings (last: cannot tell whether main holds c1: main unreadable)\n"))
	})

	It("Failed landings under max_failures raise no alarm", func() {
		code, out := check(core.Snapshot{LandFails: 2, LandErr: "push refused"}, nil)
		Expect(code).To(Equal(0))
		Expect(out).To(Equal("healthy\n"))
	})

	It("max_failures failed landings in a row raise the alarm, and the queue is not paused", func() {
		code, out := check(core.Snapshot{LandFails: 3, LandErr: "push refused"}, nil)
		Expect(code).To(Equal(3))
		Expect(out).To(Equal("ALARM: 3 consecutive failed landings (last: push refused)\n"))
		c.Lander.MaxFailures = 5
		defer func() { c.Lander.MaxFailures = 3 }()
		code, _ = check(core.Snapshot{LandFails: 4}, nil)
		Expect(code).To(Equal(0), "the alarm follows lander.max_failures")
	})

	It("The alarm hides secrets in the last error, and shortens it", func() {
		_, out := check(core.Snapshot{LandFails: 3, LandErr: "push https://user:SECRET@host/repo failed\n" + strings.Repeat("x", 500)}, nil)
		Expect(out).NotTo(ContainSubstring("SECRET"))
		Expect(strings.Count(out, "\n")).To(Equal(1))
		Expect(len(out)).To(BeNumerically("<", 250))
	})

	It("The alarm and a pause are both reported", func() {
		code, out := check(core.Snapshot{LandFails: 3, LandErr: "e", Paused: true, Why: "w", PausedAt: now.Add(-6 * time.Minute)}, nil)
		Expect(code).To(Equal(3))
		Expect(out).To(HavePrefix("ALARM: 3 consecutive failed landings (last: e)\n"))
		Expect(out).To(ContainSubstring("unhealthy: paused for 6m0s"))
	})

	It("An unreadable queue state is unknown, not unhealthy, and prints nothing", func() {
		code, out := check(core.Snapshot{}, errors.New("bad json"))
		Expect(code).To(Equal(1), "neither 0 healthy nor 3 unhealthy")
		Expect(out).To(BeEmpty())
	})

	It("The help names the exit codes", func() {
		var o, errw bytes.Buffer
		Expect(run(context.Background(), []string{"health", "--help"}, &o, &errw)).To(Equal(0))
		Expect(o.String()).To(BeEmpty())
		for _, want := range []string{"exit codes:", "0 healthy", "3 unhealthy", "any other non-zero: the state could not be read", "nothing on stdout"} {
			Expect(errw.String()).To(ContainSubstring(want))
		}
	})

	It("A pause that nothing resumes is unhealthy after five minutes", func() {
		s := core.Snapshot{Paused: true, Why: "land failed", PausedAt: now.Add(-4 * time.Minute)}
		code, _ := check(s, nil)
		Expect(code).To(Equal(0))
		s.PausedAt = now.Add(-6 * time.Minute)
		code, out := check(s, nil)
		Expect(code).To(Equal(3))
		Expect(out).To(Equal("unhealthy: paused for 6m0s: land failed; nothing auto-resumes it; run `queue resume`\n"))
	})

	It("An overdue auto-resume is unhealthy", func() {
		s := core.Snapshot{Paused: true, Why: "no verdict after 2 tries", PausedAt: now.Add(-6 * time.Minute)}
		code, _ := check(s, nil)
		Expect(code).To(Equal(0)) // cool-down over, grace not yet
		s.PausedAt = now.Add(-10 * time.Minute)
		code, out := check(s, nil)
		Expect(code).To(Equal(3))
		Expect(out).To(Equal("unhealthy: paused for 10m0s: auto-resumes after 5m0s, but is overdue\n"))
	})

	It("A no-verdict pause with no cool-down says nothing auto-resumes it", func() {
		c.Pause.Cooldown = 0
		DeferCleanup(func() { c.Pause.Cooldown = 5 * time.Minute })
		code, out := check(core.Snapshot{Paused: true, Why: "no verdict after 2 tries", PausedAt: now.Add(-10 * time.Minute)}, nil)
		Expect(code).To(Equal(3))
		Expect(out).To(ContainSubstring("nothing auto-resumes it; run `queue resume`"))
	})

	It("A no-verdict pause on the main its one auto-resume was spent on is unhealthy at once", func() {
		s := core.Snapshot{Paused: true, Why: "no verdict after 2 tries", PausedAt: now.Add(-time.Minute), ResumedOnMain: "main0"}
		code, _ := check(s, nil)
		Expect(code).To(Equal(0), "a new main still auto-resumes it")
		s.ResumedOnMain = "main1"
		code, out := check(s, nil)
		Expect(code).To(Equal(3))
		Expect(out).To(ContainSubstring("nothing auto-resumes it; run `queue resume`"))
		code, out = check(core.Snapshot{Paused: true, Why: "no verdict twice on main main1; held", PausedAt: now.Add(-time.Minute)}, nil)
		Expect(code).To(Equal(3))
		Expect(out).To(ContainSubstring("nothing auto-resumes it; run `queue resume`"))
	})

	It("A run in flight too long is unhealthy", func() {
		code, out := check(core.Snapshot{InFlight: []core.Flight{{Run: core.Run{ID: "run7"}, Started: now.Add(-2 * time.Hour)}}}, nil)
		Expect(code).To(Equal(3))
		Expect(out).To(Equal("unhealthy: run run7 in flight for 2h0m0s, over 1h0m0s\n"))
	})
})
