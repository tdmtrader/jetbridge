package pullrequestresponse_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/functions/pullrequestresponse"
	"github.com/concourse/concourse/agent/functions/repositorymerge"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

const (
	observationInput = "pull-request"
	draftInput       = "draft-response"
)

func TestAuthorizeBuildsResponseOnlyForTheExactCompletedBatch(t *testing.T) {
	fixture := newFixture(t)

	record, err := pullrequestresponse.Authorize(context.Background(), fixture.request())
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if record.Type != snapshot.TypeRef("pull-request-response/v1") ||
		record.Schema != fixture.authority.Schema ||
		record.Body.BatchID != "batch-1" ||
		len(record.Body.Replies) != 1 ||
		record.Body.Replies[0].ThreadID != "thread-1" {
		t.Fatalf("authorized response = %#v", record)
	}
	wantSubject := contracts.SubjectFromInput(
		"pull-request",
		contracts.SubjectRolePrimary,
		observationInput,
		fixture.observationRef,
	)
	if len(record.Subjects) != 1 || record.Subjects[0] != wantSubject {
		t.Fatalf("subjects = %#v, want %#v", record.Subjects, []contracts.Subject{wantSubject})
	}

	output := filepath.Join(t.TempDir(), "response")
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	if err := pullrequestresponse.Write(context.Background(), output, record); err != nil {
		t.Fatalf("Write: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(output, "record.json"))
	if err != nil {
		t.Fatalf("read record: %v", err)
	}
	var decoded contracts.Record[contracts.PullRequestResponseBody]
	if err := contracts.DecodeSealedRecord(
		raw,
		snapshot.TypeRef("pull-request-response/v1"),
		&decoded,
	); err != nil {
		t.Fatalf("written record is not decodable: %v", err)
	}
	if decoded.Body.Summary != fixture.draft.Body.Summary {
		t.Fatalf("written summary = %q, want %q", decoded.Body.Summary, fixture.draft.Body.Summary)
	}
}

func TestAuthorizeRejectsReplyOutsideAuthorizedBatch(t *testing.T) {
	fixture := newFixture(t)
	fixture.draft.Body.Replies[0].ThreadID = "thread-2"
	fixture.writeDraft(t)

	_, err := pullrequestresponse.Authorize(context.Background(), fixture.request())
	if err == nil || !strings.Contains(err.Error(), "not authorized by batch") {
		t.Fatalf("Authorize error = %v, want unauthorized-thread rejection", err)
	}
}

func TestAuthorizeRejectsBatchOutsideObservation(t *testing.T) {
	fixture := newFixture(t)
	fixture.draft.Body.BatchID = "batch-2"
	fixture.writeDraft(t)

	_, err := pullrequestresponse.Authorize(context.Background(), fixture.request())
	if err == nil || !strings.Contains(err.Error(), "not present") {
		t.Fatalf("Authorize error = %v, want absent-batch rejection", err)
	}
}

func TestAuthorizeRejectsDraftThatDoesNotBindExactObservation(t *testing.T) {
	fixture := newFixture(t)
	fixture.draft.Subjects[0].Digest = digest("different-observation")
	fixture.writeDraft(t)

	_, err := pullrequestresponse.Authorize(context.Background(), fixture.request())
	if err == nil || !strings.Contains(err.Error(), "exact pull request observation") {
		t.Fatalf("Authorize error = %v, want exact-observation rejection", err)
	}
}

func TestAuthorizeRejectsMaterializedBytesOutsideDeclaredSnapshot(t *testing.T) {
	t.Run("observation", func(t *testing.T) {
		fixture := newFixture(t)
		fixture.observation.Body.TargetSHA = objectID("changed-target")
		writeJSON(
			t,
			filepath.Join(fixture.observationRoot, "record.json"),
			fixture.observation,
		)

		_, err := pullrequestresponse.Authorize(
			context.Background(),
			fixture.request(),
		)
		if err == nil || !strings.Contains(err.Error(), "exact observation snapshot") {
			t.Fatalf("Authorize error = %v, want exact-observation digest rejection", err)
		}
	})

	t.Run("draft", func(t *testing.T) {
		fixture := newFixture(t)
		fixture.draft.Body.Summary = "Changed after the platform bound this input."
		writeJSON(
			t,
			filepath.Join(fixture.draftRoot, "record.json"),
			fixture.draft,
		)

		_, err := pullrequestresponse.Authorize(
			context.Background(),
			fixture.request(),
		)
		if err == nil || !strings.Contains(err.Error(), "exact response draft snapshot") {
			t.Fatalf("Authorize error = %v, want exact-draft digest rejection", err)
		}
	})
}

func TestWriteRejectsUnsafeOutputRoots(t *testing.T) {
	fixture := newFixture(t)
	record, err := pullrequestresponse.Authorize(
		context.Background(),
		fixture.request(),
	)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("symlink", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "target")
		if err := os.Mkdir(target, 0700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(t.TempDir(), "output")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		err := pullrequestresponse.Write(
			context.Background(), link, record,
			fixture.observationRoot, fixture.draftRoot,
		)
		if err == nil || !strings.Contains(err.Error(), "not a symlink") {
			t.Fatalf("Write error = %v, want symlink rejection", err)
		}
		if _, err := os.Lstat(filepath.Join(target, "record.json")); !os.IsNotExist(err) {
			t.Fatalf("symlink target was modified: %v", err)
		}
	})

	t.Run("nested input", func(t *testing.T) {
		output := filepath.Join(fixture.observationRoot, "response")
		if err := os.Mkdir(output, 0700); err != nil {
			t.Fatal(err)
		}
		err := pullrequestresponse.Write(
			context.Background(), output, record,
			fixture.observationRoot, fixture.draftRoot,
		)
		if err == nil || !strings.Contains(err.Error(), "overlaps") {
			t.Fatalf("Write error = %v, want input overlap rejection", err)
		}
	})

	t.Run("nested input through ancestor symlink", func(t *testing.T) {
		alias := filepath.Join(t.TempDir(), "observation-alias")
		if err := os.Symlink(fixture.observationRoot, alias); err != nil {
			t.Fatal(err)
		}
		output := filepath.Join(alias, "response-through-alias")
		if err := os.Mkdir(output, 0700); err != nil {
			t.Fatal(err)
		}
		err := pullrequestresponse.Write(
			context.Background(), output, record,
			fixture.observationRoot, fixture.draftRoot,
		)
		if err == nil || !strings.Contains(err.Error(), "overlaps") {
			t.Fatalf("Write error = %v, want physical input overlap rejection", err)
		}
		if _, err := os.Lstat(filepath.Join(output, "record.json")); !os.IsNotExist(err) {
			t.Fatalf("protected input was modified through ancestor alias: %v", err)
		}
	})

	t.Run("nonempty", func(t *testing.T) {
		output := filepath.Join(t.TempDir(), "response")
		if err := os.Mkdir(output, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(output, "unexpected"), []byte("data"), 0600); err != nil {
			t.Fatal(err)
		}
		err := pullrequestresponse.Write(
			context.Background(), output, record,
			fixture.observationRoot, fixture.draftRoot,
		)
		if err == nil || !strings.Contains(err.Error(), "must be empty") {
			t.Fatalf("Write error = %v, want nonempty-root rejection", err)
		}
	})
}

func TestAuthorizeFailsClosedOnInvalidInvocation(t *testing.T) {
	fixture := newFixture(t)
	tests := map[string]func(*pullrequestresponse.Request){
		"wrong observation type": func(request *pullrequestresponse.Request) {
			request.Observation.Type = "repository/v1"
		},
		"wrong draft type": func(request *pullrequestresponse.Request) {
			request.Draft.Type = "review/v1"
		},
		"wrong response type": func(request *pullrequestresponse.Request) {
			request.ResponseAuthority.Type = "review/v1"
		},
		"missing observation port": func(request *pullrequestresponse.Request) {
			request.ObservationInput = ""
		},
		"same input port": func(request *pullrequestresponse.Request) {
			request.DraftInput = request.ObservationInput
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			request := fixture.request()
			mutate(&request)
			if _, err := pullrequestresponse.Authorize(context.Background(), request); err == nil {
				t.Fatal("Authorize accepted invalid invocation")
			}
		})
	}
	if _, err := pullrequestresponse.Authorize(nil, fixture.request()); err == nil {
		t.Fatal("Authorize accepted nil context")
	}
}

type fixture struct {
	observationRoot string
	draftRoot       string
	observationRef  snapshot.SnapshotRef
	draftRef        snapshot.SnapshotRef
	observation     contracts.Record[contracts.PullRequestBody]
	draft           contracts.Record[contracts.PullRequestResponseBody]
	authority       pullrequestresponse.RecordAuthority
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	observationRoot := t.TempDir()
	draftRoot := t.TempDir()
	observationRef := snapshot.SnapshotRef{
		ID: 41, Type: "pull-request/v1", Digest: digest("placeholder"),
	}
	draftRef := snapshot.SnapshotRef{ID: 42, Type: "pull-request-response/v1"}
	observation, err := contracts.NewRecord(
		snapshot.TypeRef("pull-request/v1"),
		nil,
		contracts.PullRequestBody{
			Provider:     "github",
			Repository:   "acme/widget",
			ExternalID:   "17",
			URL:          "https://github.example/acme/widget/pull/17",
			State:        contracts.PullRequestActive,
			Mergeability: contracts.PullRequestMergeable,
			SourceRef:    "refs/heads/agent/change",
			SourceSHA:    objectID("source"),
			TargetRef:    "refs/heads/main",
			TargetSHA:    objectID("target"),
			Iteration:    "iteration-1",
			Trigger:      contracts.PullRequestReviewBatchTrigger,
			ReviewBatches: []contracts.PullRequestReviewBatch{{
				ID:        "batch-1",
				ReviewID:  "review-1",
				CommitSHA: objectID("source"),
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
						CommitSHA: objectID("source"),
					}},
				},
				{
					ID:        "thread-2",
					Iteration: "iteration-1",
					Comments: []contracts.PullRequestComment{{
						ID:        "comment-2",
						Author:    "reviewer-2",
						Body:      "Unrelated context.",
						CommitSHA: objectID("source"),
					}},
				},
			},
		},
	)
	if err != nil {
		t.Fatalf("new observation: %v", err)
	}
	draft, err := contracts.NewRecord(
		snapshot.TypeRef("pull-request-response/v1"),
		[]contracts.Subject{contracts.SubjectFromInput(
			"pull-request",
			contracts.SubjectRolePrimary,
			observationInput,
			observationRef,
		)},
		contracts.PullRequestResponseBody{
			BatchID: "batch-1",
			Summary: "Updated the tests requested by this review.",
			Replies: []contracts.PullRequestThreadResponse{{
				ThreadID: "thread-1",
				Body:     "The regression now covers this case.",
			}},
		},
	)
	if err != nil {
		t.Fatalf("new draft: %v", err)
	}
	fixture := &fixture{
		observationRoot: observationRoot,
		draftRoot:       draftRoot,
		observationRef:  observationRef,
		draftRef:        draftRef,
		observation:     observation,
		draft:           draft,
		authority: pullrequestresponse.RecordAuthority{
			Type: "pull-request-response/v1",
			Schema: func() snapshot.Digest {
				value, _ := contracts.SchemaDigestFor("pull-request-response/v1")
				return value
			}(),
		},
	}
	writeJSON(t, filepath.Join(observationRoot, "record.json"), observation)
	fixture.observationRef.Digest = captureDigest(t, observationRoot)
	fixture.draft.Subjects[0] = contracts.SubjectFromInput(
		"pull-request",
		contracts.SubjectRolePrimary,
		observationInput,
		fixture.observationRef,
	)
	fixture.writeDraft(t)
	return fixture
}

func (fixture *fixture) request() pullrequestresponse.Request {
	return pullrequestresponse.Request{
		Observation:       fixture.observationRef,
		ObservationInput:  observationInput,
		ObservationRoot:   fixture.observationRoot,
		Draft:             fixture.draftRef,
		DraftInput:        draftInput,
		DraftRoot:         fixture.draftRoot,
		ResponseAuthority: fixture.authority,
	}
}

func (fixture *fixture) writeDraft(t *testing.T) {
	t.Helper()
	writeJSON(t, filepath.Join(fixture.draftRoot, "record.json"), fixture.draft)
	fixture.draftRef.Digest = captureDigest(t, fixture.draftRoot)
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func digest(seed string) snapshot.Digest {
	sum := sha256.Sum256([]byte(seed))
	return snapshot.Digest("sha256:" + hex.EncodeToString(sum[:]))
}

func captureDigest(t *testing.T, root string) snapshot.Digest {
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

func objectID(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:20])
}
