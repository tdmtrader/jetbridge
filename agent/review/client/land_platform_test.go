package client

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/review"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/atctest"
	"sigs.k8s.io/yaml"
)

func TestMain(m *testing.M) { os.Exit(atctest.Run(m)) }

const reviewTemplate = "review"

// landPlatform is the real platform, seen by one test through its own team,
// with the review template installed as an operator installs it. Every Run's
// findings producer starts as it is admitted, so a submission reaches
// readiness; the test then decides how the Run ends.
type landPlatform struct {
	*atctest.Platform
	team string
}

func newLandPlatform(t *testing.T) *landPlatform {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "review-template.yml"))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.NewReplacer("((review_worker_image))", atctest.Pin, "((review_model))", "some-model").Replace(string(data))
	if strings.Contains(strings.ReplaceAll(text, "((run_id))", ""), "((") {
		t.Fatal("the template has an operator variable this test does not know")
	}
	var config atc.Config
	if err := yaml.Unmarshal([]byte(text), &config); err != nil {
		t.Fatal(err)
	}
	p := &landPlatform{Platform: atctest.Get(t)}
	p.team = p.NewTeam(t)
	p.Install(t, p.team, reviewTemplate, config)
	p.Schedule(t, p.team, reviewTemplate, FindingsResult)
	interval := landPollInterval
	landPollInterval = 20 * time.Millisecond
	t.Cleanup(func() { landPollInterval = interval })
	return p
}

// runningLanding is a Land call whose Run is ready and still running.
type runningLanding struct {
	done    chan struct{}
	outcome LandOutcome
	err     error
	handle  Handle
	runID   int
	client  *Client
	options LandOptions
	// bundle is the change the landing captured and submitted.
	bundle *review.Bundle
}

// land starts landing fix's head and returns once its Run is ready: the
// platform has delivered credentials to the findings producer and the
// landing is polling the Run.
func (p *landPlatform) land(t *testing.T, fix landFixture) *runningLanding {
	t.Helper()
	root := t.TempDir()
	auth := filepath.Join(root, "auth.json")
	if err := os.WriteFile(auth, []byte(`{"synthetic":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	c, err := New(p.URL, p.Client(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	l := &runningLanding{done: make(chan struct{}), client: c, options: LandOptions{
		Repo: fix.work, Team: p.team, Template: reviewTemplate, AuthFile: auth, StateDir: state,
		Policy: review.Policy{BlockAt: "medium"},
	}}
	go func() {
		defer close(l.done)
		l.outcome, l.err = Land(ctx, c, l.options)
	}()
	for {
		select {
		case <-l.done:
			t.Fatalf("landing ended before its Run was ready: %+v, %v", l.outcome, l.err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
		receipts, _ := filepath.Glob(filepath.Join(state, "*", "receipt.json"))
		if len(receipts) != 1 {
			continue
		}
		var receipt struct {
			RunID  int `json:"run_id"`
			Number int `json:"number"`
		}
		if data, err := os.ReadFile(receipts[0]); err != nil || json.Unmarshal(data, &receipt) != nil || receipt.RunID < 1 {
			continue
		}
		l.handle, l.runID = Handle{Team: p.team, Template: reviewTemplate, Number: receipt.Number}, receipt.RunID
		session, err := c.CredentialSession(ctx, l.handle, FindingsResult)
		if err != nil || session.Status != "ready" {
			continue
		}
		if l.bundle, err = review.LoadBundle(filepath.Join(filepath.Dir(receipts[0]), "bundle")); err != nil {
			t.Fatal(err)
		}
		return l
	}
}

func (l *runningLanding) wait(t *testing.T) (LandOutcome, error) {
	t.Helper()
	select {
	case <-l.done:
		return l.outcome, l.err
	case <-time.After(time.Minute):
		t.Fatal("landing did not finish after its Run did")
		return LandOutcome{}, nil
	}
}

// cleanAssessment reviews the fixture's one changed file and finds nothing.
const cleanAssessment = `{"summary":"No defects found.","complete":true,"reviewed_files":["parser.go"],"limitations":[],"findings":[]}`

// highAssessment has one located "high" finding.
const highAssessment = `{"summary":"Index is outside the intended byte.","complete":true,"reviewed_files":["parser.go"],"limitations":[],"findings":[{"id":"model-id","severity":"high","dimension":"correctness","title":"Check the length","explanation":"One-byte input panics at index 1.","recommendation":"Validate length before indexing.","location":{"side":"head","path":"parser.go","start_line":2,"end_line":2}}]}`

// publishedReview is a clean review of b as the worker publishes it for Run
// runID.
func publishedReview(t *testing.T, b *review.Bundle, runID int) map[string]map[string][]byte {
	return publishedAssessment(t, b, runID, cleanAssessment)
}

func publishedAssessment(t *testing.T, b *review.Bundle, runID int, assessment string) map[string]map[string][]byte {
	t.Helper()
	r, err := review.BuildReport(b, []byte(assessment),
		review.Metadata{RunID: &runID, ModelRequested: "some-model", CodexVersion: "codex-cli test", Profile: review.DefaultProfile()})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return map[string]map[string][]byte{FindingsResult: {"review.json": append(data, '\n'), "review.md": []byte(r.Markdown())}}
}

// requireNotLandedLater pushes head to core by hand after a landing that did
// not land, then lands again from the same state: the saved bundle proves
// only that a landing started, so the rerun must not report head landed.
func (l *runningLanding) requireNotLandedLater(t *testing.T, fix landFixture) {
	t.Helper()
	landGit(t, fix.work, "push", "-q", "origin", fix.head+":refs/heads/core")
	outcome, err := Land(context.Background(), l.client, l.options)
	if err == nil || outcome.Outcome != "refused" || !strings.Contains(outcome.Reason, "nothing to land") {
		t.Fatalf("a landing that never passed was reported landed: %+v, %v", outcome, err)
	}
}

func requireCore(t *testing.T, fix landFixture, want string) {
	t.Helper()
	if got := landGit(t, fix.origin, "rev-parse", "refs/heads/core"); got != want {
		t.Fatalf("origin core is %s, want %s", got, want)
	}
}

func TestLandPushesExactlyTheReviewedHead(t *testing.T) {
	p := newLandPlatform(t)
	fix := newLandFixture(t)
	l := p.land(t, fix)
	p.Succeed(t, p.team, reviewTemplate, l.handle.Number, publishedReview(t, l.bundle, l.runID))
	outcome, err := l.wait(t)
	want := LandOutcome{Outcome: "landed", RunNumber: l.handle.Number, BaseCommit: fix.base, HeadCommit: fix.head, Verdict: "no_findings", Blocking: []string{}}
	if err != nil || outcome.Outcome != want.Outcome || outcome.RunNumber != want.RunNumber || outcome.BaseCommit != want.BaseCommit ||
		outcome.HeadCommit != want.HeadCommit || outcome.Verdict != want.Verdict || len(outcome.Blocking) != 0 {
		t.Fatalf("landing decided %+v, %v; want %+v", outcome, err, want)
	}
	requireCore(t, fix, fix.head)

	// Landing the pair again finds core on head and the pass marker the
	// landing wrote before its push: it was landed, by this Run.
	again, err := Land(context.Background(), l.client, l.options)
	if err != nil || again.Outcome != "landed" || again.RunNumber != l.handle.Number || again.BaseCommit != fix.base || again.HeadCommit != fix.head {
		t.Fatalf("rerun after landing decided %+v, %v", again, err)
	}
}

func TestLandRefusesAtTheFloor(t *testing.T) {
	p := newLandPlatform(t)
	fix := newLandFixture(t)
	l := p.land(t, fix)
	p.Succeed(t, p.team, reviewTemplate, l.handle.Number, publishedAssessment(t, l.bundle, l.runID, highAssessment))
	outcome, err := l.wait(t)
	if err == nil || outcome.Outcome != "refused" || outcome.Reason != "findings at or above medium" || len(outcome.Blocking) != 1 || outcome.Blocking[0] != "f-001" {
		t.Fatalf("want a refusal at the floor, got %+v, %v", outcome, err)
	}
	requireCore(t, fix, fix.base)
	l.requireNotLandedLater(t, fix)
}

func TestLandRefusesAFailedReviewRun(t *testing.T) {
	p := newLandPlatform(t)
	fix := newLandFixture(t)
	l := p.land(t, fix)
	// The producer publishes a clean report and exits, then the build fails:
	// the Run's failure refuses the landing whatever its report says.
	for result, files := range publishedReview(t, l.bundle, l.runID) {
		p.Start(t, p.team, reviewTemplate, l.handle.Number, result).Publish(t, files)
	}
	p.Fail(t, p.team, reviewTemplate, l.handle.Number)
	outcome, err := l.wait(t)
	if err == nil || outcome.Outcome != "refused" || outcome.Reason != "review Run failed" || outcome.RunNumber != l.handle.Number {
		t.Fatalf("want a refusal of the failed Run, got %+v, %v", outcome, err)
	}
	requireCore(t, fix, fix.base)
	l.requireNotLandedLater(t, fix)
}

// The report is published for the landing's own Run, so the Run client
// accepts it; only the landing's provenance check against the bundle it
// captured can tell that it reviewed another change.
func TestLandRefusesAReportForAnotherChange(t *testing.T) {
	p := newLandPlatform(t)
	fix := newLandFixture(t)
	// The same tree as head under another commit: a report for it names
	// another head and another input digest.
	other := landGit(t, fix.work, "commit-tree", fix.head+"^{tree}", "-p", fix.head, "-m", "another change")
	otherBundle, err := review.Capture(context.Background(), review.CaptureOptions{Repo: fix.work, Base: fix.base, Head: other, Output: filepath.Join(t.TempDir(), "other")})
	if err != nil {
		t.Fatal(err)
	}
	l := p.land(t, fix)
	if otherBundle.Digest == l.bundle.Digest {
		t.Fatal("the other change has the landing's input digest")
	}
	p.Succeed(t, p.team, reviewTemplate, l.handle.Number, publishedReview(t, otherBundle, l.runID))
	outcome, err := l.wait(t)
	if err == nil || outcome.Outcome != "refused" || !strings.Contains(outcome.Reason, "does not match the captured change") {
		t.Fatalf("want a refusal of the foreign report, got %+v, %v", outcome, err)
	}
	requireCore(t, fix, fix.base)
	l.requireNotLandedLater(t, fix)
}
