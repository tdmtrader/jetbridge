package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func TestPRMonitorMaterializeModeProducesExactRepositoryOutputs(t *testing.T) {
	root := cliPRMonitorMounts(t, false)
	var stdout, stderr strings.Builder
	code := runCLI(context.Background(), []string{
		"pr-monitor-materialize",
		"--root", root,
		"--observation", "pull-request",
		"--source-output", "source",
		"--target-output", "target",
	}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("pr-monitor-materialize exit = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "source=") ||
		!strings.Contains(stdout.String(), "target=") {
		t.Fatalf("stdout = %q, want safe exact heads", stdout.String())
	}
	for _, name := range []string{"source", "target"} {
		if got := cliGit(t, filepath.Join(root, name), "status", "--porcelain=v1"); got != "" {
			t.Fatalf("%s output is dirty: %q", name, got)
		}
	}
}

func TestPRMonitorMaterializeModeRejectsMismatchedHead(t *testing.T) {
	root := cliPRMonitorMounts(t, true)
	var stdout, stderr strings.Builder
	code := runCLI(context.Background(), []string{
		"pr-monitor-materialize",
		"--root", root,
		"--observation", "pull-request",
		"--source-output", "source",
		"--target-output", "target",
	}, &stdout, &stderr)
	if code != exitRejects {
		t.Fatalf("pr-monitor-materialize exit = %d, want %d; stderr = %s", code, exitRejects, stderr.String())
	}
	if !strings.Contains(stderr.String(), "source HEAD") {
		t.Fatalf("stderr = %q, want exact-head rejection", stderr.String())
	}
}

func cliPRMonitorMounts(t *testing.T, mismatched bool) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"pull-request", "source", "target"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	base := filepath.Join(t.TempDir(), "base")
	if err := os.Mkdir(base, 0700); err != nil {
		t.Fatal(err)
	}
	cliGit(t, base, "init", "--initial-branch=main")
	cliWrite(t, base, "base.txt", "base\n")
	cliGit(t, base, "add", ".")
	cliGit(t, base, "commit", "-m", "base")
	resource := filepath.Join(root, "pull-request")
	source := filepath.Join(resource, "source-repository")
	target := filepath.Join(resource, "target-repository")
	cliGit(t, resource, "clone", "--no-hardlinks", base, source)
	cliGit(t, source, "remote", "remove", "origin")
	cliWrite(t, source, "source.txt", "source\n")
	cliGit(t, source, "add", ".")
	cliGit(t, source, "commit", "-m", "source")
	cliGit(t, resource, "clone", "--no-hardlinks", base, target)
	cliGit(t, target, "remote", "remove", "origin")
	cliWrite(t, target, "target.txt", "target\n")
	cliGit(t, target, "add", ".")
	cliGit(t, target, "commit", "-m", "target")
	sourceSHA := cliGit(t, source, "rev-parse", "HEAD")
	targetSHA := cliGit(t, target, "rev-parse", "HEAD")
	if mismatched {
		sourceSHA = strings.Repeat("f", 40)
	}
	record, err := contracts.NewRecord(
		snapshot.TypeRef("pull-request/v1"),
		nil,
		contracts.PullRequestBody{
			Provider:     "github",
			Repository:   "acme/widget",
			ExternalID:   "17",
			URL:          "https://github.example/acme/widget/pull/17",
			State:        contracts.PullRequestActive,
			Mergeability: contracts.PullRequestConflicted,
			SourceRef:    "refs/heads/agent/change",
			SourceSHA:    sourceSHA,
			TargetRef:    "refs/heads/main",
			TargetSHA:    targetSHA,
			Iteration:    "iteration-1",
			Trigger:      contracts.PullRequestConflictTrigger,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resource, "record.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	_, observationDigest := cliCanonicalize(t, resource)
	t.Setenv("AGENT_INPUT_PULL_REQUEST_SNAPSHOT_TYPE", "pull-request/v1")
	t.Setenv("AGENT_INPUT_PULL_REQUEST_SNAPSHOT_DIGEST", observationDigest.String())
	return root
}
