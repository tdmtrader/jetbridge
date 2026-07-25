package repositorymerge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

var botEnv = []string{
	"GIT_AUTHOR_NAME=concourse-agent[bot]",
	"GIT_AUTHOR_EMAIL=agent@concourse.local",
	"GIT_COMMITTER_NAME=concourse-agent[bot]",
	"GIT_COMMITTER_EMAIL=agent@concourse.local",
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), botEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// scenario builds an origin with `main` plus a delivered branch
// `agent/change-1`, and returns a working clone. When advanceTarget is
// non-empty, main gains a further commit writing that content to target.txt,
// so the branch is behind.
func scenario(t *testing.T, branchFile, branchBody, advanceTarget string) (ws string) {
	t.Helper()
	tmp := t.TempDir()
	bare := filepath.Join(tmp, "origin.git")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, bare, "init", "--bare", "--initial-branch=main")

	seed := filepath.Join(tmp, "seed")
	git(t, tmp, "clone", bare, seed)
	write(t, seed, "base.txt", "base\n")
	git(t, seed, "add", ".")
	git(t, seed, "commit", "-m", "base")
	git(t, seed, "push", "origin", "HEAD:main")

	// delivered branch off main
	git(t, seed, "checkout", "-b", "agent/change-1")
	write(t, seed, branchFile, branchBody)
	git(t, seed, "add", ".")
	git(t, seed, "commit", "-m", "agent work")
	git(t, seed, "push", "origin", "HEAD:refs/heads/agent/change-1")

	if advanceTarget != "" {
		git(t, seed, "checkout", "main")
		write(t, seed, "target.txt", advanceTarget)
		git(t, seed, "add", ".")
		git(t, seed, "commit", "-m", "target moved")
		git(t, seed, "push", "origin", "HEAD:main")
	}

	ws = filepath.Join(tmp, "ws")
	git(t, tmp, "clone", bare, ws)
	git(t, ws, "fetch", "origin", "+refs/heads/*:refs/remotes/origin/*")
	return ws
}

func TestPrepareCleanMergeProducesResultSha(t *testing.T) {
	ws := scenario(t, "f.txt", "work\n", "moved\n")
	targetBefore := git(t, ws, "rev-parse", "origin/main")

	res, err := Prepare(ws, Plan{
		Branch: "origin/agent/change-1", Target: "origin/main",
		Method: MethodMerge, Message: "merge change 1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Ok || res.Conflict {
		t.Fatalf("expected a clean merge, got %+v", res)
	}
	if res.ResultSha == "" || res.ResultSha == targetBefore {
		t.Fatalf("expected a new result sha, got %q", res.ResultSha)
	}
}

func TestPrepareNeverTouchesTheRemote(t *testing.T) {
	ws := scenario(t, "f.txt", "work\n", "moved\n")
	before := git(t, ws, "rev-parse", "origin/main")

	if _, err := Prepare(ws, Plan{
		Branch: "origin/agent/change-1", Target: "origin/main",
		Method: MethodMerge, Message: "merge change 1",
	}); err != nil {
		t.Fatal(err)
	}

	git(t, ws, "fetch", "origin")
	if after := git(t, ws, "rev-parse", "origin/main"); after != before {
		t.Fatal("Prepare must be speculative — it must never push to the remote")
	}
}

func TestPrepareReportsConflictAndLeavesRepoClean(t *testing.T) {
	// both sides edit base.txt => conflict
	ws := scenario(t, "base.txt", "agent version\n", "")
	git(t, ws, "checkout", "main")
	write(t, ws, "base.txt", "target version\n")
	git(t, ws, "add", ".")
	git(t, ws, "commit", "-m", "target edits base")
	git(t, ws, "push", "origin", "HEAD:main")
	git(t, ws, "fetch", "origin")

	res, err := Prepare(ws, Plan{
		Branch: "origin/agent/change-1", Target: "origin/main",
		Method: MethodMerge, Message: "merge change 1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Ok || !res.Conflict {
		t.Fatalf("expected a conflict, got %+v", res)
	}
	if len(res.ConflictPaths) == 0 || res.ConflictPaths[0] != "base.txt" {
		t.Fatalf("expected base.txt in conflict paths, got %v", res.ConflictPaths)
	}
	// the working tree must be usable afterwards: no merge left in progress
	if _, err := os.Stat(filepath.Join(ws, ".git", "MERGE_HEAD")); !os.IsNotExist(err) {
		t.Fatal("a conflicting Prepare must abort the merge, leaving no MERGE_HEAD")
	}
	// repository/v1 rejects a dirty work tree, so the abort must also have
	// swept the conflict residue.
	if status := git(t, ws, "status", "--porcelain=v1", "--untracked-files=all"); status != "" {
		t.Fatalf("a conflicting Prepare must leave a clean work tree, got:\n%s", status)
	}
}

func TestPrepareSquashProducesASingleCommit(t *testing.T) {
	ws := scenario(t, "f.txt", "work\n", "moved\n")

	res, err := Prepare(ws, Plan{
		Branch: "origin/agent/change-1", Target: "origin/main",
		Method: MethodSquash, Message: "squash change 1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Ok {
		t.Fatalf("expected a clean squash, got %+v", res)
	}
	// a squash result has exactly one parent (the target head)
	parents := git(t, ws, "rev-list", "--parents", "-n", "1", res.ResultSha)
	if n := len(strings.Fields(parents)) - 1; n != 1 {
		t.Fatalf("expected a squash commit with 1 parent, got %d (%q)", n, parents)
	}
}

func TestPrepareRejectsUnknownMethod(t *testing.T) {
	ws := scenario(t, "f.txt", "work\n", "")
	if _, err := Prepare(ws, Plan{
		Branch: "origin/agent/change-1", Target: "origin/main", Method: "yolo",
	}); err == nil {
		t.Fatal("an unknown merge method must be rejected, not silently defaulted")
	}
}

func TestAppendTrailerJoinsAnExistingBlockAndOtherwiseStartsOne(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    string
	}{
		{name: "empty", message: "", want: "Agent-Change: abc"},
		{name: "subject only", message: "merge", want: "merge\n\nAgent-Change: abc"},
		{
			name:    "existing block",
			message: "merge\n\nCo-Authored-By: someone <s@example.test>",
			want:    "merge\n\nCo-Authored-By: someone <s@example.test>\nAgent-Change: abc",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := appendTrailer(test.message, "Agent-Change: abc"); got != test.want {
				t.Fatalf("appendTrailer = %q, want %q", got, test.want)
			}
		})
	}
}

// --- runner fixtures -------------------------------------------------------

type fixture struct {
	registry   *contracts.Registry
	targetRoot string
	changeRoot string
	inputs     map[string]snapshot.SnapshotRef
	archives   map[string][]byte
	document   contracts.RepositoryChangeDocument
}

func (f fixture) opener() snapshot.InputOpener {
	return func(_ context.Context, name string, _ snapshot.SnapshotRef) (io.ReadCloser, error) {
		archive, found := f.archives[name]
		if !found {
			return nil, errors.New("no such input")
		}
		return io.NopCloser(bytes.NewReader(archive)), nil
	}
}

func (f fixture) request() Request {
	return Request{
		Candidate:     f.inputs["candidate"],
		CandidateRoot: f.changeRoot,
		Target:        f.inputs["target"],
		TargetInput:   "target",
		TargetRoot:    f.targetRoot,
		Inputs:        f.inputs,
		OpenInput:     f.opener(),
		Method:        MethodMerge,
		Message:       "merge candidate into target",
	}
}

// newFixture builds a real base repository, a candidate repository-change/v1
// value derived from it, and a target repository whose tip has moved on.
// candidateFile/targetFile decide whether the two edits conflict.
func newFixture(t *testing.T, candidateFile, candidateBody, targetFile, targetBody string) fixture {
	t.Helper()
	return buildFixture(t, candidateFile, candidateBody, targetFile, targetBody, false)
}

// newUnrelatedTargetFixture builds a valid candidate whose base repository is a
// different repository from the delivery target.
func newUnrelatedTargetFixture(t *testing.T) fixture {
	t.Helper()
	return buildFixture(t, "candidate.txt", "candidate\n", "target.txt", "target\n", true)
}

func buildFixture(t *testing.T, candidateFile, candidateBody, targetFile, targetBody string, unrelatedTarget bool) fixture {
	t.Helper()
	registry, err := contracts.NewRegistry(contracts.WithCanonicalizer(snapshot.Canonicalizer{}))
	if err != nil {
		t.Fatal(err)
	}

	tmp := t.TempDir()
	base := filepath.Join(tmp, "base")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, base, "init", "--initial-branch=main")
	write(t, base, "base.txt", "base\n")
	git(t, base, "add", ".")
	git(t, base, "commit", "-m", "base")
	baseSHA := git(t, base, "rev-parse", "HEAD")

	candidate := filepath.Join(tmp, "candidate")
	git(t, tmp, "clone", "--no-hardlinks", base, candidate)
	write(t, candidate, candidateFile, candidateBody)
	git(t, candidate, "add", ".")
	git(t, candidate, "commit", "-m", "candidate work")

	target := filepath.Join(tmp, "target")
	if unrelatedTarget {
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatal(err)
		}
		git(t, target, "init", "--initial-branch=main")
		write(t, target, "unrelated.txt", "unrelated root\n")
		git(t, target, "add", ".")
		git(t, target, "commit", "-m", "unrelated base")
	} else {
		git(t, tmp, "clone", "--no-hardlinks", base, target)
	}
	write(t, target, targetFile, targetBody)
	git(t, target, "add", ".")
	git(t, target, "commit", "-m", "target moved")
	// A clone leaves a remote pointing at the seed directory. Delivery never
	// resolves a remote, and dropping it keeps the fixture honest.
	if !unrelatedTarget {
		git(t, target, "remote", "remove", "origin")
	}
	git(t, candidate, "remote", "remove", "origin")

	baseArchive, baseDigest := captureDirectory(t, base)
	targetArchive, targetDigest := captureDirectory(t, target)
	payload, _ := captureDirectory(t, candidate)
	payloadDigest := sha256.Sum256(payload)

	document := contracts.RepositoryChangeDocument{
		SchemaVersion:  "1.0.0",
		RepositoryID:   repositoryIdentityOf(t, registry, base),
		BaseInput:      "base",
		BaseSHA:        baseSHA,
		ResultSHA:      git(t, candidate, "rev-parse", "HEAD"),
		ResultTreeSHA:  git(t, candidate, "rev-parse", "HEAD^{tree}"),
		Representation: "git-tree",
		PayloadPath:    "payload.tar",
		PayloadDigest:  "sha256:" + hex.EncodeToString(payloadDigest[:]),
	}
	changeRoot := filepath.Join(tmp, "change")
	if err := os.MkdirAll(changeRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(changeRoot, "change.json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(changeRoot, "payload.tar"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	changeArchive, changeDigest := captureDirectory(t, changeRoot)

	return fixture{
		registry:   registry,
		targetRoot: target,
		changeRoot: changeRoot,
		document:   document,
		inputs: map[string]snapshot.SnapshotRef{
			"base":      {ID: 1, Type: "repository/v1", Digest: baseDigest},
			"target":    {ID: 2, Type: "repository/v1", Digest: targetDigest},
			"candidate": {ID: 3, Type: "repository-change/v1", Digest: changeDigest},
		},
		archives: map[string][]byte{
			"base": baseArchive, "target": targetArchive, "candidate": changeArchive,
		},
	}
}

func captureDirectory(t *testing.T, directory string) ([]byte, snapshot.Digest) {
	t.Helper()
	tree, err := CaptureDirectory(context.Background(), snapshot.Canonicalizer{}, directory)
	if err != nil {
		t.Fatalf("capture %s: %v", directory, err)
	}
	defer tree.Close()
	archive, err := os.ReadFile(tree.ArchivePath)
	if err != nil {
		t.Fatal(err)
	}
	return archive, tree.Digest
}

func repositoryIdentityOf(t *testing.T, registry *contracts.Registry, directory string) string {
	t.Helper()
	validator, err := registry.Lookup("repository/v1")
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := repositoryMetadata(context.Background(), validator, directory, snapshot.ValidationContext{})
	if err != nil {
		t.Fatalf("repository metadata for %s: %v", directory, err)
	}
	return metadata.RepositoryID
}

// --- runner behaviour ------------------------------------------------------

func TestRunnerMergesOntoTheAdvancedTargetAndSealsAValidChange(t *testing.T) {
	f := newFixture(t, "candidate.txt", "candidate\n", "target.txt", "target\n")
	runner, err := NewRunner(f.registry)
	if err != nil {
		t.Fatal(err)
	}
	merged, err := runner.Merge(context.Background(), f.request())
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	defer merged.Close()

	if merged.Report.Status != "ok" {
		t.Fatalf("report = %+v", merged.Report)
	}
	if err := merged.Report.Validate(); err != nil {
		t.Fatalf("invalid validation-report/v1: %v", err)
	}
	if merged.Report.Subject != "snapshot:"+f.inputs["candidate"].ID.String()+"@"+f.inputs["candidate"].Digest.String() {
		t.Fatalf("subject = %q", merged.Report.Subject)
	}
	if merged.Change.BaseInput != "target" {
		t.Fatalf("base_input = %q, want the target port", merged.Change.BaseInput)
	}
	if merged.Change.Representation != "git-tree" {
		t.Fatalf("representation = %q", merged.Change.Representation)
	}
	// base_sha must be the tip we rebased onto, not the candidate's own base.
	if merged.Change.BaseSHA == f.document.BaseSHA {
		t.Fatal("merged base_sha must be the target tip, not the candidate's base")
	}
	// The merge commit carries the candidate's result commit as a trailer.
	body := git(t, f.targetRoot, "log", "-1", "--format=%B", merged.Change.ResultSHA)
	if !strings.Contains(body, TrailerKey+": "+f.document.ResultSHA) {
		t.Fatalf("merge commit message lacks the %s trailer:\n%s", TrailerKey, body)
	}

	// The sealed value must satisfy repository-change/v1 with the target bound
	// as its base input — exactly what the platform checks when it seals the
	// step's typed output.
	output := t.TempDir()
	if err := WriteMergedChange(context.Background(), output, merged); err != nil {
		t.Fatalf("WriteMergedChange: %v", err)
	}
	validator, err := f.registry.Lookup("repository-change/v1")
	if err != nil {
		t.Fatal(err)
	}
	validationContext, err := snapshot.NewValidationContext(f.inputs, f.opener())
	if err != nil {
		t.Fatal(err)
	}
	if err := validateTree(context.Background(), validator, output, validationContext); err != nil {
		t.Fatalf("merged output is not a valid repository-change/v1: %v", err)
	}
}

func TestRunnerReportsConflictAsAStatusRatherThanAnError(t *testing.T) {
	f := newFixture(t, "base.txt", "candidate version\n", "base.txt", "target version\n")
	runner, err := NewRunner(f.registry)
	if err != nil {
		t.Fatal(err)
	}
	// Run is the preflight mode: it always returns a report so a human can see
	// the conflict before an approval is requested.
	report, err := runner.Run(context.Background(), f.request())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Status != "failed" {
		t.Fatalf("report = %+v", report)
	}
	if err := report.Validate(); err != nil {
		t.Fatalf("invalid validation-report/v1: %v", err)
	}
	found := false
	for _, check := range report.Checks {
		if check.Name == "base.txt" && check.Status == "failed" && check.Detail == "unmerged" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an unmerged check for base.txt, got %+v", report.Checks)
	}
	if status := git(t, f.targetRoot, "status", "--porcelain=v1", "--untracked-files=all"); status != "" {
		t.Fatalf("a conflicting merge must leave a clean work tree, got:\n%s", status)
	}
}

func TestRunnerRejectsAValidCandidateForADifferentRepository(t *testing.T) {
	// The candidate is entirely valid against its own base; it simply belongs
	// to another repository than the delivery target. Merging it would graft an
	// unrelated history, so it must be refused before any git work happens.
	f := newUnrelatedTargetFixture(t)
	runner, err := NewRunner(f.registry)
	if err != nil {
		t.Fatal(err)
	}
	report, err := runner.Run(context.Background(), f.request())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Status != "failed" {
		t.Fatalf("report = %+v", report)
	}
	if !strings.Contains(report.Checks[0].Detail, "not the delivery target") {
		t.Fatalf("checks = %+v", report.Checks)
	}
	if err := report.Validate(); err != nil {
		t.Fatalf("invalid validation-report/v1: %v", err)
	}
}

func TestRunnerRejectsMalformedInvocationAndHonorsCancellation(t *testing.T) {
	f := newFixture(t, "candidate.txt", "candidate\n", "target.txt", "target\n")
	runner, err := NewRunner(f.registry)
	if err != nil {
		t.Fatal(err)
	}

	wrongType := f.request()
	wrongType.Candidate.Type = "repository/v1"
	if _, err := runner.Merge(context.Background(), wrongType); err == nil || !strings.Contains(err.Error(), "repository-change/v1") {
		t.Fatalf("wrong candidate type error = %v", err)
	}

	unbound := f.request()
	unbound.TargetInput = "not-a-declared-input"
	if _, err := runner.Merge(context.Background(), unbound); err == nil || !strings.Contains(err.Error(), "not-a-declared-input") {
		t.Fatalf("unbound target input error = %v", err)
	}

	unknownMethod := f.request()
	unknownMethod.Method = "yolo"
	if _, err := runner.Merge(context.Background(), unknownMethod); err == nil {
		t.Fatal("an unknown merge method must be rejected")
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runner.Merge(canceled, f.request()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}

func TestNewRunnerRequiresARegistry(t *testing.T) {
	if _, err := NewRunner(nil); err == nil {
		t.Fatal("a nil registry must be rejected")
	}
	var typed *contracts.Registry
	if _, err := NewRunner(typed); err == nil {
		t.Fatal("a typed-nil registry must be rejected")
	}
}

func TestWriteReportEmitsStrictSnapshotDocument(t *testing.T) {
	request := Request{Candidate: snapshot.SnapshotRef{
		ID: 7, Type: "repository-change/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("a", 64)),
	}}
	document := reportFor(request, "ok", "merge is clean", nil)
	output := t.TempDir()
	if err := WriteReport(context.Background(), output, document); err != nil {
		t.Fatalf("WriteReport: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(output, "validation-report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded["schema_version"] != "1.0.0" || decoded["status"] != "ok" {
		t.Fatalf("payload = %s", data)
	}
}
