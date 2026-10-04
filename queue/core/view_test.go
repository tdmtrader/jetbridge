package core_test

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/queue/core"
)

var _ = Describe("PanelView", func() {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	render := func(s core.Snapshot) (map[string]any, []byte) {
		b, err := core.PanelView(s, core.Stats(s, now, time.Hour), now)
		ExpectWithOffset(1, err).NotTo(HaveOccurred())
		var v map[string]any
		ExpectWithOffset(1, json.Unmarshal(b, &v)).To(Succeed())
		return v, b
	}
	group := func(v map[string]any, key string) map[string]any {
		for _, g := range v["groups"].([]any) {
			if g.(map[string]any)["key"] == key {
				return g.(map[string]any)
			}
		}
		Fail("no group " + key)
		return nil
	}
	rows := func(g map[string]any) []any { r, _ := g["rows"].([]any); return r }
	rec := func(id string, k core.EventKind, why, cause string) core.SettleRecord {
		return core.SettleRecord{ID: id, Commit: "c" + id, Kind: k, At: now.Add(-time.Minute), AdmittedAt: now.Add(-time.Hour), Why: why, Cause: cause}
	}

	// The decoder is ResourceViewV1.elm decodeView/decodeGroup/decodeRow/decodeTile/decodeColumn
	// (lines 117-200 of fork ref ci/atc-integration-c-2026-10-01): "schema" is the one required
	// view field and must be "view/v1"; badge, badge_style, summary, banner are strings; as_of and
	// stale_after are ints; columns need a string "key"; legend items a string "style"; groups a
	// string "key" and rows each a string "id" with cells holding strings, ints or floats; tiles a
	// string "label", value string/number, detail string, alert bool. A present field of the wrong type drops the view.
	It("The queue publishes its state in the panel's view format", func() {
		s := core.Snapshot{
			Queued:   []core.Entry{{ID: "a", Commit: "ca", AdmittedAt: now}, {ID: "b", Commit: "cb", AdmittedAt: now}},
			BuildsOn: map[string][]string{"b": {"a"}},
			InFlight: []core.Flight{{Run: core.Run{ID: "r1"}, Candidate: "cand"}},
			Settled:  []core.SettleRecord{rec("old", core.LandedEvent, "", "")},
		}
		v, b := render(s)
		Expect(len(b)).To(BeNumerically("<=", core.MaxViewBytes))
		Expect(v["schema"]).To(Equal("view/v1"))
		for _, k := range []string{"badge", "badge_style", "summary", "banner"} {
			Expect(v[k]).To(BeAssignableToTypeOf(""), k)
		}
		for _, k := range []string{"as_of", "stale_after"} {
			Expect(v[k].(float64)).To(Equal(float64(int64(v[k].(float64)))), k)
		}
		for _, c := range v["columns"].([]any) {
			Expect(c.(map[string]any)["key"]).To(BeAssignableToTypeOf(""))
			if ty, ok := c.(map[string]any)["type"]; ok {
				Expect(ty).To(BeElementOf("text", "age", "sha"))
			}
		}
		for _, l := range v["legend"].([]any) {
			Expect(l.(map[string]any)["style"]).To(BeElementOf("green", "blue", "grey", "red", "amber", "dim", "bold"))
		}
		keys := []string{}
		for _, g := range v["groups"].([]any) {
			gm := g.(map[string]any)
			keys = append(keys, gm["key"].(string))
			for _, r := range rows(gm) {
				rm := r.(map[string]any)
				Expect(rm["id"]).To(BeAssignableToTypeOf(""))
				for _, c := range rm["cells"].(map[string]any) {
					Expect(c).To(BeAssignableToTypeOf(""))
				}
			}
		}
		Expect(keys).To(Equal([]string{"testing", "queued", "landed", "ejected"}))
		Expect(v["tiles"]).To(HaveLen(7))
		for _, t := range v["tiles"].([]any) {
			tm := t.(map[string]any)
			Expect(tm["label"]).To(BeAssignableToTypeOf(""))
			Expect(tm["value"]).To(BeAssignableToTypeOf(""))
			if a, ok := tm["alert"]; ok {
				Expect(a).To(BeAssignableToTypeOf(true))
			}
		}
		Expect(rows(group(v, "testing"))).To(HaveLen(1))
		q := rows(group(v, "queued"))
		Expect(q).To(HaveLen(2))
		Expect(q[1].(map[string]any)["cells"]).To(HaveKeyWithValue("note", "builds on a"))
		Expect(rows(group(v, "landed"))).To(HaveLen(1))
		for _, w := range []string{"tra" + "ck", "tic" + "ket", "work" + "flow", "play" + "book", "an" + "vil", "solle" + "va", "hear" + "th"} {
			Expect(strings.ToLower(string(b))).NotTo(ContainSubstring(w))
		}
	})

	It("A paused queue shows a banner with the reason", func() {
		v, _ := render(core.Snapshot{Paused: true, Why: "main is red"})
		Expect(v["banner"]).To(ContainSubstring("main is red"))
		Expect(v["badge"]).To(Equal("paused"))
		v, _ = render(core.Snapshot{})
		Expect(v["banner"]).To(Equal(""))
	})

	It("A very long list is cut with a \"+N more\" row under the size cap", func() {
		s := core.Snapshot{}
		for i := range 5000 {
			s.Settled = append(s.Settled, rec(fmt.Sprintf("change-%04d", i), core.LandedEvent, strings.Repeat("because ", 8), ""))
		}
		v, b := render(s)
		Expect(len(b)).To(BeNumerically("<=", core.MaxViewBytes))
		r := rows(group(v, "landed"))
		Expect(r[len(r)-1].(map[string]any)["id"]).To(MatchRegexp(`^\+\d+ more$`))
		Expect(r[0].(map[string]any)["id"]).To(Equal("change-4999"))
		_, again := render(s)
		Expect(again).To(Equal(b))
		Expect(regexp.MustCompile(`\+\d+ more`).Match(b)).To(BeTrue())
	})

	It("A credential inside a reason is hidden in the view", func() {
		why := "landing failed 3 times: git push: remote: denied for https://user:SECRET@host/repo"
		s := core.Snapshot{Paused: true, Why: why, Settled: []core.SettleRecord{rec("a", core.EjectedEvent, why, "culprit")}}
		_, b := render(s)
		Expect(string(b)).NotTo(ContainSubstring("SECRET"))
		Expect(string(b)).To(ContainSubstring("denied for https://***@host/repo"))
	})

	It("A list one row over the panel's row limit still ends with its marker", func() {
		s := core.Snapshot{}
		for i := range 201 {
			s.Settled = append(s.Settled, rec(fmt.Sprint("c", i), core.LandedEvent, "", ""))
		}
		v, _ := render(s)
		r := rows(group(v, "landed"))
		Expect(r).To(HaveLen(200))
		Expect(r[199].(map[string]any)["id"]).To(Equal("+2 more"))
	})

	It("An ejected flake is shown with its reason", func() {
		v, _ := render(core.Snapshot{Settled: []core.SettleRecord{
			rec("bad", core.EjectedEvent, "failed on its own", "culprit"),
			rec("flaky-one", core.FlakeEvent, "failed as a batch, passed in parts", ""),
		}})
		r := rows(group(v, "ejected"))
		Expect(r).To(HaveLen(2))
		Expect(r[0].(map[string]any)["cells"]).To(HaveKeyWithValue("note", "flake: failed as a batch, passed in parts"))
		Expect(r[1].(map[string]any)["cells"]).To(HaveKeyWithValue("note", "culprit: failed on its own"))
	})

	It("An empty queue publishes a valid view", func() {
		v, _ := render(core.Snapshot{})
		for _, g := range v["groups"].([]any) {
			Expect(g.(map[string]any)["empty"]).NotTo(BeEmpty())
			Expect(rows(g.(map[string]any))).To(BeEmpty())
		}
	})
})

var _ = Describe("Redact", func() {
	It("hides only the userinfo inside a URL's authority", func() {
		for in, want := range map[string]string{
			"https://host?contact=dev@example.com":                         "https://host?contact=dev@example.com",
			"https://host/x#dev@example.com":                               "https://host/x#dev@example.com",
			"https://user:pw@host/x":                                       "https://***@host/x",
			"ssh://git@host/x":                                             "ssh://***@host/x",
			"git@host:path":                                                "git@host:path",
			`parse "https://user:pw@host/%zz"`:                             `parse "https://***@host/%zz"`,
			`{"url":"https://host","contact":"dev@example.com"}`:           `{"url":"https://host","contact":"dev@example.com"}`,
			`{"url":"https://user:pw@host/x","contact":"dev@example.com"}`: `{"url":"https://***@host/x","contact":"dev@example.com"}`,
			"https://user:p@ss@host/x":                                     "https://***@host/x",
		} {
			Expect(core.Redact(in)).To(Equal(want), in)
		}
	})
})
