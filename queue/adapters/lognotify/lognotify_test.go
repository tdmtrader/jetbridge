package lognotify_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.yaml.in/yaml/v3"

	"github.com/concourse/concourse/queue/adapters/lognotify"
	"github.com/concourse/concourse/queue/core"
)

type failing struct{}

func (failing) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func section(src string) *yaml.Node {
	var doc yaml.Node
	Expect(yaml.Unmarshal([]byte(src), &doc)).To(Succeed())
	return doc.Content[0]
}

var _ = Describe("Notifier", func() {
	var out bytes.Buffer
	var n *lognotify.Notifier
	BeforeEach(func() { out.Reset(); n = lognotify.New(&out) })

	lines := func() []map[string]any {
		var got []map[string]any
		for _, l := range strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n") {
			var m map[string]any
			Expect(json.Unmarshal([]byte(l), &m)).To(Succeed())
			got = append(got, m)
		}
		return got
	}

	It("An ejected change is written to the log with its reason", func() {
		e := core.Event{Kind: core.EjectedEvent, Entries: []core.Entry{{ID: "c1"}}, Run: core.Run{ID: "r7"},
			Verdict: "fail", Why: "tests failed", Cause: "build-failed"}
		Expect(n.Notify(context.Background(), e)).To(Succeed())
		Expect(out.String()).To(HaveSuffix("\n"))
		m := lines()[0]
		Expect(m).To(HaveKeyWithValue("kind", "ejected"))
		Expect(m).To(HaveKeyWithValue("why", "tests failed"))
		Expect(m).To(HaveKeyWithValue("cause", "build-failed"))
		Expect(m).To(HaveKeyWithValue("run", "r7"))
		Expect(m).To(HaveKeyWithValue("verdict", "fail"))
		Expect(m["entries"]).To(Equal([]any{map[string]any{"id": "c1"}}))
		Expect(m["time"]).To(MatchRegexp(`^\d{4}-\d\d-\d\dT[\d:.]+Z$`))
	})

	It("writes the time the event carries", func() {
		at := time.Date(2026, 10, 3, 9, 30, 0, 0, time.FixedZone("x", -5*3600))
		Expect(n.Notify(context.Background(), core.Event{Kind: core.LandedEvent, At: at})).To(Succeed())
		Expect(lines()[0]).To(HaveKeyWithValue("time", "2026-10-03T14:30:00Z"))
	})

	It("writes one line per event", func() {
		for range 3 {
			Expect(n.Notify(context.Background(), core.Event{Kind: core.PausedEvent})).To(Succeed())
		}
		Expect(lines()).To(HaveLen(3))
	})

	It("round-trips every kind", func() {
		kinds := []core.EventKind{core.BatchStarted, core.VerdictIn, core.LandedEvent, core.EjectedEvent,
			core.FlakeEvent, core.PausedEvent, core.ResumedEvent}
		for _, k := range kinds {
			Expect(n.Notify(context.Background(), core.Event{Kind: k, Entries: []core.Entry{{ID: "a"}, {ID: "b"}}, Parent: "p"})).To(Succeed())
		}
		for i, m := range lines() {
			Expect(m).To(HaveKeyWithValue("kind", string(kinds[i])))
			Expect(m["entries"]).To(Equal([]any{map[string]any{"id": "a"}, map[string]any{"id": "b"}}))
			Expect(m).To(HaveKeyWithValue("parent", "p"))
		}
	})

	It("A started batch is written to the log with every change in full", func() {
		at := time.Date(2026, 10, 3, 9, 30, 0, 0, time.FixedZone("x", -5*3600))
		a := core.Entry{ID: "c1", Commit: "abc", Ref: "refs/heads/f", AdmittedAt: at}
		b := core.Entry{ID: "c2", Commit: "def"}
		e := core.Event{Kind: core.BatchStarted, Entries: []core.Entry{a, b},
			Run: core.Run{ID: "r1", Base: "r0", Entries: []core.Entry{a, b}}}
		Expect(n.Notify(context.Background(), e)).To(Succeed())
		m := lines()[0]
		Expect(m["entries"]).To(Equal([]any{
			map[string]any{"id": "c1", "commit": "abc", "ref": "refs/heads/f", "admitted_at": "2026-10-03T14:30:00Z"},
			map[string]any{"id": "c2", "commit": "def"},
		}))
		Expect(m).To(HaveKeyWithValue("run", "r1"))
		Expect(m).To(HaveKeyWithValue("run_base", "r0"))
		Expect(m["run_entries"]).To(Equal(m["entries"]))
	})

	It("Concurrent events each get their own whole line", func() {
		const count = 50
		var wg sync.WaitGroup
		for i := range count {
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer GinkgoRecover()
				e := core.Event{Kind: core.VerdictIn, Why: strings.Repeat("w", 2000), Run: core.Run{ID: fmt.Sprint("r", i)}}
				Expect(n.Notify(context.Background(), e)).To(Succeed())
			}()
		}
		wg.Wait()
		seen := map[any]bool{}
		for _, m := range lines() {
			seen[m["run"]] = true
		}
		Expect(seen).To(HaveLen(count))
	})

	It("A multiline reason stays on one line", func() {
		Expect(n.Notify(context.Background(), core.Event{Kind: core.EjectedEvent, Why: "first\nsecond\r\nthird"})).To(Succeed())
		Expect(strings.Count(out.String(), "\n")).To(Equal(1))
		Expect(lines()[0]).To(HaveKeyWithValue("why", "first\nsecond\r\nthird"))
	})

	It("returns a write error", func() {
		Expect(lognotify.New(failing{}).Notify(context.Background(), core.Event{Kind: core.LandedEvent})).To(MatchError("disk full"))
	})
})

var _ = Describe("Parse and Open", func() {
	It("reads path - as stdout", func() {
		c, err := lognotify.Parse(section("{kind: log, path: \"-\"}"))
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Path).To(Equal("-"))
	})

	It("opens a file append-only, creating it", func() {
		path := filepath.Join(GinkgoT().TempDir(), "q.log")
		for range 2 {
			c, err := lognotify.Parse(section("{kind: log, path: " + path + "}"))
			Expect(err).NotTo(HaveOccurred())
			n, err := lognotify.Open(c)
			Expect(err).NotTo(HaveOccurred())
			Expect(n.Notify(context.Background(), core.Event{Kind: core.ResumedEvent})).To(Succeed())
			Expect(n.Close()).To(Succeed())
		}
		b, _ := os.ReadFile(path)
		Expect(strings.Count(string(b), "\n")).To(Equal(2))
		st, _ := os.Stat(path)
		Expect(st.Mode().Perm()).To(Equal(os.FileMode(0o644)))
	})

	It("refuses an unknown kind", func() {
		_, err := lognotify.Parse(section("{kind: slack, path: x}"))
		Expect(err).To(MatchError(ContainSubstring(`notify.kind "slack" is not allowed`)))
	})

	It("refuses a missing path and an unknown key", func() {
		_, err := lognotify.Parse(section("{kind: log}"))
		Expect(err).To(MatchError(ContainSubstring("notify.path is required")))
		_, err = lognotify.Parse(section("{kind: log, pth: x}"))
		Expect(err).To(MatchError(ContainSubstring(`did you mean "notify.path"`)))
	})
})
