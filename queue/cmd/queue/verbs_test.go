package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/queue/adapters/git"
	"github.com/concourse/concourse/queue/config"
	"github.com/concourse/concourse/queue/core"
)

var _ = Describe("read verbs", func() {
	at := time.Date(2026, 10, 3, 14, 30, 0, 0, time.UTC)
	snap := core.Snapshot{
		Version:  "gen7",
		Queued:   []core.Entry{{ID: "a", Commit: "aaaaaaaaaaaa", Ref: "r/a", AdmittedAt: at}, {ID: "b", Commit: "bbbbbbbbbbbb", AdmittedAt: at}},
		BuildsOn: map[string][]string{"b": {"a"}},
		Ejected:  map[string]bool{"x": true, "y": true},
		Commits:  map[string]string{"x": "xxxxxxxxxxxx"},
		InFlight: []core.Flight{{Run: core.Run{ID: "run1", Base: "run0", Entries: []core.Entry{{ID: "a"}}}, Candidate: "cccccccccccc"}},
		Settled: []core.SettleRecord{
			{ID: "x", Kind: core.EjectedEvent, At: at, Why: "failed alone", Cause: "culprit", Run: "run0", AdmittedAt: at},
			{ID: "y", Kind: core.EjectedEvent, At: at.Add(time.Hour), Why: "parent", Cause: "ParentEjected"},
			{ID: "z", Kind: core.LandedEvent, At: at},
		},
		Refused: []core.Refusal{{ID: "x", Commit: "x0", Why: "unsafe id"}},
	}
	read := func(verb, id string, asJSON bool) (string, error) {
		var b bytes.Buffer
		err := readVerb(verb, "trunk", snap, id, asJSON, &b)
		return b.String(), err
	}

	It("The list shows queued changes in order with what they build on", func() {
		out, err := read("list", "", false)
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(Equal("main trunk generation gen7\n" +
			"a aaaaaaaa admitted 2026-10-03T14:30:00Z builds-on []\n" +
			"b bbbbbbbb admitted 2026-10-03T14:30:00Z builds-on [a]\n" +
			"in flight run1 base \"run0\" candidate cccccccc: [a]\n" +
			"ejected x\nejected y\n"))
		j, _ := read("list", "", true)
		var l queueList
		Expect(json.Unmarshal([]byte(j), &l)).To(Succeed())
		Expect(l.Queued).To(HaveLen(2))
		Expect(l.Queued[1].BuildsOn).To(Equal([]string{"a"}))
		Expect(l.Main).To(Equal("trunk"))
		Expect(l.Generation).To(Equal("gen7"))
		Expect(l.InFlight).To(Equal([]flying{{"run1", "run0", "cccccccccccc", []string{"a"}}}))
		Expect(l.Ejected).To(Equal([]string{"x", "y"}))
	})

	It("The list also shows main, the generation, runs in flight and open ejects", func() {
		out, _ := read("list", "", false)
		Expect(out).To(ContainSubstring("main trunk generation gen7"))
		Expect(out).To(ContainSubstring("in flight run1"))
		Expect(out).To(ContainSubstring("ejected y"))
	})

	It("The list reads a saved queue end to end", func() {
		ctx, dir := context.Background(), GinkgoT().TempDir()
		remote := filepath.Join(dir, "remote.git")
		Expect(exec.Command("git", "init", "-q", "--bare", remote).Run()).To(Succeed())
		file := filepath.Join(dir, "queue.yaml")
		Expect(os.WriteFile(file, fmt.Appendf(nil, sample, remote, dir), 0o600)).To(Succeed())
		c, err := config.Parse(fmt.Appendf(nil, sample, remote, dir))
		Expect(err).NotTo(HaveOccurred())
		store := git.NewStore(c)
		l, err := store.Acquire(ctx, "runner", time.Minute)
		Expect(err).NotTo(HaveOccurred())
		st, err := store.Load(ctx)
		Expect(err).NotTo(HaveOccurred())
		st.Queued = []core.Entry{{ID: "a", Commit: "aaaaaaaaaaaa", AdmittedAt: at}}
		st.Ejected = map[string]bool{"x": true}
		saved, err := store.Save(ctx, l.Token, st)
		Expect(err).NotTo(HaveOccurred())
		var o, e bytes.Buffer
		Expect(run(ctx, []string{"list", "--config", file}, &o, &e)).To(Equal(0), e.String())
		Expect(o.String()).To(ContainSubstring("main trunk generation " + saved))
		Expect(o.String()).To(ContainSubstring("a aaaaaaaa admitted"))
		Expect(o.String()).To(ContainSubstring("ejected x"))
		after, err := store.Load(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(after.Version).To(Equal(saved))
	})

	It("The ejected list shows recent ejects with why, cause and when", func() {
		out, _ := read("ejected", "", false)
		Expect(out).To(Equal("y why \"parent\" cause \"ParentEjected\" at 2026-10-03T15:30:00Z\nx why \"failed alone\" cause \"culprit\" at 2026-10-03T14:30:00Z\n"))
		j, _ := read("ejected", "", true)
		var rows []ejection
		Expect(json.Unmarshal([]byte(j), &rows)).To(Succeed())
		Expect(rows).To(HaveLen(2))
		Expect(rows[0].ID).To(Equal("y"))
	})

	It("An eject keeps the failing test names and the build they came from", func() {
		s := snap
		s.Settled = []core.SettleRecord{{ID: "x", Kind: core.EjectedEvent, Failure: core.Failure{Failed: []string{"a", "b", "c", "d", "e", "f"}, FailedOn: "job t build 7"}}}
		for _, verb := range []string{"ejected", "explain"} {
			var b bytes.Buffer
			Expect(readVerb(verb, "", s, "x", false, &b)).To(Succeed())
			Expect(b.String()).To(ContainSubstring("  failed: a, b, c, d, e (+1 more) on job t build 7\n"))
		}
	})

	It("Explain shows one change's admit, runs, settle records and refusals", func() {
		out, err := read("explain", "x", false)
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(ContainSubstring("x: ejected, commit xxxxxxxx"))
		Expect(out).To(ContainSubstring(`settle ejected at 2026-10-03T14:30:00Z why "failed alone" cause "culprit" run "run0"`))
		Expect(out).To(ContainSubstring("refused x0: unsafe id"))
		out, _ = read("explain", "a", false)
		Expect(out).To(ContainSubstring("a: queued"))
		Expect(out).To(ContainSubstring("run run1"))
		j, _ := read("explain", "x", true)
		var h history
		Expect(json.Unmarshal([]byte(j), &h)).To(Succeed())
		Expect(h.Settled).To(HaveLen(1))
		Expect(h.Refused).To(HaveLen(1))
	})

	It("Explaining an unknown change fails clearly", func() {
		_, err := read("explain", "nope", false)
		Expect(err).To(MatchError(ContainSubstring(`no entry "nope"`)))
	})

	It("is read-only and redacted through the command", func() {
		var o, e bytes.Buffer
		Expect(run(context.Background(), []string{"list"}, &o, &e)).To(Equal(1)) // needs --config
		Expect(run(context.Background(), []string{"explain", "--config", "/nonexistent"}, &o, &e)).To(Equal(1))
	})
})
