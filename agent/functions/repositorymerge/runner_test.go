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
	"time"

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
	record     contracts.Record[contracts.RepositoryChangeBody]
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
		Candidate:       f.inputs["candidate"],
		CandidateRoot:   f.changeRoot,
		CandidateInput:  "candidate",
		Target:          f.inputs["target"],
		TargetInput:     "target",
		TargetRoot:      f.targetRoot,
		Inputs:          f.inputs,
		OpenInput:       f.opener(),
		Method:          MethodMerge,
		Message:         "merge candidate into target",
		ReportAuthority: declaredReportAuthority(),
	}
}

// declaredReportAuthority stands in for the AGENT_OUTPUT_<PORT>_RECORD_TYPE and
// _RECORD_SCHEMA rows the platform publishes for the merge-report port. The
// values are this build's own, which is what a lockstep deployment declares.
func declaredReportAuthority() RecordAuthority {
	schema, _ := contracts.SchemaDigestFor(validationType)
	return RecordAuthority{Type: validationType, Schema: schema}
}

// sealReport writes the report to a fresh output mount and drives it through the
// REAL validation/v1 seal-time gate with the fixture's own port bindings —
// exactly what the platform does when it seals the merge-preflight step. A
// report that only satisfies this package's own checks would be worthless: the
// step fails at seal time and the conflicted delivery loses its only evidence.
func (f fixture) sealReport(t *testing.T, report contracts.Record[contracts.ValidationBody]) {
	t.Helper()
	output := t.TempDir()
	if err := WriteReport(context.Background(), output, report); err != nil {
		t.Fatalf("WriteReport: %v", err)
	}
	validator, err := f.registry.Lookup(validationType)
	if err != nil {
		t.Fatal(err)
	}
	validationContext, err := snapshot.NewValidationContext(f.inputs, f.opener())
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(output)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, err := validator.AdmitForSeal(context.Background(), root, validationContext); err != nil {
		t.Fatalf("the merge report is not a sealable %s: %v", validationType, err)
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
	payloadSum := sha256.Sum256(payload)
	payloadDigest, err := snapshot.ParseDigest("sha256:" + hex.EncodeToString(payloadSum[:]))
	if err != nil {
		t.Fatal(err)
	}

	baseRef := snapshot.SnapshotRef{ID: 1, Type: "repository/v1", Digest: baseDigest}
	body := contracts.RepositoryChangeBody{
		RepositoryID:   repositoryIdentityOf(t, registry, base),
		BaseSHA:        baseSHA,
		Representation: "git-tree",
		Payload: contracts.ContentRef{
			Path:      "content/payload.tar",
			Digest:    payloadDigest,
			MediaType: "application/octet-stream",
		},
		ResultTree:   git(t, candidate, "rev-parse", "HEAD^{tree}"),
		ResultCommit: git(t, candidate, "rev-parse", "HEAD"),
	}
	// The candidate is a sealed record: its base lineage is the base SUBJECT,
	// bound at the declared "base" port, not a bare field in the body.
	record, err := contracts.NewRecord(
		snapshot.TypeRef("repository-change/v1"),
		[]contracts.Subject{contracts.SubjectFromInput(
			"base", contracts.SubjectRoleBase, "base", baseRef,
		)},
		body,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := body.Validate(record.Subjects); err != nil {
		t.Fatalf("fixture candidate is not a valid repository-change body: %v", err)
	}
	changeRoot := filepath.Join(tmp, "change")
	if err := os.MkdirAll(filepath.Join(changeRoot, "content"), 0o755); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(changeRoot, "record.json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(changeRoot, body.Payload.Path), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	changeArchive, changeDigest := captureDirectory(t, changeRoot)

	return fixture{
		registry:   registry,
		targetRoot: target,
		changeRoot: changeRoot,
		record:     record,
		inputs: map[string]snapshot.SnapshotRef{
			"base":      baseRef,
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

	if merged.Conclusion() != "passed" {
		t.Fatalf("report = %+v", merged.Report)
	}
	// The report is a sealed validation/v1 record whose PRIMARY subject is the
	// exact candidate snapshot it judged, bound at the port it was declared on.
	if len(merged.Report.Subjects) != 2 {
		t.Fatalf("report subjects = %+v, want the candidate and the target", merged.Report.Subjects)
	}
	primary := merged.Report.Subjects[0]
	if primary.Role != contracts.SubjectRolePrimary || primary.Input != "candidate" ||
		primary.Digest != f.inputs["candidate"].Digest {
		t.Fatalf("report primary subject = %+v, want the exact candidate bound at its port", primary)
	}
	if context := merged.Report.Subjects[1]; context.Role != contracts.SubjectRoleContext || context.Input != "target" {
		t.Fatalf("report context subject = %+v, want the target port", context)
	}
	f.sealReport(t, merged.Report)
	// The merged change's base lineage is now a SUBJECT: the target repository,
	// bound at the declared port the request named.
	if len(merged.Change.Subjects) != 1 {
		t.Fatalf("subjects = %+v, want exactly one base subject", merged.Change.Subjects)
	}
	baseSubject := merged.Change.Subjects[0]
	if baseSubject.Role != contracts.SubjectRoleBase || baseSubject.Input != "target" {
		t.Fatalf("base subject = %+v, want the target port bound as base", baseSubject)
	}
	if baseSubject.Type != f.inputs["target"].Type || baseSubject.Digest != f.inputs["target"].Digest {
		t.Fatalf("base subject = %+v, want the exact target snapshot", baseSubject)
	}
	if merged.Change.Body.Representation != "git-tree" {
		t.Fatalf("representation = %q", merged.Change.Body.Representation)
	}
	// base_sha must be the tip we rebased onto, not the candidate's own base.
	if merged.Change.Body.BaseSHA == f.record.Body.BaseSHA {
		t.Fatal("merged base_sha must be the target tip, not the candidate's base")
	}
	// The merge commit carries the candidate's result commit as a trailer.
	body := git(t, f.targetRoot, "log", "-1", "--format=%B", merged.Change.Body.ResultCommit)
	if !strings.Contains(body, TrailerKey+": "+f.record.Body.ResultCommit) {
		t.Fatalf("merge commit message lacks the %s trailer:\n%s", TrailerKey, body)
	}

	// The sealed value must satisfy repository-change/v1 with the target bound
	// as its base subject's input — exactly what the platform checks when it
	// seals the step's typed output.
	output := t.TempDir()
	if err := WriteMergedChange(context.Background(), output, merged); err != nil {
		t.Fatalf("WriteMergedChange: %v", err)
	}
	// The payload must live beneath content/, and the envelope beside it.
	if got := merged.Change.Body.Payload.Path; got != "content/payload.tar" {
		t.Fatalf("payload path = %q, want it beneath content/", got)
	}
	if _, err := os.Stat(filepath.Join(output, merged.Change.Body.Payload.Path)); err != nil {
		t.Fatalf("merged payload is missing beneath content/: %v", err)
	}
	if _, err := os.Stat(filepath.Join(output, "record.json")); err != nil {
		t.Fatalf("merged record.json is missing: %v", err)
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
	if report.Body.Conclusion != "failed" {
		t.Fatalf("report = %+v", report.Body)
	}
	// The conflict is ONE check with ONE attempt; the unmerged paths are that
	// attempt's evidence, anchored to the candidate being judged.
	if len(report.Body.Checks) != 1 || report.Body.Checks[0].ID != mergeCheckID ||
		report.Body.Checks[0].Status != "failed" || len(report.Body.Checks[0].Attempts) != 1 {
		t.Fatalf("checks = %+v, want one failed check with one attempt", report.Body.Checks)
	}
	attempt := report.Body.Checks[0].Attempts[0]
	if attempt.Number != 1 || attempt.Status != "failed" {
		t.Fatalf("attempt = %+v", attempt)
	}
	if _, err := time.ParseDuration(attempt.Duration); err != nil {
		t.Fatalf("attempt duration %q is not a Go duration: %v", attempt.Duration, err)
	}
	found := false
	for _, anchor := range attempt.Evidence {
		if anchor.Subject == candidateSubjectID && anchor.Locator.Kind == "opaque" && anchor.Locator.Value == "base.txt" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected base.txt anchored as evidence, got %+v", attempt.Evidence)
	}
	f.sealReport(t, report)
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
	if report.Body.Conclusion != "failed" {
		t.Fatalf("report = %+v", report.Body)
	}
	if !strings.Contains(report.Body.Checks[0].Detail, "not the delivery target") {
		t.Fatalf("checks = %+v", report.Body.Checks)
	}
	f.sealReport(t, report)
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

	unboundCandidate := f.request()
	unboundCandidate.CandidateInput = "not-a-declared-input"
	if _, err := runner.Merge(context.Background(), unboundCandidate); err == nil || !strings.Contains(err.Error(), "not-a-declared-input") {
		t.Fatalf("unbound candidate input error = %v", err)
	}

	unknownMethod := f.request()
	unknownMethod.Method = "yolo"
	if _, err := runner.Merge(context.Background(), unknownMethod); err == nil {
		t.Fatal("an unknown merge method must be rejected")
	}

	// The report identity is declared by the platform. A caller that supplies
	// none cannot produce a sealable report, and must be told before the merge
	// is computed rather than after.
	noAuthority := f.request()
	noAuthority.ReportAuthority = RecordAuthority{}
	if _, err := runner.Merge(context.Background(), noAuthority); err == nil || !strings.Contains(err.Error(), "merge report type") {
		t.Fatalf("missing report authority error = %v", err)
	}
	wrongSchema := f.request()
	wrongSchema.ReportAuthority.Schema = "not-a-digest"
	if _, err := runner.Merge(context.Background(), wrongSchema); err == nil || !strings.Contains(err.Error(), "merge report schema") {
		t.Fatalf("malformed report schema error = %v", err)
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

// TestReportCopiesTheDeclaredContractIdentityVerbatim pins the one thing the pod
// is NOT allowed to decide for itself.
//
// The type and schema digest come from the platform as
// AGENT_OUTPUT_<PORT>_RECORD_* rows. The agent-runner image ships independently
// of the web node, so a runner that stamped its own compiled digest would
// advertise an identity the sealing side never declared — and one that re-judged
// the digest it was handed would refuse to write the only durable artifact a
// conflicted delivery produces. Both halves are asserted here with a digest this
// build would never choose on its own.
func TestReportCopiesTheDeclaredContractIdentityVerbatim(t *testing.T) {
	f := newFixture(t, "candidate.txt", "candidate\n", "target.txt", "target\n")
	runner, err := NewRunner(f.registry)
	if err != nil {
		t.Fatal(err)
	}
	declared := snapshot.Digest("sha256:" + strings.Repeat("b", 64))
	if current, _ := contracts.SchemaDigestFor(validationType); current == declared {
		t.Fatal("the fixture digest must differ from this build's own, or the test proves nothing")
	}
	request := f.request()
	request.ReportAuthority = RecordAuthority{Type: validationType, Schema: declared}

	report, err := runner.Run(context.Background(), request)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Type != validationType || report.Schema != declared {
		t.Fatalf("envelope = %s %s, want the declared identity %s %s", report.Type, report.Schema, validationType, declared)
	}

	output := t.TempDir()
	if err := WriteReport(context.Background(), output, report); err != nil {
		t.Fatalf("WriteReport must not re-judge a digest the platform issued: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(output, "record.json"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded["record_version"] != contracts.RecordVersion ||
		decoded["type"] != validationType.String() || decoded["schema"] != declared.String() {
		t.Fatalf("record.json = %s", data)
	}
}

func TestWriteReportRejectsAReportItIsTheAuthorityOn(t *testing.T) {
	f := newFixture(t, "candidate.txt", "candidate\n", "target.txt", "target\n")
	runner, err := NewRunner(f.registry)
	if err != nil {
		t.Fatal(err)
	}
	report, err := runner.Run(context.Background(), f.request())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	for name, corrupt := range map[string]func(*contracts.Record[contracts.ValidationBody]){
		"wrong record version": func(r *contracts.Record[contracts.ValidationBody]) { r.RecordVersion = "2.0.0" },
		"wrong contract type":  func(r *contracts.Record[contracts.ValidationBody]) { r.Type = "review/v1" },
		"malformed digest":     func(r *contracts.Record[contracts.ValidationBody]) { r.Schema = "not-a-digest" },
		"unsorted subjects": func(r *contracts.Record[contracts.ValidationBody]) {
			r.Subjects[0], r.Subjects[1] = r.Subjects[1], r.Subjects[0]
		},
		"conclusion disagrees with the checks": func(r *contracts.Record[contracts.ValidationBody]) {
			r.Body.Conclusion = "failed"
		},
	} {
		t.Run(name, func(t *testing.T) {
			broken := report
			broken.Subjects = append([]contracts.Subject(nil), report.Subjects...)
			corrupt(&broken)
			if err := WriteReport(context.Background(), t.TempDir(), broken); err == nil {
				t.Fatalf("WriteReport(%s) succeeded, want a rejection", name)
			}
		})
	}
}
