package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/functions/repositorymerge"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func TestRunCLIRejectsMissingAndUnknownModes(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "no mode", args: nil},
		{name: "unknown mode", args: []string{"merge-everything"}},
		{name: "missing mounts", args: []string{"merge-preflight"}},
		{name: "unexpected argument", args: []string{"merge-preflight", "--candidate=c", "--target=t", "--base=b", "--output=o", "extra"}},
		{name: "nested mount name", args: []string{"merge-preflight", "--candidate=a/b", "--target=t", "--base=b", "--output=o"}},
		{name: "aliased mounts", args: []string{"merge-preflight", "--candidate=same", "--target=same", "--base=b", "--output=o"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr strings.Builder
			if code := runCLI(context.Background(), test.args, &stdout, &stderr); code != exitUsage {
				t.Fatalf("exit = %d, want %d (stderr: %s)", code, exitUsage, stderr.String())
			}
		})
	}
}

func TestParseMountResolvesBareNamesAgainstTheTaskRoot(t *testing.T) {
	root := t.TempDir()
	name, path, err := parseMount(root, "candidate")
	if err != nil {
		t.Fatal(err)
	}
	if name != "candidate" || path != filepath.Join(root, "candidate") {
		t.Fatalf("parseMount = %q, %q", name, path)
	}

	elsewhere := filepath.Join(t.TempDir(), "somewhere")
	name, path, err = parseMount(root, "target="+elsewhere)
	if err != nil {
		t.Fatal(err)
	}
	if name != "target" || path != elsewhere {
		t.Fatalf("parseMount = %q, %q", name, path)
	}
}

// TestMergeModesProduceASealableChangeAndAReport drives both modes exactly the
// way a task pod does: bare artifact names beneath one working directory.
func TestMergeModesProduceASealableChangeAndAReport(t *testing.T) {
	root := layoutMounts(t, "candidate.txt", "candidate\n")

	var stdout, stderr strings.Builder
	code := runCLI(context.Background(), []string{
		"merge-preflight", "--root", root,
		"--candidate", "candidate", "--target", "target", "--base", "base",
		"--output", "merge-report", "--message", "merge delivered change",
	}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("merge-preflight exit = %d (stderr: %s)", code, stderr.String())
	}
	reportBytes, err := os.ReadFile(filepath.Join(root, "merge-report", "validation-report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var report contracts.ValidationReportDocument
	if err := json.Unmarshal(reportBytes, &report); err != nil {
		t.Fatal(err)
	}
	if report.Status != "ok" {
		t.Fatalf("preflight report = %+v", report)
	}

	// Preflight mutated its own target mount, so prepare runs against a fresh
	// layout, exactly as a separate pod would.
	root = layoutMounts(t, "candidate.txt", "candidate\n")
	stdout.Reset()
	stderr.Reset()
	code = runCLI(context.Background(), []string{
		"merge-prepare", "--root", root,
		"--candidate", "candidate", "--target", "target", "--base", "base",
		"--output", "merged-change", "--message", "merge delivered change",
	}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("merge-prepare exit = %d (stderr: %s)", code, stderr.String())
	}
	recordBytes, err := os.ReadFile(filepath.Join(root, "merged-change", "record.json"))
	if err != nil {
		t.Fatal(err)
	}
	var record contracts.Record[contracts.RepositoryChangeBody]
	if err := contracts.DecodeSealedRecord(recordBytes, snapshot.TypeRef("repository-change/v1"), &record); err != nil {
		t.Fatalf("merged record.json is not a sealed repository-change/v1 record: %v", err)
	}
	if err := record.Body.Validate(record.Subjects); err != nil {
		t.Fatalf("merged record.json is invalid: %v", err)
	}
	// The merged change's base lineage is the target repository, carried as the
	// record's single base subject rather than a bare base_input field.
	if len(record.Subjects) != 1 {
		t.Fatalf("merged subjects = %+v, want exactly one base subject", record.Subjects)
	}
	if record.Subjects[0].Role != contracts.SubjectRoleBase || record.Subjects[0].Input != "target" {
		t.Fatalf("merged base subject = %+v, want the target port bound as base", record.Subjects[0])
	}
	if record.Body.Representation != "git-tree" {
		t.Fatalf("merged body = %+v", record.Body)
	}
	if !strings.HasPrefix(record.Body.Payload.Path, "content/") {
		t.Fatalf("merged payload path = %q, want it beneath content/", record.Body.Payload.Path)
	}
	if _, err := os.Stat(filepath.Join(root, "merged-change", record.Body.Payload.Path)); err != nil {
		t.Fatalf("merged payload is missing: %v", err)
	}
}

func TestMergePrepareFailsTheStepOnConflict(t *testing.T) {
	// Both sides edit base.txt, so the merge conflicts.
	root := layoutMounts(t, "base.txt", "candidate version\n")

	var stdout, stderr strings.Builder
	if code := runCLI(context.Background(), []string{
		"merge-preflight", "--root", root,
		"--candidate", "candidate", "--target", "target", "--base", "base", "--output", "merge-report",
	}, &stdout, &stderr); code != exitOK {
		t.Fatalf("merge-preflight must still exit 0 on conflict, got %d (stderr: %s)", code, stderr.String())
	}
	reportBytes, err := os.ReadFile(filepath.Join(root, "merge-report", "validation-report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var report contracts.ValidationReportDocument
	if err := json.Unmarshal(reportBytes, &report); err != nil {
		t.Fatal(err)
	}
	if report.Status != "failed" {
		t.Fatalf("preflight report = %+v", report)
	}

	root = layoutMounts(t, "base.txt", "candidate version\n")
	stdout.Reset()
	stderr.Reset()
	if code := runCLI(context.Background(), []string{
		"merge-prepare", "--root", root,
		"--candidate", "candidate", "--target", "target", "--base", "base", "--output", "merged-change",
	}, &stdout, &stderr); code != exitRejects {
		t.Fatalf("merge-prepare exit = %d, want %d (stderr: %s)", code, exitRejects, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(root, "merged-change", "record.json")); !os.IsNotExist(err) {
		t.Fatal("a conflicted merge must not materialize a merged change")
	}
}

// layoutMounts builds base, candidate, and target task mounts beneath one root.
// The target always rewrites base.txt, so a candidate that edits any other file
// merges cleanly and a candidate that edits "base.txt" conflicts.
func layoutMounts(t *testing.T, candidateFile, candidateBody string) string {
	t.Helper()
	root := t.TempDir()
	base := filepath.Join(root, "base")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	cliGit(t, base, "init", "--initial-branch=main")
	cliWrite(t, base, "base.txt", "base\n")
	cliGit(t, base, "add", ".")
	cliGit(t, base, "commit", "-m", "base")
	baseSHA := cliGit(t, base, "rev-parse", "HEAD")

	work := filepath.Join(t.TempDir(), "candidate")
	cliGit(t, filepath.Dir(work), "clone", "--no-hardlinks", base, work)
	cliWrite(t, work, candidateFile, candidateBody)
	cliGit(t, work, "add", ".")
	cliGit(t, work, "commit", "-m", "candidate work")
	cliGit(t, work, "remote", "remove", "origin")

	target := filepath.Join(root, "target")
	cliGit(t, root, "clone", "--no-hardlinks", base, target)
	cliWrite(t, target, "base.txt", "target version\n")
	cliGit(t, target, "add", ".")
	cliGit(t, target, "commit", "-m", "target moved")
	cliGit(t, target, "remote", "remove", "origin")

	payload, _ := cliCanonicalize(t, work)
	sum := sha256.Sum256(payload)
	payloadDigest, err := snapshot.ParseDigest("sha256:" + hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatal(err)
	}
	// The CLI re-derives every input reference from the materialized bytes, so
	// the candidate's base subject must name the base mount's canonical digest.
	_, baseDigest := cliCanonicalize(t, base)
	body := contracts.RepositoryChangeBody{
		RepositoryID:   repositoryIdentity(t, base),
		BaseSHA:        baseSHA,
		Representation: "git-tree",
		Payload: contracts.ContentRef{
			Path:      "content/payload.tar",
			Digest:    payloadDigest,
			MediaType: "application/octet-stream",
		},
		ResultTree:   cliGit(t, work, "rev-parse", "HEAD^{tree}"),
		ResultCommit: cliGit(t, work, "rev-parse", "HEAD"),
	}
	record, err := contracts.NewRecord(
		snapshot.TypeRef("repository-change/v1"),
		[]contracts.Subject{contracts.SubjectFromInput(
			"base", contracts.SubjectRoleBase, "base",
			snapshot.SnapshotRef{ID: 1, Type: "repository/v1", Digest: baseDigest},
		)},
		body,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := body.Validate(record.Subjects); err != nil {
		t.Fatalf("fixture candidate is not a valid repository-change body: %v", err)
	}
	candidate := filepath.Join(root, "candidate")
	if err := os.MkdirAll(filepath.Join(candidate, "content"), 0o755); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(candidate, "record.json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(candidate, body.Payload.Path), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{"merge-report", "merged-change"} {
		if err := os.MkdirAll(filepath.Join(root, output), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func cliCanonicalize(t *testing.T, directory string) ([]byte, snapshot.Digest) {
	t.Helper()
	tree, err := repositorymerge.CaptureDirectory(context.Background(), snapshot.Canonicalizer{}, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Close()
	archive, err := os.ReadFile(tree.ArchivePath)
	if err != nil {
		t.Fatal(err)
	}
	return archive, tree.Digest
}

func repositoryIdentity(t *testing.T, directory string) string {
	t.Helper()
	registry, err := contracts.NewRegistry(contracts.WithCanonicalizer(snapshot.Canonicalizer{}))
	if err != nil {
		t.Fatal(err)
	}
	validator, err := registry.Lookup("repository/v1")
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	result, err := validator.RevalidateSealed(context.Background(), root, snapshot.ValidationContext{})
	if err != nil {
		t.Fatalf("validate %s: %v", directory, err)
	}
	var metadata contracts.RepositoryMetadata
	if err := json.Unmarshal(result.IntrinsicMetadata, &metadata); err != nil {
		t.Fatal(err)
	}
	return metadata.RepositoryID
}

func cliWrite(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func cliGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	command.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=concourse-agent[bot]", "GIT_AUTHOR_EMAIL=agent@concourse.local",
		"GIT_COMMITTER_NAME=concourse-agent[bot]", "GIT_COMMITTER_EMAIL=agent@concourse.local",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}
