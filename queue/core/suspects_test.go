package core_test

import (
	"context"
	"fmt"
	"slices"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/queue/core"
)

// hinted hints its Serial as the driver does and keeps every Outcome's notes.
type hinted struct {
	*core.Serial
	failed  []string
	changed map[string][]string
	notes   []string
}

func (h *hinted) Record(v core.View, run string, verdict core.Verdict) (core.Outcome, error) {
	if verdict == core.Fail && h.WantsHint(run) {
		h.Hint(run, h.failed, h.changed)
	}
	out, err := h.Serial.Record(v, run, verdict)
	h.notes = append(h.notes, out.Notes...)
	return out, err
}

// touched: each change touched its own directory.
var touched = map[string][]string{"a": {"pkg/a/a.go"}, "b": {"pkg/b/b.go"}, "c": {"pkg/c/c.go"}, "d": {"pkg/d/d.go"}}

type namingRunner struct {
	*memRunner
	names []string
}

func (r *namingRunner) FailedTests(string) []string { return r.names }

type listingComposer struct {
	*memComposer
	calls int
}

func (c *listingComposer) Changed(_ context.Context, es []core.Entry) (map[string][]string, error) {
	c.calls++
	return touched, nil
}

var _ = Describe("Suspects", func() {
	pol := core.Policy{RetryNone: 1}
	serial := func(failed ...string) *hinted {
		return &hinted{Serial: &core.Serial{Max: 4, Policy: pol}, failed: failed, changed: touched}
	}
	// ejecting says a, b, c and d land but for the ejected ones.
	ejecting := func(ids ...string) map[string]core.Decision {
		out := map[string]core.Decision{"a": core.Land, "b": core.Land, "c": core.Land, "d": core.Land}
		for _, id := range ids {
			out[id] = core.Eject
		}
		return out
	}

	It("A correct hint ejects after one solo run without bisecting", func() {
		h := serial("pkg/c TestC")
		runs, settled := drive(h, ents("a", "b", "c", "d"), nil, failsWith("c"))
		Expect(runs).To(Equal([][]string{{"a", "b", "c", "d"}, {"c"}, {"a", "b", "d"}}))
		Expect(decisions(settled)).To(Equal(ejecting("c")))
		Expect(settled["c"].Cause).To(Equal(core.Culprit))
		Expect(h.notes).To(Equal([]string{`failed tests point at "c"; running it alone first`}))
	})

	It("A wrong hint lands the innocent suspect, then bisect finds the culprit among the rest", func() {
		runs, settled := drive(serial("pkg/a TestA"), ents("a", "b", "c", "d"), nil, failsWith("c"))
		Expect(runs).To(Equal([][]string{{"a", "b", "c", "d"}, {"a"}, {"b"}, {"c", "d"}, {"c"}, {"d"}}))
		Expect(decisions(settled)).To(Equal(ejecting("c")))
	})

	It("records a flake on the whole batch when the suspect and the rest all pass", func() {
		h := serial("pkg/b TestB")
		runs, settled := drive(h, ents("a", "b", "c", "d"), nil, func(r []string) core.Verdict {
			return map[bool]core.Verdict{true: core.Fail, false: core.Pass}[len(r) == 4]
		})
		Expect(runs).To(Equal([][]string{{"a", "b", "c", "d"}, {"b"}, {"a"}, {"c", "d"}}))
		Expect(decisions(settled)).To(Equal(ejecting()))
		Expect(h.Flakes).To(Equal([][]core.Entry{ents("a", "b", "c", "d")}))
	})

	It("A stacked suspect runs with the changes it builds on, never alone", func() {
		runs, settled := drive(serial("pkg/c TestC"), ents("a", "b", "c", "d"), map[string][]string{"c": {"b"}}, failsWith("d"))
		Expect(runs).To(Equal([][]string{{"a", "b", "c", "d"}, {"b", "c"}, {"a"}, {"d"}}))
		Expect(decisions(settled)).To(Equal(ejecting("d")))
	})

	It("a red stacked suspect run ejects nobody and the whole batch is bisected", func() {
		runs, settled := drive(serial("pkg/c TestC"), ents("a", "b", "c", "d"), map[string][]string{"c": {"b"}}, failsWith("b"))
		Expect(runs[:3]).To(Equal([][]string{{"a", "b", "c", "d"}, {"b", "c"}, {"a", "b"}}))
		Expect(decisions(settled)).To(Equal(ejecting("b", "c")))
		Expect(settled["b"].Cause).To(Equal(core.Culprit))
	})

	It("Under proven-first a green suspect lands out of arrival order", func() {
		for _, order := range []core.Order{core.ProvenFirst, ""} {
			h := serial("pkg/b TestB")
			h.Policy.Order = order
			runs, settled := drive(h, ents("a", "b", "c", "d"), nil, failsWith("c"))
			Expect(runs).To(Equal([][]string{{"a", "b", "c", "d"}, {"b"}, {"a"}, {"c", "d"}, {"c"}, {"d"}}))
			Expect(decisions(settled)).To(Equal(ejecting("c")))
		}
	})

	It("Under strict order a suspect runs alone only if it is first", func() {
		h := serial("pkg/b TestB")
		h.Policy.Order = core.Strict
		runs, settled := drive(h, ents("a", "b", "c", "d"), nil, failsWith("c"))
		Expect(runs).To(Equal([][]string{{"a", "b", "c", "d"}, {"a", "b"}, {"c", "d"}, {"c"}, {"d"}}))
		Expect(decisions(settled)).To(Equal(ejecting("c")))
		Expect(h.notes).To(Equal([]string{`suspect "b" is not first; strict order bisects by halves`}))
		h = serial("pkg/a TestA")
		h.Policy.Order = core.Strict
		runs, _ = drive(h, ents("a", "b", "c", "d"), nil, failsWith("c"))
		Expect(runs).To(Equal([][]string{{"a", "b", "c", "d"}, {"a"}, {"b"}, {"c", "d"}, {"c"}, {"d"}}))
	})

	It("With no failed test names the batch is split in halves and the log says so", func() {
		h := serial()
		runs, _ := drive(h, ents("a", "b", "c", "d"), nil, failsWith("c"))
		Expect(runs).To(Equal([][]string{{"a", "b", "c", "d"}, {"a", "b"}, {"c", "d"}, {"c"}, {"d"}}))
		Expect(h.notes).To(Equal([]string{"no failed-test names; bisecting by halves"}))
		h = serial("TestNothingTouched")
		runs, _ = drive(h, ents("a", "b", "c", "d"), nil, failsWith("c"))
		Expect(runs).To(HaveLen(5))
		Expect(h.notes).To(Equal([]string{"failed tests point at no change; bisecting by halves"}))
	})

	It("the driver hands the runner's failed test names and the changed files to the strategy", func() {
		for _, names := range [][]string{{"pkg/c TestC"}, nil} {
			comp := &listingComposer{memComposer: &memComposer{}}
			run := &namingRunner{memRunner: &memRunner{verdict: failsWith("c")}, names: names}
			var logs []string
			d := &core.Driver{
				Store: &memStore{}, Composer: comp, Runner: run, Lander: &memLander{c: comp.memComposer}, Notifier: &memNotifier{},
				NewStrategy: func() core.Strategy { return &core.Serial{Max: 4, Policy: pol} },
				Main:        "core", Log: func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) },
			}
			for _, id := range []string{"a", "b", "c", "d"} {
				Expect(d.Admit(context.Background(), entry(id))).To(Succeed())
			}
			for range 12 {
				Expect(d.Step(context.Background())).To(Succeed())
			}
			hints := slices.DeleteFunc(logs, func(l string) bool { return !strings.Contains(l, "serial-1: ") })
			Expect(comp.calls).To(Equal(len(names)))
			if names != nil {
				Expect(run.starts).To(Equal([][]string{{"a", "b", "c", "d"}, {"c"}, {"a", "b", "d"}}))
				Expect(hints).To(ConsistOf(HaveSuffix(`serial-1: failed tests point at "c"; running it alone first`)))
			} else {
				Expect(run.starts).To(HaveLen(5))
				Expect(hints).To(ConsistOf(HaveSuffix("serial-1: no failed-test names; bisecting by halves")))
			}
		}
	})
})
