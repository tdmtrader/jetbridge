//go:build live
// +build live

package jetbridge_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/atc"
)

// The workload checks of hangar_one_daemon A8: with the output plane on the
// disk store, a review Run and an implement Run complete, and their results
// download and verify.
//
// Each Run is driven the way a developer drives it: through the `jb` CLI
// (cmd/jb), built from this source, against a saved fly target -- capture,
// submit with the credential handoff, result. `jb` reads every result through
// agent/runclient, which canonicalizes the archive and refuses it unless it is
// exactly the digest the Run's terminal binding holds; the check then
// downloads each named result itself and holds it to its binding again
// (liveHangarRequireBoundResult), claim included. The CLI is run rather than
// imported because this package is core, and core does not import the agentic
// layer (architecture_test.go), not even from a test.
//
// The worker image is the deployable review worker with its codex replaced by
// the fixture provider (agent/review/testdata/provider), exactly as
// hack/test-review-kubelet builds it: the cluster has no model, so the
// fixture's "finding" and "edit" modes stand in for one. That image must be
// pinned in web.runCredentialWorkerImages, or the handoff is refused, and is
// named to the tier by CONCOURSE_LIVE_REVIEW_WORKER_IMAGE. The credential
// handed off is the fixture's synthetic auth.json, never a real one.
//
// Both skip while web.hangarOutputCapture and web.runResults expect off in
// testdata/live_deployment.json; see liveHangarRequireRunResults.

const (
	liveHangarReviewTemplate    = "live-hangar-review"
	liveHangarImplementTemplate = "live-hangar-implement"
	// liveHangarFixtureAuth is the synthetic auth.json the fixture provider
	// requires (it exits unless it holds synthetic-access).
	liveHangarFixtureAuth = `{"auth_mode":"chatgpt","tokens":{"access_token":"synthetic-access","refresh_token":"synthetic-refresh","id_token":"synthetic-id"}}`
	// liveHangarValidateCommand passes only on the fixture "edit" mode's
	// change applied to the snapshot's base.
	liveHangarValidateCommand = `test -f parser_test.go && test ! -e deleted.txt && grep -q 's\[0\]' parser.go`
)

// TestLiveHangarReviewRunPublishesAVerifiedReport installs
// deploy/review-template.yml, captures a two-commit change with `jb review
// capture`, submits it with `jb review submit` (which hands off the fixture
// credential and returns once the worker acknowledged it), waits for the Run
// to succeed, and reads its findings with `jb review result`. The report must
// name the Run and the submitted bundle and carry the fixture's one finding;
// the findings result must download and match its binding.
func TestLiveHangarReviewRunPublishesAVerifiedReport(t *testing.T) {
	password := liveHangarRequireRunResults(t)
	image := liveHangarFixtureWorkerImage(t)

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	api := liveHangarLogin(t, ctx, password)
	api.installTemplate(t, liveHangarReviewTemplate, liveHangarWorkloadTemplate(t, "review-template.yml", map[string]string{
		"review_worker_image": image,
		"review_model":        "finding",
	}))
	jb := newLiveHangarJB(t, ctx, api)

	repo := newLiveHangarRepo(t, ctx)
	repo.write("parser.go", "package parser\nfunc First(s string) byte { return s[0] }\n")
	repo.write("deleted.txt", "removed line\n")
	base := repo.commit("base")
	repo.remove("deleted.txt")
	repo.write("parser.go", "package parser\nfunc First(s string) byte { return s[1] }\n")
	head := repo.commit("head")

	var captured struct {
		Input  string `json:"input"`
		Digest string `json:"input_digest"`
	}
	jb.json(&captured, "review", "capture", "--repo", repo.dir, "--base", base, "--head", head, "--output", filepath.Join(jb.work, "bundle"))
	if captured.Input == "" || captured.Digest == "" {
		t.Fatalf("jb review capture reported %+v", captured)
	}

	submission := jb.submit("review", liveHangarReviewTemplate, captured.Input)
	run := api.waitForRun(t, ctx, liveHangarReviewTemplate, submission.Handle.Number)
	liveHangarRequireSucceeded(t, run, submission)

	var report struct {
		RunID      *int   `json:"run_id"`
		Verdict    string `json:"verdict"`
		Provenance struct {
			InputDigest     string `json:"input_digest"`
			BaseCommit      string `json:"base_commit"`
			HeadCommit      string `json:"head_commit"`
			ExecutionPolicy string `json:"execution_policy"`
		} `json:"provenance"`
		Assessment struct {
			Findings []struct {
				Location struct {
					Side      string `json:"side"`
					Path      string `json:"path"`
					StartLine int    `json:"start_line"`
				} `json:"location"`
			} `json:"findings"`
		} `json:"assessment"`
	}
	jb.json(&report, "review", "result", "--target", liveHangarJBTarget, "--template", liveHangarReviewTemplate, "--run", strconv.Itoa(run.Number))
	if report.RunID == nil || *report.RunID != run.ID {
		t.Fatalf("Run %d's report names Run %v, want %d", run.Number, report.RunID, run.ID)
	}
	if p := report.Provenance; p.InputDigest != captured.Digest || p.BaseCommit != base || p.HeadCommit != head {
		t.Fatalf("Run %d's report provenance %+v; submitted bundle %s at %s..%s", run.Number, p, captured.Digest, base, head)
	}
	findings := report.Assessment.Findings
	if len(findings) != 1 || findings[0].Location.Side != "head" || findings[0].Location.Path != "parser.go" || findings[0].Location.StartLine != 2 {
		t.Fatalf("Run %d's report holds findings %+v; the fixture model reports one, at head parser.go:2", run.Number, findings)
	}

	body := api.downloadResultOf(t, ctx, liveHangarReviewTemplate, run, "findings")
	binding := liveHangarRequireBoundResult(t, run, "findings", body)
	t.Logf("Run %d's findings: %s (%d bytes), claim %s", run.Number, binding.Ref.Digest, len(body), binding.ClaimID)
}

// TestLiveHangarImplementRunPublishesAVerifiedChangeAndValidation installs
// deploy/implement-template.yml, captures a snapshot with `jb implement
// capture`, submits it with `jb implement submit`, waits for the Run to
// succeed, and reads both results with `jb implement result`: the author
// task's change and the validate task's validation, which `jb` binds to that
// change. The change must be the fixture's edit of the submitted snapshot and
// the operator's validate command must have passed against it; both results
// must download and match their bindings.
func TestLiveHangarImplementRunPublishesAVerifiedChangeAndValidation(t *testing.T) {
	password := liveHangarRequireRunResults(t)
	image := liveHangarFixtureWorkerImage(t)
	validateImage := os.Getenv("CONCOURSE_LIVE_VALIDATE_IMAGE")
	if validateImage == "" {
		t.Fatal("CONCOURSE_LIVE_VALIDATE_IMAGE is unset; the implement template's validate task needs an image with /bin/sh, git, sha256sum, timeout, awk, sed and tr that runs as root (deploy/implement-template.yml)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	api := liveHangarLogin(t, ctx, password)
	command, err := json.Marshal(liveHangarValidateCommand)
	if err != nil {
		t.Fatal(err)
	}
	api.installTemplate(t, liveHangarImplementTemplate, liveHangarWorkloadTemplate(t, "implement-template.yml", map[string]string{
		"review_worker_image": image,
		"implement_model":     "edit",
		"validate_image":      validateImage,
		// A YAML double-quoted scalar: the command holds a backslash.
		"validate_command": string(command),
	}))
	jb := newLiveHangarJB(t, ctx, api)

	repo := newLiveHangarRepo(t, ctx)
	repo.write("parser.go", "package parser\nfunc First(s string) byte { return s[1] }\n")
	repo.write("deleted.txt", "removed line\n")
	base := repo.commit("base")
	brief := filepath.Join(jb.work, "brief.md")
	if err := os.WriteFile(brief, []byte("Return the first byte, cover it with a test, and remove deleted.txt.\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var captured struct {
		Input      string `json:"input"`
		Digest     string `json:"input_digest"`
		BaseCommit string `json:"base_commit"`
	}
	jb.json(&captured, "implement", "capture", "--repo", repo.dir, "--base", base, "--brief", brief, "--output", filepath.Join(jb.work, "snapshot"))
	if captured.Input == "" || captured.Digest == "" || captured.BaseCommit != base {
		t.Fatalf("jb implement capture reported %+v for base %s", captured, base)
	}

	submission := jb.submit("implement", liveHangarImplementTemplate, captured.Input)
	run := api.waitForRun(t, ctx, liveHangarImplementTemplate, submission.Handle.Number)
	liveHangarRequireSucceeded(t, run, submission)

	destination := []string{"--target", liveHangarJBTarget, "--template", liveHangarImplementTemplate, "--run", strconv.Itoa(run.Number)}
	var change struct {
		Summary struct {
			RunID      *int `json:"run_id"`
			Provenance struct {
				InputDigest     string `json:"input_digest"`
				BaseCommit      string `json:"base_commit"`
				ExecutionPolicy string `json:"execution_policy"`
			} `json:"provenance"`
			ChangedFiles []struct {
				Path   string `json:"path"`
				Status string `json:"status"`
			} `json:"changed_files"`
			PatchDigest string `json:"patch_digest"`
		} `json:"summary"`
		Patch string `json:"patch"`
	}
	jb.json(&change, append([]string{"implement", "result"}, destination...)...)
	summary := change.Summary
	if summary.RunID == nil || *summary.RunID != run.ID {
		t.Fatalf("Run %d's change names Run %v, want %d", run.Number, summary.RunID, run.ID)
	}
	if p := summary.Provenance; p.InputDigest != captured.Digest || p.BaseCommit != base || p.ExecutionPolicy != "edit-only" {
		t.Fatalf("Run %d's change provenance %+v; submitted snapshot %s at %s", run.Number, p, captured.Digest, base)
	}
	var changed []string
	for _, file := range summary.ChangedFiles {
		changed = append(changed, file.Status+" "+file.Path)
	}
	if got := strings.Join(changed, ", "); got != "deleted deleted.txt, modified parser.go, added parser_test.go" {
		t.Fatalf("Run %d's change touches %s; the fixture model's edit deletes deleted.txt, modifies parser.go and adds parser_test.go", run.Number, got)
	}
	if !strings.Contains(change.Patch, "return s[0]") {
		t.Fatalf("Run %d's patch does not make the fixture's edit:\n%s", run.Number, change.Patch)
	}

	var validation struct {
		RunID       int    `json:"run_id"`
		InputDigest string `json:"input_digest"`
		PatchDigest string `json:"patch_digest"`
		Applied     bool   `json:"applied"`
		Command     string `json:"command"`
		ExitCode    *int   `json:"exit_code"`
		Outcome     string `json:"outcome"`
		LogTail     string `json:"log_tail"`
	}
	jb.json(&validation, append([]string{"implement", "result", "--result", "validation"}, destination...)...)
	if validation.RunID != run.ID || validation.InputDigest != captured.Digest || validation.PatchDigest != summary.PatchDigest || validation.Command != liveHangarValidateCommand {
		t.Fatalf("Run %d's validation %+v does not name the Run, snapshot %s, change %s and command it ran", run.Number, validation, captured.Digest, summary.PatchDigest)
	}
	if validation.Outcome != "passed" || !validation.Applied || validation.ExitCode == nil || *validation.ExitCode != 0 {
		t.Fatalf("Run %d's validation recorded %s (applied %t, exit %v), want passed: %s", run.Number, validation.Outcome, validation.Applied, validation.ExitCode, validation.LogTail)
	}

	for _, name := range []string{"change", "validation"} {
		body := api.downloadResultOf(t, ctx, liveHangarImplementTemplate, run, name)
		binding := liveHangarRequireBoundResult(t, run, name, body)
		t.Logf("Run %d's %s: %s (%d bytes), claim %s", run.Number, name, binding.Ref.Digest, len(body), binding.ClaimID)
	}
}

// liveHangarFixtureWorkerImage returns the fixture worker image the tier was
// given, and fails unless web pins it for credential handoff.
func liveHangarFixtureWorkerImage(t *testing.T) string {
	t.Helper()
	image := os.Getenv("CONCOURSE_LIVE_REVIEW_WORKER_IMAGE")
	if image == "" {
		t.Fatal("CONCOURSE_LIVE_REVIEW_WORKER_IMAGE is unset; the tier needs the review worker image with the fixture provider as its codex (hack/test-review-kubelet builds one), pinned by digest")
	}
	var pinned []string
	for _, arg := range deployed.webContainer.Args {
		if value, ok := strings.CutPrefix(arg, "--run-credential-worker-image="); ok {
			pinned = append(pinned, value)
		}
	}
	for _, value := range pinned {
		if value == image {
			return image
		}
	}
	t.Fatalf("web pins %v for credential handoff, not %s; add it to web.runCredentialWorkerImages", pinned, image)
	return ""
}

// liveHangarWorkloadTemplate reads a template from deploy/ and fills the
// operator's variables, leaving ((run_id)) for the ATC to fill per Run as
// `fly set-pipeline -v` would.
func liveHangarWorkloadTemplate(t *testing.T, file string, vars map[string]string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", file))
	if err != nil {
		t.Fatalf("read deploy/%s: %v", file, err)
	}
	config := string(raw)
	for name, value := range vars {
		placeholder := "((" + name + "))"
		if !strings.Contains(config, placeholder) {
			t.Fatalf("deploy/%s has no %s", file, placeholder)
		}
		config = strings.ReplaceAll(config, placeholder, value)
	}
	if rest := strings.ReplaceAll(config, "((run_id))", ""); strings.Contains(rest, "((") {
		t.Fatalf("deploy/%s still has a variable this check does not fill", file)
	}
	return config
}

func liveHangarRequireSucceeded(t *testing.T, run atc.PipelineRun, submission liveHangarSubmission) {
	t.Helper()
	if run.ID != submission.RunID {
		t.Fatalf("Run %d is id %d; the submission admitted %d", run.Number, run.ID, submission.RunID)
	}
	if run.Status != atc.RunStatusSucceeded || run.Terminal == nil || run.Terminal.Status != atc.RunStatusSucceeded {
		t.Fatalf("Run %d finished %q (terminal %+v), want succeeded", run.Number, run.Status, run.Terminal)
	}
}

const liveHangarJBTarget = "live"

// liveHangarJB is the `jb` CLI built from this source, with a saved fly
// target for the live-tests login.
type liveHangarJB struct {
	t      *testing.T
	ctx    context.Context
	binary string
	home   string
	// work holds what a developer keeps outside the repository: the captured
	// input, the receipt and the auth file.
	work string
}

// newLiveHangarJB builds `jb` and saves a target for api's login. The target
// is a loopback relay to web: the Run client hands credentials off only over
// HTTPS or loopback, and in the cluster the tier reaches web at its pod's
// plain-HTTP address.
func newLiveHangarJB(t *testing.T, ctx context.Context, api liveHangarAPI) liveHangarJB {
	t.Helper()
	jb := liveHangarJB{t: t, ctx: ctx, home: t.TempDir(), work: t.TempDir()}
	jb.binary = filepath.Join(t.TempDir(), "jb")
	build := exec.CommandContext(ctx, "go", "build", "-o", jb.binary, "github.com/concourse/concourse/cmd/jb")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build jb: %v: %s", err, out)
	}

	upstream, err := url.Parse(api.base)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	relay := &http.Server{Handler: &httputil.ReverseProxy{Rewrite: func(request *httputil.ProxyRequest) { request.SetURL(upstream) }}}
	go relay.Serve(listener)
	t.Cleanup(func() { relay.Close() })

	flyrc, err := json.Marshal(map[string]any{"targets": map[string]any{liveHangarJBTarget: map[string]any{
		"api":   "http://" + listener.Addr().String(),
		"team":  "main",
		"token": map[string]string{"type": "Bearer", "value": api.token},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jb.home, ".flyrc"), flyrc, 0o600); err != nil {
		t.Fatal(err)
	}
	return jb
}

// run runs jb and returns its stdout, failing on a non-zero exit.
func (jb liveHangarJB) run(args ...string) []byte {
	jb.t.Helper()
	command := exec.CommandContext(jb.ctx, jb.binary, args...)
	command.Env = append(os.Environ(), "FLY_HOME="+jb.home)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		jb.t.Fatalf("jb %s: %v\nstdout: %s\nstderr: %s", strings.Join(args, " "), err, stdout.Bytes(), stderr.Bytes())
	}
	return stdout.Bytes()
}

func (jb liveHangarJB) json(into any, args ...string) {
	jb.t.Helper()
	out := jb.run(args...)
	if err := json.Unmarshal(out, into); err != nil {
		jb.t.Fatalf("jb %s printed %q: %v", strings.Join(args, " "), out, err)
	}
}

type liveHangarSubmission struct {
	RunID  int `json:"run_id"`
	Handle struct {
		Team     string `json:"team"`
		Template string `json:"template"`
		Number   int    `json:"number"`
	} `json:"handle"`
	Ready bool   `json:"ready"`
	State string `json:"state"`
}

// submit submits a captured input and requires the Run ready: admitted, and
// its worker holding the handed-off credential.
func (jb liveHangarJB) submit(workload, template, input string) liveHangarSubmission {
	jb.t.Helper()
	auth := filepath.Join(jb.work, "auth.json")
	if err := os.WriteFile(auth, []byte(liveHangarFixtureAuth), 0o600); err != nil {
		jb.t.Fatal(err)
	}
	var submission liveHangarSubmission
	jb.json(&submission, workload, "submit", "--target", liveHangarJBTarget, "--team", "main", "--template", template,
		"--input", input, "--receipt", filepath.Join(jb.work, "receipt.json"), "--auth-file", auth)
	if !submission.Ready || submission.RunID <= 0 || submission.Handle.Number <= 0 || submission.Handle.Template != template {
		jb.t.Fatalf("jb %s submit reported %+v, want a ready Run of %s", workload, submission, template)
	}
	jb.t.Logf("%s Run %d (id %d) is ready", workload, submission.Handle.Number, submission.RunID)
	return submission
}

// liveHangarRepo is a committed local repository a developer captures from.
type liveHangarRepo struct {
	t   *testing.T
	ctx context.Context
	dir string
}

func newLiveHangarRepo(t *testing.T, ctx context.Context) liveHangarRepo {
	t.Helper()
	repo := liveHangarRepo{t: t, ctx: ctx, dir: t.TempDir()}
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.name", "Live tier"}, {"config", "user.email", "live@example.test"}, {"config", "commit.gpgsign", "false"}} {
		repo.git(args...)
	}
	return repo
}

func (repo liveHangarRepo) git(args ...string) string {
	repo.t.Helper()
	command := exec.CommandContext(repo.ctx, "git", append([]string{"-C", repo.dir}, args...)...)
	out, err := command.CombinedOutput()
	if err != nil {
		repo.t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func (repo liveHangarRepo) write(name, content string) {
	repo.t.Helper()
	if err := os.WriteFile(filepath.Join(repo.dir, name), []byte(content), 0o600); err != nil {
		repo.t.Fatal(err)
	}
}

func (repo liveHangarRepo) remove(name string) {
	repo.t.Helper()
	if err := os.Remove(filepath.Join(repo.dir, name)); err != nil {
		repo.t.Fatal(err)
	}
}

// commit commits every change and returns the commit.
func (repo liveHangarRepo) commit(message string) string {
	repo.t.Helper()
	repo.git("add", "-A")
	repo.git("commit", "-q", "-m", message)
	return repo.git("rev-parse", "HEAD")
}
