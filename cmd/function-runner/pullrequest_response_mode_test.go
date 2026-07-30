package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/functions/repositorymerge"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func TestAuthorizePRResponseModeProducesExactResponse(t *testing.T) {
	root := prResponseMounts(t, "thread-1")
	var stdout, stderr strings.Builder

	code := runCLI(context.Background(), []string{
		"authorize-pr-response",
		"--root", root,
		"--observation", "pull-request",
		"--draft", "draft-response",
		"--output", "response",
	}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("authorize-pr-response exit = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "batch-1") {
		t.Fatalf("stdout = %q, want safe batch identity", stdout.String())
	}
	raw, err := os.ReadFile(filepath.Join(root, "response", "record.json"))
	if err != nil {
		t.Fatal(err)
	}
	var record contracts.Record[contracts.PullRequestResponseBody]
	if err := contracts.DecodeSealedRecord(
		raw,
		snapshot.TypeRef("pull-request-response/v1"),
		&record,
	); err != nil {
		t.Fatalf("response record: %v", err)
	}
	if record.Body.BatchID != "batch-1" ||
		len(record.Body.Replies) != 1 ||
		record.Body.Replies[0].ThreadID != "thread-1" {
		t.Fatalf("response body = %#v", record.Body)
	}
}

func TestAuthorizePRResponseModeRejectsUnauthorizedThread(t *testing.T) {
	root := prResponseMounts(t, "thread-2")
	var stdout, stderr strings.Builder

	code := runCLI(context.Background(), []string{
		"authorize-pr-response",
		"--root", root,
		"--observation", "pull-request",
		"--draft", "draft-response",
		"--output", "response",
	}, &stdout, &stderr)
	if code != exitRejects {
		t.Fatalf("authorize-pr-response exit = %d, want %d; stderr = %s", code, exitRejects, stderr.String())
	}
	if !strings.Contains(stderr.String(), "not authorized by batch") {
		t.Fatalf("stderr = %q, want authority failure", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(root, "response", "record.json")); !os.IsNotExist(err) {
		t.Fatal("rejected response created a typed output")
	}
}

func TestAuthorizePRResponseModeDerivesNoResponseWithoutOptionalDraft(t *testing.T) {
	root := prResponseMounts(t, "thread-1")
	prResponseSetObservationTrigger(
		t,
		root,
		contracts.PullRequestConflictTrigger,
		contracts.PullRequestActive,
	)
	t.Setenv("AGENT_INPUT_DRAFT_RESPONSE_SNAPSHOT_TYPE", "")
	t.Setenv("AGENT_INPUT_DRAFT_RESPONSE_SNAPSHOT_DIGEST", "")
	var stdout, stderr strings.Builder

	code := runCLI(context.Background(), []string{
		"authorize-pr-response",
		"--root", root,
		"--observation", "pull-request",
		"--draft", "draft-response",
		"--output", "response",
	}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("authorize-pr-response exit = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "no provider response") {
		t.Fatalf("stdout = %q, want semantic absence", stdout.String())
	}
	raw, err := os.ReadFile(filepath.Join(root, "response", "record.json"))
	if err != nil {
		t.Fatal(err)
	}
	var record contracts.Record[contracts.PullRequestResponseBody]
	if err := contracts.DecodeSealedRecord(
		raw,
		snapshot.TypeRef("pull-request-response/v1"),
		&record,
	); err != nil {
		t.Fatalf("response record: %v", err)
	}
	if record.Body.Kind != contracts.PullRequestResponseNoResponse ||
		record.Body.BatchID != "" ||
		record.Body.Summary != "" ||
		len(record.Body.Replies) != 0 {
		t.Fatalf("no-response body = %#v", record.Body)
	}
}

func TestAuthorizePRResponseModeRejectsMissingReviewDraftAsPolicyNotUsage(t *testing.T) {
	root := prResponseMounts(t, "thread-1")
	t.Setenv("AGENT_INPUT_DRAFT_RESPONSE_SNAPSHOT_TYPE", "")
	t.Setenv("AGENT_INPUT_DRAFT_RESPONSE_SNAPSHOT_DIGEST", "")
	var stdout, stderr strings.Builder

	code := runCLI(context.Background(), []string{
		"authorize-pr-response",
		"--root", root,
		"--observation", "pull-request",
		"--draft", "draft-response",
		"--output", "response",
	}, &stdout, &stderr)
	if code != exitRejects {
		t.Fatalf(
			"authorize-pr-response exit = %d, want %d; stderr = %s",
			code,
			exitRejects,
			stderr.String(),
		)
	}
	if !strings.Contains(stderr.String(), "review batch requires") {
		t.Fatalf("stderr = %q, want missing-review-draft rejection", stderr.String())
	}
}

func TestAuthorizePRResponseModeRejectsInvalidInvocation(t *testing.T) {
	for name, args := range map[string][]string{
		"missing mounts": {"authorize-pr-response"},
		"aliased mounts": {
			"authorize-pr-response",
			"--observation=same",
			"--draft=same",
			"--output=response",
		},
		"unexpected argument": {
			"authorize-pr-response",
			"--observation=observation",
			"--draft=draft",
			"--output=response",
			"extra",
		},
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr strings.Builder
			if code := runCLI(
				context.Background(),
				args,
				&stdout,
				&stderr,
			); code != exitUsage {
				t.Fatalf("exit = %d, want %d; stderr = %s", code, exitUsage, stderr.String())
			}
		})
	}
}

func prResponseMounts(t *testing.T, replyThread string) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"pull-request", "draft-response", "response"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	observationBody := contracts.PullRequestBody{
		Provider:     "github",
		Repository:   "acme/widget",
		ExternalID:   "17",
		URL:          "https://github.example/acme/widget/pull/17",
		State:        contracts.PullRequestActive,
		Mergeability: contracts.PullRequestMergeable,
		SourceRef:    "refs/heads/agent/change",
		SourceSHA:    prResponseObjectID("source"),
		TargetRef:    "refs/heads/main",
		TargetSHA:    prResponseObjectID("target"),
		Iteration:    "iteration-1",
		Trigger:      contracts.PullRequestReviewBatchTrigger,
		ReviewBatches: []contracts.PullRequestReviewBatch{{
			ID:        "batch-1",
			ReviewID:  "review-1",
			CommitSHA: prResponseObjectID("source"),
			Reviewer:  "reviewer-1",
			Ready:     true,
			ThreadIDs: []string{"thread-1"},
		}},
		Threads: []contracts.PullRequestThread{
			{
				ID:        "thread-1",
				Iteration: "iteration-1",
				Comments: []contracts.PullRequestComment{{
					ID:        "comment-1",
					Author:    "reviewer-1",
					Body:      "Please update the test.",
					CommitSHA: prResponseObjectID("source"),
				}},
			},
			{
				ID:        "thread-2",
				Iteration: "iteration-1",
				Comments: []contracts.PullRequestComment{{
					ID:        "comment-2",
					Author:    "reviewer-2",
					Body:      "Unrelated context.",
					CommitSHA: prResponseObjectID("source"),
				}},
			},
		},
	}
	observation, err := contracts.NewRecord(
		snapshot.TypeRef("pull-request/v1"),
		nil,
		observationBody,
	)
	if err != nil {
		t.Fatal(err)
	}
	observationRef := snapshot.SnapshotRef{ID: 1, Type: "pull-request/v1"}
	prResponseWriteJSON(
		t,
		filepath.Join(root, "pull-request", "record.json"),
		observation,
	)
	observationDigest := prResponseCaptureDigest(
		t,
		filepath.Join(root, "pull-request"),
	)
	observationRef.Digest = observationDigest
	draft, err := contracts.NewRecord(
		snapshot.TypeRef("pull-request-response/v1"),
		[]contracts.Subject{contracts.SubjectFromInput(
			"pull-request",
			contracts.SubjectRolePrimary,
			"pull-request",
			observationRef,
		)},
		contracts.PullRequestResponseBody{
			Kind:    contracts.PullRequestResponseReviewResponse,
			BatchID: "batch-1",
			Summary: "Addressed the submitted review.",
			Replies: []contracts.PullRequestThreadResponse{{
				ThreadID: replyThread,
				Body:     "Updated in this revision.",
			}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	prResponseWriteJSON(
		t,
		filepath.Join(root, "draft-response", "record.json"),
		draft,
	)
	draftDigest := prResponseCaptureDigest(
		t,
		filepath.Join(root, "draft-response"),
	)

	t.Setenv("AGENT_INPUT_PULL_REQUEST_SNAPSHOT_TYPE", "pull-request/v1")
	t.Setenv("AGENT_INPUT_PULL_REQUEST_SNAPSHOT_DIGEST", observationDigest.String())
	t.Setenv("AGENT_INPUT_DRAFT_RESPONSE_SNAPSHOT_TYPE", "pull-request-response/v1")
	t.Setenv("AGENT_INPUT_DRAFT_RESPONSE_SNAPSHOT_DIGEST", draftDigest.String())
	responseSchema, found := contracts.SchemaDigestFor("pull-request-response/v1")
	if !found {
		t.Fatal("response schema missing")
	}
	t.Setenv("AGENT_OUTPUT_RESPONSE_RECORD_TYPE", "pull-request-response/v1")
	t.Setenv("AGENT_OUTPUT_RESPONSE_RECORD_SCHEMA", responseSchema.String())
	return root
}

func prResponseSetObservationTrigger(
	t *testing.T,
	root string,
	trigger contracts.PullRequestTrigger,
	state contracts.PullRequestState,
) {
	t.Helper()
	recordPath := filepath.Join(root, "pull-request", "record.json")
	raw, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	var record contracts.Record[contracts.PullRequestBody]
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	record.Body.Trigger = trigger
	record.Body.State = state
	record.Body.ReviewBatches = nil
	record.Body.Threads = nil
	prResponseWriteJSON(t, recordPath, record)
	digest := prResponseCaptureDigest(t, filepath.Dir(recordPath))
	t.Setenv("AGENT_INPUT_PULL_REQUEST_SNAPSHOT_DIGEST", digest.String())
}

func prResponseWriteJSON(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func prResponseDigest(seed string) snapshot.Digest {
	sum := sha256.Sum256([]byte(seed))
	return snapshot.Digest("sha256:" + hex.EncodeToString(sum[:]))
}

func prResponseCaptureDigest(t *testing.T, root string) snapshot.Digest {
	t.Helper()
	tree, err := repositorymerge.CaptureDirectory(
		context.Background(),
		snapshot.Canonicalizer{TempDir: t.TempDir()},
		root,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Close()
	return tree.Digest
}

func prResponseObjectID(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:20])
}
