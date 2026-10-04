package main

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/queue/core"
)

var _ = Describe("read verbs", func() {
	at := time.Date(2026, 10, 3, 14, 30, 0, 0, time.UTC)
	snap := core.Snapshot{
		Queued:   []core.Entry{{ID: "a", Commit: "aaaaaaaaaaaa", Ref: "r/a", AdmittedAt: at}, {ID: "b", Commit: "bbbbbbbbbbbb", AdmittedAt: at}},
		BuildsOn: map[string][]string{"b": {"a"}},
		Ejected:  map[string]bool{"x": true, "y": true},
		Commits:  map[string]string{"x": "xxxxxxxxxxxx"},
		InFlight: []core.Flight{{Run: core.Run{ID: "run1", Entries: []core.Entry{{ID: "a"}}}}},
		Settled: []core.SettleRecord{
			{ID: "x", Kind: core.EjectedEvent, At: at, Why: "failed alone", Cause: "culprit", Run: "run0", AdmittedAt: at},
			{ID: "y", Kind: core.EjectedEvent, At: at.Add(time.Hour), Why: "parent", Cause: "ParentEjected"},
			{ID: "z", Kind: core.LandedEvent, At: at},
		},
		Refused: []core.Refusal{{ID: "x", Commit: "x0", Why: "unsafe id"}},
	}
	read := func(verb, id string, asJSON bool) (string, error) {
		var b bytes.Buffer
		err := readVerb(verb, snap, id, asJSON, &b)
		return b.String(), err
	}

	It("The list shows queued changes in order with what they build on", func() {
		out, err := read("list", "", false)
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(Equal("a aaaaaaaa admitted 2026-10-03T14:30:00Z builds-on []\nb bbbbbbbb admitted 2026-10-03T14:30:00Z builds-on [a]\n"))
		j, _ := read("list", "", true)
		var rows []listed
		Expect(json.Unmarshal([]byte(j), &rows)).To(Succeed())
		Expect(rows).To(HaveLen(2))
		Expect(rows[1].BuildsOn).To(Equal([]string{"a"}))
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
