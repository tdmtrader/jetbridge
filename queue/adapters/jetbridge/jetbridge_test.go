package jetbridge_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.yaml.in/yaml/v3"

	"github.com/concourse/concourse/queue/adapters/jetbridge"
	"github.com/concourse/concourse/queue/config"
	"github.com/concourse/concourse/queue/core"
)

const (
	candidate = "c0ffee0000000000000000000000000000000001"
	older     = "c0ffee0000000000000000000000000000000002"
	base      = "/api/v1/teams/main/pipelines/demo-pipeline"
)

// fake is a pretend JetBridge: one check build (id 1), versions 8 (older)
// and 7 (candidate) behind any extra, and job builds from id 100 that end in
// final once polled running times. A build's input from the resource is the
// version whose ref is tested; inputs is what the build lists under its input
// names, which may be aliases. Unfiltered, versions is cut to its first 100.
type fake struct {
	mu          sync.Mutex
	final       string
	tested      string
	inputs      []map[string]any
	extra       int
	bindingDown bool
	buildDown   bool
	running     int
	builds      int
	calls       []string
	pages       [][]string // log payloads of the failed build's event pages, one slice per page
}

func (f *fake) versions(r *http.Request) []map[string]any {
	vs := []map[string]any{}
	for i := range f.extra {
		vs = append(vs, map[string]any{"id": 1000 + i, "version": map[string]string{"ref": fmt.Sprintf("%040d", i)}})
	}
	vs = append(vs, map[string]any{"id": 8, "version": map[string]string{"ref": older}}, map[string]any{"id": 7, "version": map[string]string{"ref": candidate}})
	if filter := r.URL.Query().Get("filter"); filter != "" {
		return slices.DeleteFunc(vs, func(v map[string]any) bool { return "ref:"+v["version"].(map[string]string)["ref"] != filter })
	}
	return vs[:min(len(vs), 100)]
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	if r.Header.Get("Authorization") != "Bearer fake-token" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	send := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	refs := map[string]string{"7": candidate, "8": older}
	switch p := r.URL.Path; {
	case p == base+"/resources/demo-repo/check":
		send(map[string]any{"id": 1, "status": "started"})
	case p == "/api/v1/builds/1":
		send(map[string]any{"id": 1, "status": "succeeded"})
	case p == base+"/resources/demo-repo/versions":
		send(f.versions(r))
	case strings.HasSuffix(p, "/pin"), p == base+"/resources/demo-repo/unpin":
	case f.buildDown && p == base+"/jobs/demo-job/builds":
		w.WriteHeader(http.StatusServiceUnavailable)
	case p == base+"/jobs/demo-job/builds":
		f.builds++
		send(map[string]any{"id": 99 + f.builds, "status": "pending"})
	case strings.HasSuffix(p, "/abort"):
		w.WriteHeader(http.StatusNoContent)
	case f.bindingDown && (strings.HasSuffix(p, "/input_to") || strings.HasSuffix(p, "/resources")):
		w.WriteHeader(http.StatusServiceUnavailable)
	case strings.HasSuffix(p, "/input_to"):
		builds := []map[string]any{}
		if id := strings.TrimSuffix(strings.TrimPrefix(p, base+"/resources/demo-repo/versions/"), "/input_to"); refs[id] == f.tested {
			for b := range f.builds {
				builds = append(builds, map[string]any{"id": 100 + b, "status": f.final})
			}
		}
		send(builds)
	case strings.HasSuffix(p, "/events") && r.URL.Query().Get("format") == "json":
		i, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Query().Get("cursor"), "p"))
		page := map[string]any{"events": []map[string]any{{"event": "status", "data": map[string]any{"status": "x"}}}, "finished": true, "caught_up": true}
		for _, payload := range f.pages[i] {
			page["events"] = append(page["events"].([]map[string]any), map[string]any{"event": "log", "version": "5.1", "data": map[string]any{"payload": payload}})
		}
		if i+1 < len(f.pages) {
			page["next_cursor"], page["caught_up"] = fmt.Sprintf("p%d", i+1), false
		}
		send(page)
	case strings.HasSuffix(p, "/resources") && strings.HasPrefix(p, "/api/v1/builds/"):
		inputs := f.inputs
		if inputs == nil {
			inputs = []map[string]any{{"name": "demo-repo", "version": map[string]string{"ref": f.tested}}}
		}
		send(map[string]any{"inputs": inputs})
	case strings.HasPrefix(p, "/api/v1/builds/"):
		status := f.final
		if f.running > 0 {
			f.running, status = f.running-1, "started"
		}
		send(map[string]any{"status": status})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fake) saw(call string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == call {
			return true
		}
	}
	return false
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

var _ = Describe("JetBridge runner", func() {
	var (
		f   *fake
		srv *httptest.Server
		clk *clock
		r   *jetbridge.Runner
		ctx = context.Background()
	)

	newRunner := func(url string) *jetbridge.Runner {
		GinkgoT().Setenv("FAKE_JB_TOKEN", "fake-token")
		r := jetbridge.New(jetbridge.Config{Kind: "jetbridge", URL: url, Team: "main", Pipeline: "demo-pipeline",
			Job: "demo-job", Resource: "demo-repo", Credential: "env:FAKE_JB_TOKEN", WaitCap: time.Hour}, func(string, ...any) {})
		r.Now = clk.now
		return r
	}

	It("A credential in a redirect on a best-effort unpin is hidden in the log", func() {
		clk = &clock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if strings.HasSuffix(req.URL.Path, "/unpin") {
				w.Header().Set("Location", "https://user:SECRET@host/%zz")
				w.WriteHeader(http.StatusTemporaryRedirect)
				return
			}
			fmt.Fprint(w, `{"id":1}`)
		}))
		DeferCleanup(srv.Close)
		var logs []string
		r := newRunner(srv.URL)
		r.Log = func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
		Expect(r.Start(ctx, core.Run{ID: "r1"}, candidate)).To(Succeed())
		Expect(r.Start(ctx, core.Run{ID: "r2"}, candidate)).To(Succeed()) // releases r1: unpin is redirected
		Expect(logs).NotTo(BeEmpty())
		Expect(strings.Join(logs, "\n")).NotTo(ContainSubstring("SECRET"))
	})

	It("A credential in a runner url that does not parse is hidden in the error", func() {
		clk = &clock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
		for _, u := range []string{"https://user:SECRET@host/%zz", "https://host/%zz", "https://user:SECRET/x@host/"} {
			err := newRunner(u).Start(ctx, core.Run{ID: "r1"}, candidate)
			Expect(err).To(MatchError("runner.url: does not parse as a URL"), "the field only, never the URL or its port: %s", u)
		}
	})

	// test starts a run and polls it until done, at most 10 times.
	test := func(id string) (core.Verdict, error) {
		if err := r.Start(ctx, core.Run{ID: id}, candidate); err != nil {
			return core.None, err
		}
		for range 10 {
			v, done, err := r.Poll(ctx, id)
			if err != nil || done {
				return v, err
			}
		}
		return "", fmt.Errorf("never done")
	}

	BeforeEach(func() {
		f = &fake{final: "succeeded", tested: candidate}
		srv = httptest.NewServer(f)
		DeferCleanup(srv.Close)
		clk = &clock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
		r = newRunner(srv.URL)
	})

	It("A passing build of the candidate is green", func() {
		Expect(test("serial-1")).To(Equal(core.Pass))
		Expect(f.saw("PUT " + base + "/resources/demo-repo/versions/7/pin")).To(BeTrue())
		Expect(f.saw("GET " + base + "/resources/demo-repo/versions/7/input_to")).To(BeTrue())
		Expect(f.saw("PUT " + base + "/resources/demo-repo/unpin")).To(BeTrue())
	})

	It("A failing build is red", func() {
		f.final = "failed"
		Expect(test("serial-1")).To(Equal(core.Fail))
	})

	It("A failing build with test names in its log reports them", func() {
		f.final, f.pages = "failed", [][]string{{"compiling\nFAILED: TestAlpha\n", "FAILED: TestBeta  \nnoise FAILED: no\n"}, {"FAILED: TestAlpha\nFAILED: TestGamma\n"}}
		Expect(test("serial-1")).To(Equal(core.Fail))
		Expect(r.FailedTests("serial-1")).To(Equal([]string{"TestAlpha", "TestBeta", "TestGamma"}))
	})

	It("A failing build without test names is a plain fail", func() {
		f.final, f.pages = "failed", [][]string{{"compiling\nboom\n"}}
		Expect(test("serial-1")).To(Equal(core.Fail))
		Expect(r.FailedTests("serial-1")).To(BeEmpty())
	})

	It("A failing build whose log cannot be read is still a fail", func() {
		f.final, f.pages = "failed", nil // the fake has no page to serve: a 500
		Expect(test("serial-1")).To(Equal(core.Fail))
		Expect(r.FailedTests("serial-1")).To(BeEmpty())
	})

	It("A passing build reports no test names", func() {
		f.pages = [][]string{{"FAILED: TestAlpha\n"}}
		Expect(test("serial-1")).To(Equal(core.Pass))
		Expect(r.FailedTests("serial-1")).To(BeEmpty())
		Expect(f.saw("GET /api/v1/builds/100/events")).To(BeFalse())
	})

	It("A configured pattern picks the test names out of the log", func() {
		f.final, f.pages = "failed", [][]string{{"--- FAIL: TestAlpha (0.1s)\nFAILED: TestBeta\n"}}
		r.Config.FailedPattern = `^--- FAIL: (\S+)`
		Expect(test("serial-1")).To(Equal(core.Fail))
		Expect(r.FailedTests("serial-1")).To(Equal([]string{"TestAlpha"}))
	})

	It("An errored or aborted build gives no verdict", func() {
		for _, status := range []string{"errored", "aborted", "something-new"} {
			f.final = status
			Expect(test("serial-1")).To(Equal(core.None), status)
		}
	})

	It("A build that tested a different commit gives no verdict", func() {
		f.tested = older
		Expect(test("serial-1")).To(Equal(core.None))
		Expect(f.saw("PUT " + base + "/resources/demo-repo/unpin")).To(BeTrue())
	})

	It("A build exceeding the wait cap gives no verdict and is aborted", func() {
		f.running = 1000
		Expect(r.Start(ctx, core.Run{ID: "serial-1"}, candidate)).To(Succeed())
		v, done, err := r.Poll(ctx, "serial-1") // check done: pins and triggers
		Expect(err).NotTo(HaveOccurred())
		Expect(done).To(BeFalse())
		Expect(v).To(Equal(core.None))
		clk.t = clk.t.Add(time.Hour + time.Second)
		v, done, err = r.Poll(ctx, "serial-1")
		Expect(err).NotTo(HaveOccurred())
		Expect(done).To(BeTrue())
		Expect(v).To(Equal(core.None))
		Expect(f.saw("PUT /api/v1/builds/100/abort")).To(BeTrue())
		Expect(f.saw("PUT " + base + "/resources/demo-repo/unpin")).To(BeTrue())
	})

	It("An unreachable server gives no verdict, never red", func() {
		Expect(r.Start(ctx, core.Run{ID: "serial-1"}, candidate)).To(Succeed())
		srv.Close() // gone mid-run: Poll errs, never red
		v, _, err := r.Poll(ctx, "serial-1")
		Expect(err).To(HaveOccurred())
		Expect(v).To(Equal(core.None))
		Expect(r.Start(ctx, core.Run{ID: "serial-2"}, candidate)).NotTo(Succeed())
	})

	It("forgets the earlier build when a run ID is started again", func() {
		f.running = 1000
		Expect(r.Start(ctx, core.Run{ID: "serial-1"}, older)).To(Succeed())
		_, _, _ = r.Poll(ctx, "serial-1") // build 100 for the older commit
		f.running, f.final = 0, "succeeded"
		Expect(r.Start(ctx, core.Run{ID: "serial-1"}, candidate)).To(Succeed())
		v, done, err := r.Poll(ctx, "serial-1")
		Expect(err).NotTo(HaveOccurred())
		Expect([]any{v, done}).To(Equal([]any{core.None, false})) // a fresh check, not build 100's result
		Expect(test("serial-1")).To(Equal(core.Pass))
	})

	It("gives no verdict when the candidate never shows up as a version", func() {
		Expect(r.Start(ctx, core.Run{ID: "serial-1"}, "feedface00000000000000000000000000000000")).To(Succeed())
		v, done, err := r.Poll(ctx, "serial-1")
		Expect([]any{v, done, err}).To(Equal([]any{core.None, true, nil}))
		Expect(f.saw("POST " + base + "/jobs/demo-job/builds")).To(BeFalse())
	})

	It("A build that tested the candidate under a different input name still binds by version; a different version never gives a verdict", func() {
		for _, final := range []string{"failed", "succeeded"} {
			// src comes from demo-repo at version 8; an input aliased demo-repo,
			// from another resource, happens to hold the candidate's ref.
			f.final, f.tested = final, older
			f.inputs = []map[string]any{{"name": "demo-repo", "version": map[string]string{"ref": candidate}}, {"name": "src", "version": map[string]string{"ref": older}}}
			Expect(test("serial-1")).To(Equal(core.None), final)
		}
		f.final, f.tested = "succeeded", candidate
		f.inputs = []map[string]any{{"name": "src", "version": map[string]string{"ref": candidate}}}
		Expect(test("serial-1")).To(Equal(core.Pass)) // the job calls the resource src: still version 7
	})

	It("finds the candidate beyond the first 100 versions", func() {
		f.extra = 100
		Expect(test("serial-1")).To(Equal(core.Pass))
		Expect(f.saw("PUT " + base + "/resources/demo-repo/versions/7/pin")).To(BeTrue())
	})

	It("A new Start supersedes the in-flight run: its build is aborted and unpinned, and its verdict can never be read", func() {
		f.running = 1000
		Expect(r.Start(ctx, core.Run{ID: "serial-1"}, candidate)).To(Succeed())
		_, _, _ = r.Poll(ctx, "serial-1") // pinned and triggered: build 100
		Expect(r.Start(ctx, core.Run{ID: "serial-2"}, older)).To(Succeed())
		Expect(f.saw("PUT /api/v1/builds/100/abort")).To(BeTrue())
		Expect(f.saw("PUT " + base + "/resources/demo-repo/unpin")).To(BeTrue())
		f.running, f.final = 0, "succeeded"
		v, done, _ := r.Poll(ctx, "serial-1")
		Expect([]any{v, done}).To(Equal([]any{core.None, true}))
		_, _, _ = r.Poll(ctx, "serial-2")
		Expect(f.saw("PUT " + base + "/resources/demo-repo/versions/8/pin")).To(BeTrue())
	})

	It("A 503 creating the job after the pin does not wedge the next Start", func() {
		f.buildDown = true
		Expect(r.Start(ctx, core.Run{ID: "serial-1"}, candidate)).To(Succeed())
		_, _, err := r.Poll(ctx, "serial-1") // pinned, then job creation 503
		Expect(err).To(HaveOccurred())
		f.buildDown = false
		Expect(r.Start(ctx, core.Run{ID: "serial-2"}, older)).To(Succeed())
		_, _, _ = r.Poll(ctx, "serial-2")
		Expect(f.saw("PUT " + base + "/resources/demo-repo/versions/8/pin")).To(BeTrue())
	})

	It("A 503 reading input_to after the build finished does not wedge the next Start", func() {
		f.bindingDown = true
		Expect(r.Start(ctx, core.Run{ID: "serial-1"}, candidate)).To(Succeed())
		_, _, _ = r.Poll(ctx, "serial-1") // pinned and triggered
		_, _, err := r.Poll(ctx, "serial-1")
		Expect(err).To(HaveOccurred())
		f.bindingDown = false
		Expect(r.Start(ctx, core.Run{ID: "serial-2"}, older)).To(Succeed())
		_, _, _ = r.Poll(ctx, "serial-2")
		Expect(f.saw("PUT " + base + "/resources/demo-repo/versions/8/pin")).To(BeTrue())
	})

	It("an error checking which version a build tested is an error, not done", func() {
		f.final, f.bindingDown = "failed", true
		Expect(r.Start(ctx, core.Run{ID: "serial-1"}, candidate)).To(Succeed())
		_, _, _ = r.Poll(ctx, "serial-1") // pinned and triggered
		v, done, err := r.Poll(ctx, "serial-1")
		Expect(err).To(HaveOccurred())
		Expect([]any{v, done}).To(Equal([]any{core.None, false}))
		f.bindingDown = false
		v, done, err = r.Poll(ctx, "serial-1")
		Expect([]any{v, done, err}).To(Equal([]any{core.Fail, true, nil}))
	})

	It("a runner in a new process takes back a saved run and never triggers its build again", func() {
		Expect(r.Start(ctx, core.Run{ID: "serial-1"}, candidate)).To(Succeed())
		_, _, _ = r.Poll(ctx, "serial-1") // build 100 is triggered
		r, saved := newRunner(srv.URL), r.Build("serial-1")
		r.Resume("serial-1", saved)
		Expect(test("serial-1")).To(Equal(core.Pass), "started again: the same build, kept")
		Expect(f.builds).To(Equal(1))
	})

	It("gives no verdict for a run it never started", func() {
		v, _, err := r.Poll(ctx, "serial-9")
		Expect(err).To(HaveOccurred())
		Expect(v).To(Equal(core.None))
	})

	It("reads the token from a file on every call", func() {
		dir := GinkgoT().TempDir()
		Expect(writeFile(dir+"/cred", "stale-token\n")).To(Succeed())
		r.Config.Credential = "file:" + dir + "/cred"
		Expect(r.Start(ctx, core.Run{ID: "serial-1"}, candidate)).NotTo(Succeed())
		Expect(writeFile(dir+"/cred", "fake-token\n")).To(Succeed())
		Expect(test("serial-1")).To(Equal(core.Pass))
	})

	Describe("Parse", func() {
		parse := func(s string) (jetbridge.Config, error) {
			var doc yaml.Node
			Expect(yaml.Unmarshal([]byte(s), &doc)).To(Succeed())
			return jetbridge.Parse(doc.Content[0])
		}
		const ok = "{kind: jetbridge, url: https://ci.example.test, pipeline: p, job: j, resource: r, credential: env:FAKE_JB_TOKEN"

		It("fills the defaults", func() {
			c, err := parse(ok + "}")
			Expect(err).NotTo(HaveOccurred())
			Expect(c.Team).To(Equal("main"))
			Expect(c.WaitCap).To(Equal(time.Hour))
			Expect(c.FailedPattern).To(Equal(jetbridge.DefaultFailedPattern))
			c, err = parse(ok + ", team: t, wait_cap: 20m}")
			Expect(err).NotTo(HaveOccurred())
			Expect([]any{c.Team, c.WaitCap}).To(Equal([]any{"t", 20 * time.Minute}))
		})

		It("refuses a failed_pattern that is not a regular expression with one group", func() {
			for _, bad := range []string{`"("`, `"FAILED"`, `"(a)(b)"`} {
				_, err := parse(ok + ", failed_pattern: " + bad + "}")
				Expect(err).To(MatchError(ContainSubstring("runner.failed_pattern")))
			}
		})

		It("refuses an unknown key naming the nearest", func() {
			_, err := parse(ok + ", wait_capp: 1m}")
			Expect(err).To(MatchError(`unknown key "runner.wait_capp"; did you mean "runner.wait_cap"?`))
		})

		It("refuses a missing setting, another kind, or a literal secret", func() {
			_, err := parse("{kind: jetbridge, credential: env:X}")
			Expect(err).To(MatchError(ContainSubstring("runner.url, runner.pipeline, runner.job and runner.resource are required")))
			_, err = parse(strings.Replace(ok, "kind: jetbridge", "kind: other", 1) + "}")
			Expect(err).To(MatchError(ContainSubstring(`runner.kind: unsupported value; use one of: jetbridge`)))
			_, err = parse(strings.Replace(ok, "env:FAKE_JB_TOKEN", "hunter2", 1) + "}")
			Expect(err).To(MatchError(ContainSubstring("runner.credential must name an env var (env:NAME) or a file (file:PATH)")))
		})

		It("refuses a url holding a credential, naming the setting and never the value", func() {
			for _, u := range []string{"https://user:F4ke/Pa55@host/%zz", "https://user:F4kePa55@host", "https://F4keTok3n@host"} {
				_, err := config.Parse([]byte("apiVersion: jetbridge.dev/queue/v2\nrepository: {uri: /r.git}\nrunner: " + strings.Replace(ok, "https://ci.example.test", "'"+u+"'", 1) + "}\n"))
				Expect(err).To(MatchError(ContainSubstring("runner.url: a URL must not hold credentials")), "url %q", u)
				Expect(err.Error()).NotTo(ContainSubstring("F4ke"), "url %q", u)
			}
			// checked again as decoded here, whoever loaded the section
			_, err := parse(strings.Replace(ok, "https://ci.example.test", "!!binary aHR0cHM6Ly91c2VyOkY0a2UvUGE1NUBob3N0LyV6eg==", 1) + "}")
			Expect(err).To(MatchError(ContainSubstring("runner.url: a URL must not hold credentials")))
			Expect(err.Error()).NotTo(ContainSubstring("F4ke"))
		})
	})
})

func writeFile(path, s string) error { return os.WriteFile(path, []byte(s), 0o600) }
