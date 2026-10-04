package main

import (
	"bytes"
	"context"
	"errors"
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
		code := health(c, func(context.Context) (core.Snapshot, error) { return s, err }, func(context.Context) (string, error) { return "main1", nil }, now, &b)
		return code, b.String()
	}

	It("A healthy queue passes", func() {
		code, out := check(core.Snapshot{InFlight: []core.Flight{{Run: core.Run{ID: "r"}, Started: now.Add(-time.Minute)}}}, nil)
		Expect(code).To(Equal(0))
		Expect(out).To(Equal("healthy\n"))
		code, _ = check(core.Snapshot{InFlight: []core.Flight{{Run: core.Run{ID: "old"}}}}, nil) // no start kept: not judged
		Expect(code).To(Equal(0))
	})

	It("A damaged queue state is unhealthy", func() {
		code, out := check(core.Snapshot{}, errors.New("bad json"))
		Expect(code).To(Equal(3))
		Expect(out).To(Equal("unhealthy: queue state damaged: bad json\n"))
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
