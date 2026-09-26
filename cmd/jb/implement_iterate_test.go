package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/implement"
	"github.com/concourse/concourse/agent/review"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/fly/rc"
)

const (
	reviewRunID  = 52
	reviewNumber = 5
)

// Iterating is a new capture that carries a verified prior change and the
// verified findings of a review of it: implement Run → apply → review Run →
// capture --prior-run --findings-run. Nothing is reopened, and neither input
// is sealed unless it was verified against its Run.
func TestImplementCaptureCarriesAPriorChangeAndReviewFindings(t *testing.T) {
	platform := &cliPlatform{credential: "available"}
	platform.run = atc.PipelineRun{ID: cliRunID, Number: cliNumber, ContractVersion: atc.RunContractV2, ActivationEpoch: 1, Status: atc.RunStatusRunning}
	platform.server = httptest.NewServer(http.HandlerFunc(platform.serve))
	defer platform.server.Close()
	t.Setenv("FLY_HOME", t.TempDir())
	if err := rc.SaveTarget("jb-test", platform.server.URL, false, "main", &rc.TargetToken{Type: "Bearer", Value: cliToken}, "", "", ""); err != nil {
		t.Fatal(err)
	}
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

	// Implement Run 3 (ID 41) completes; its change is applied locally.
	first := filepath.Join(root, "first")
	if _, err := jb(t, "implement", "capture", "--repo", repo, "--brief", brief, "--output", first); err != nil {
		t.Fatal(err)
	}
	loaded, err := implement.LoadSnapshot(first)
	if err != nil {
		t.Fatal(err)
	}
	platform.complete(t, loaded, base)
	implementRun := []string{"--target", "jb-test", "--run", strconv.Itoa(cliNumber)}
	if _, err := jb(t, append([]string{"implement", "apply", "--repo", repo}, implementRun...)...); err != nil {
		t.Fatal(err)
	}
	retrieved := filepath.Join(root, "retrieved")
	if _, err := jb(t, append([]string{"implement", "result", "--output", retrieved}, implementRun...)...); err != nil {
		t.Fatal(err)
	}

	// Review Run 5 (ID 52) reviews base..impl/run-41 and publishes a finding.
	bundle, err := review.Capture(t.Context(), review.CaptureOptions{Repo: repo, Base: baseCommit, Head: "impl/run-41", Output: filepath.Join(root, "bundle")})
	if err != nil {
		t.Fatal(err)
	}
	reviewID := reviewRunID
	report, err := review.BuildReport(bundle, []byte(`{"summary":"Reviewed the change.","complete":true,"reviewed_files":["parser.go"],"limitations":[],"findings":[`+
		`{"id":"x","severity":"medium","dimension":"testing","title":"First byte is untested","explanation":"Nothing covers First.","recommendation":"Add a test for First.",`+
		`"location":{"side":"head","path":"parser.go","start_line":2,"end_line":2}}]}`),
		review.Metadata{RunID: &reviewID, ModelRequested: "review-model", CodexVersion: "codex-cli test", Profile: review.DefaultProfile()})
	if err != nil {
		t.Fatal(err)
	}
	reportJSON, _ := json.Marshal(report)
	archive, binding := resultArchive(t, map[string][]byte{"review.json": reportJSON})
	platform.mu.Lock()
	platform.review = atc.PipelineRun{ID: reviewRunID, Number: reviewNumber, ContractVersion: atc.RunContractV2, ActivationEpoch: 1, Status: atc.RunStatusSucceeded,
		Terminal: &atc.RunTerminalResult{Status: atc.RunStatusSucceeded, Results: map[string]atc.RunResultBinding{"findings": binding}}}
	platform.reviewArchive = archive
	platform.mu.Unlock()

	capture := func(name string, extra ...string) (string, error) {
		return jb(t, append([]string{"implement", "capture", "--repo", repo, "--brief", brief, "--output", filepath.Join(root, name)}, extra...)...)
	}
	remote := []string{"--target", "jb-test", "--base", baseCommit}
	out, err := capture("next", append(remote, "--prior-run", strconv.Itoa(cliNumber), "--findings-run", strconv.Itoa(reviewNumber))...)
	if err != nil {
		t.Fatal(err)
	}
	var captured struct {
		Digest      string `json:"input_digest"`
		PriorRun    int    `json:"prior_run"`
		FindingsRun int    `json:"findings_run"`
	}
	if err := json.Unmarshal([]byte(out), &captured); err != nil || captured.PriorRun != cliRunID || captured.FindingsRun != reviewRunID {
		t.Fatalf("capture did not report the Runs it carried: %s %v", out, err)
	}
	next, err := implement.LoadSnapshot(filepath.Join(root, "next"))
	if err != nil || next.Digest != captured.Digest || *next.Manifest.PriorRun != cliRunID || *next.Manifest.FindingsRun != reviewRunID {
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

	for name, c := range map[string]struct {
		args []string
		want string
	}{
		// impl/run-41 is checked out: HEAD is not the prior change's base.
		"prior for another base":     {[]string{"--target", "jb-test", "--prior-run", strconv.Itoa(cliNumber)}, "not the requested base"},
		"prior from disk, same":      {[]string{"--prior-dir", retrieved}, "not the requested base"},
		"both priors":                {append(remote, "--prior-run", strconv.Itoa(cliNumber), "--prior-dir", retrieved), "at most one"},
		"findings without a target":  {[]string{"--base", baseCommit, "--findings-run", strconv.Itoa(reviewNumber)}, "require --target"},
		"prior without a target":     {[]string{"--base", baseCommit, "--prior-run", strconv.Itoa(cliNumber)}, "require --target"},
		"a review Run that is not":   {append(remote, "--findings-run", strconv.Itoa(reviewNumber+1)), "review findings from Run 6"},
		"an implement Run as review": {append(remote, "--findings-run", strconv.Itoa(cliNumber), "--review-template", "implement"), "review findings from Run 3"},
	} {
		if _, err := capture("refused-"+strings.ReplaceAll(name, " ", "-"), c.args...); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want an error naming %q, got %v", name, c.want, err)
		}
	}

	// A review result naming a different Run is refused before it is sealed.
	platform.mu.Lock()
	platform.review.ID = reviewRunID + 1
	platform.mu.Unlock()
	if _, err := capture("forged", append(remote, "--findings-run", strconv.Itoa(reviewNumber))...); err == nil || !strings.Contains(err.Error(), "different Run") {
		t.Fatalf("sealed findings from a result of another Run: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "forged")); !os.IsNotExist(err) {
		t.Fatal("a refused capture published a snapshot")
	}
}
