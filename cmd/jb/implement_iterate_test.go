package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/implement"
	"github.com/concourse/concourse/agent/review"
)

// submit runs a jb submit command for workload and returns the Run it
// reported ready.
func submit(t *testing.T, workload string, args ...string) (number, runID int) {
	t.Helper()
	out, err := jb(t, append([]string{workload, "submit", "--target", "jb-test"}, args...)...)
	var submitted struct {
		RunID  int  `json:"run_id"`
		Ready  bool `json:"ready"`
		Handle struct {
			Number int `json:"number"`
		} `json:"handle"`
	}
	if err != nil || json.Unmarshal([]byte(out), &submitted) != nil || !submitted.Ready {
		t.Fatalf("%s submit: %s %v", workload, out, err)
	}
	return submitted.Handle.Number, submitted.RunID
}

// reviewed completes review Run number with one finding about parser.go,
// in a report that names the Run named.
func (p *cliPlatform) reviewed(t *testing.T, bundle *review.Bundle, number, named int) {
	t.Helper()
	report, err := review.BuildReport(bundle, []byte(`{"summary":"Reviewed the change.","complete":true,"reviewed_files":["parser.go"],"limitations":[],"findings":[`+
		`{"id":"x","severity":"medium","dimension":"testing","title":"First byte is untested","explanation":"Nothing covers First.","recommendation":"Add a test for First.",`+
		`"location":{"side":"head","path":"parser.go","start_line":2,"end_line":2}}]}`),
		review.Metadata{RunID: &named, ModelRequested: "review-model", CodexVersion: "codex-cli test", Profile: review.DefaultProfile()})
	if err != nil {
		t.Fatal(err)
	}
	reportJSON, _ := json.Marshal(report)
	p.Succeed(t, p.team, "review", number, map[string]map[string][]byte{"findings": {"review.json": reportJSON}})
}

// Iterating is a new capture that carries a verified prior change and the
// verified findings of a review of it: implement Run → apply → review Run →
// capture --prior-run --findings-run. Nothing is reopened, and neither input
// is sealed unless it was verified against its Run.
func TestImplementCaptureCarriesAPriorChangeAndReviewFindings(t *testing.T) {
	p := newCLIPlatform(t)
	p.Schedule(t, p.team, "implement", "change")
	p.Schedule(t, p.team, "review", "findings")
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	cliGit(t, repo, "init", "-q")
	cliGit(t, repo, "config", "user.name", "CLI fixture")
	cliGit(t, repo, "config", "user.email", "cli@example.test")
	base := "package parser\nfunc First(s string) byte { return s[1] }\n"
	brief := filepath.Join(root, "brief.md")
	for name, text := range map[string]string{filepath.Join(repo, "parser.go"): base, brief: "Return the first byte.\n"} {
		if err := os.WriteFile(name, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cliGit(t, repo, "add", ".")
	cliGit(t, repo, "commit", "-qm", "base")
	baseCommit := cliGit(t, repo, "rev-parse", "HEAD")

	auth := filepath.Join(root, "auth.json")
	if err := os.WriteFile(auth, []byte(`{"synthetic":"owner-auth"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// An implement Run completes; its change is applied locally.
	first := filepath.Join(root, "first")
	if _, err := jb(t, "implement", "capture", "--repo", repo, "--brief", brief, "--output", first); err != nil {
		t.Fatal(err)
	}
	loaded, err := implement.LoadSnapshot(first)
	if err != nil {
		t.Fatal(err)
	}
	implementNumber, _ := submit(t, "implement", "--input", first, "--receipt", filepath.Join(root, "implement.json"), "--auth-file", auth)
	implementID := p.complete(t, implementNumber, loaded, base)
	branch := "impl/run-" + strconv.Itoa(implementID)
	implementRun := []string{"--target", "jb-test", "--run", strconv.Itoa(implementNumber)}
	if _, err := jb(t, append([]string{"implement", "apply", "--repo", repo}, implementRun...)...); err != nil {
		t.Fatal(err)
	}
	// Apply never checks the branch out; the developer does.
	cliGit(t, repo, "switch", "-q", branch)
	retrieved := filepath.Join(root, "retrieved")
	if _, err := jb(t, append([]string{"implement", "result", "--output", retrieved}, implementRun...)...); err != nil {
		t.Fatal(err)
	}

	// A review Run reviews base..impl/run-<id> and publishes a finding.
	bundle, err := review.Capture(t.Context(), review.CaptureOptions{Repo: repo, Base: baseCommit, Head: branch, Output: filepath.Join(root, "bundle")})
	if err != nil {
		t.Fatal(err)
	}
	reviewNumber, reviewID := submit(t, "review", "--input", bundle.Dir, "--receipt", filepath.Join(root, "review.json"), "--auth-file", auth)
	p.reviewed(t, bundle, reviewNumber, reviewID)

	capture := func(name string, extra ...string) (string, error) {
		return jb(t, append([]string{"implement", "capture", "--repo", repo, "--brief", brief, "--output", filepath.Join(root, name)}, extra...)...)
	}
	remote := []string{"--target", "jb-test", "--base", baseCommit}
	out, err := capture("next", append(remote, "--prior-run", strconv.Itoa(implementNumber), "--findings-run", strconv.Itoa(reviewNumber))...)
	if err != nil {
		t.Fatal(err)
	}
	var captured struct {
		Digest      string `json:"input_digest"`
		PriorRun    int    `json:"prior_run"`
		FindingsRun int    `json:"findings_run"`
	}
	if err := json.Unmarshal([]byte(out), &captured); err != nil || captured.PriorRun != implementID || captured.FindingsRun != reviewID {
		t.Fatalf("capture did not report the Runs it carried: %s %v", out, err)
	}
	next, err := implement.LoadSnapshot(filepath.Join(root, "next"))
	if err != nil || next.Digest != captured.Digest || *next.Manifest.PriorRun != implementID || *next.Manifest.FindingsRun != reviewID {
		t.Fatalf("snapshot does not record the Run IDs: %+v %v", next, err)
	}
	prior, err := next.Prior()
	if err != nil || !strings.Contains(string(prior), "+func First(s string) byte { return s[0] }") {
		t.Fatalf("prior change: %q %v", prior, err)
	}
	findings, err := next.Findings()
	if err != nil {
		t.Fatal(err)
	}
	var sealed []review.Finding
	if err := json.Unmarshal(findings, &sealed); err != nil || len(sealed) != 1 || sealed[0].ID != "f-001" || sealed[0].Title != "First byte is untested" {
		t.Fatalf("findings.json is not the review's published findings: %s %v", findings, err)
	}

	// A change read from disk is carried without a Run nothing confirmed.
	out, err = capture("from-disk", "--base", baseCommit, "--prior-dir", retrieved)
	if err != nil || strings.Contains(out, "prior_run") || !strings.Contains(out, "prior_patch_digest") {
		t.Fatalf("capture --prior-dir: %s %v", out, err)
	}

	missing := reviewNumber + 1
	for name, c := range map[string]struct {
		args []string
		want string
	}{
		// The developer is on the applied branch: HEAD is not the prior change's base.
		"prior for another base":     {[]string{"--target", "jb-test", "--prior-run", strconv.Itoa(implementNumber)}, "not the requested base"},
		"prior from disk, same":      {[]string{"--prior-dir", retrieved}, "not the requested base"},
		"both priors":                {append(remote, "--prior-run", strconv.Itoa(implementNumber), "--prior-dir", retrieved), "at most one"},
		"findings without a target":  {[]string{"--base", baseCommit, "--findings-run", strconv.Itoa(reviewNumber)}, "require --target"},
		"prior without a target":     {[]string{"--base", baseCommit, "--prior-run", strconv.Itoa(implementNumber)}, "require --target"},
		"a review Run that is not":   {append(remote, "--findings-run", strconv.Itoa(missing)), "review findings from Run " + strconv.Itoa(missing)},
		"an implement Run as review": {append(remote, "--findings-run", strconv.Itoa(implementNumber), "--review-template", "implement"), "review findings from Run " + strconv.Itoa(implementNumber)},
	} {
		if _, err := capture("refused-"+strings.ReplaceAll(name, " ", "-"), c.args...); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want an error naming %q, got %v", name, c.want, err)
		}
	}

	// A second review Run whose published report names the first is refused
	// before anything is sealed.
	forgedNumber, _ := submit(t, "review", "--input", bundle.Dir, "--receipt", filepath.Join(root, "forged.json"), "--auth-file", auth)
	p.reviewed(t, bundle, forgedNumber, reviewID)
	if _, err := capture("forged", append(remote, "--findings-run", strconv.Itoa(forgedNumber))...); err == nil || !strings.Contains(err.Error(), "different Run") {
		t.Fatalf("sealed findings from a result of another Run: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "forged")); !os.IsNotExist(err) {
		t.Fatal("a refused capture published a snapshot")
	}
}
