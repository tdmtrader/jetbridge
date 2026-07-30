package contracts_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func validPullRequestResponseBody() contracts.PullRequestResponseBody {
	return contracts.PullRequestResponseBody{
		BatchID: "batch-1",
		Summary: "I have addressed the requested change.",
		Replies: []contracts.PullRequestThreadResponse{{
			ThreadID: "thread-1", Body: "Updated in the latest revision.",
		}},
	}
}

func pullRequestWithAuthorizedThreads(threads ...string) contracts.PullRequestBody {
	body := validPullRequestBody()
	body.Threads = nil
	for index, threadID := range threads {
		body.Threads = append(body.Threads, contracts.PullRequestThread{
			ID: threadID, Iteration: "iteration-1", Comments: []contracts.PullRequestComment{{
				ID: "comment-" + string(rune('1'+index)), Author: "reviewer-1", Body: "Please revise this.", CommitSHA: strings.Repeat("a", 40),
			}},
		})
	}
	body.ReviewBatches[0].ThreadIDs = append([]string(nil), threads...)
	return body
}

func TestPullRequestResponseRejectsThreadOutsideAuthorizedObservation(t *testing.T) {
	body := validPullRequestResponseBody()
	body.Replies[0].ThreadID = "thread-not-in-subject"
	if err := contracts.ValidatePullRequestResponseAgainst(
		body,
		pullRequestWithAuthorizedThreads("thread-1"),
	); err == nil {
		t.Fatal("accepted reply to an unauthorized thread")
	}
}

func TestPullRequestResponseRejectsDuplicateReplies(t *testing.T) {
	body := validPullRequestResponseBody()
	body.Replies = append(body.Replies, body.Replies[0])
	if err := body.Validate(nil); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("Validate() error = %v, want duplicate", err)
	}
}

func TestPullRequestResponseRejectsOversizedRepliesAndIdentifiers(t *testing.T) {
	body := validPullRequestResponseBody()
	body.Replies = nil
	for index := 0; index < 513; index++ {
		body.Replies = append(body.Replies, contracts.PullRequestThreadResponse{ThreadID: fmt.Sprintf("thread-%03d", index), Body: "Updated in the latest revision."})
	}
	if err := body.Validate(nil); err == nil || !strings.Contains(err.Error(), "replies") {
		t.Fatalf("oversized replies validation error = %v", err)
	}
	body = validPullRequestResponseBody()
	body.BatchID = strings.Repeat("a", 257)
	if err := body.Validate(nil); err == nil || !strings.Contains(err.Error(), "batch id") {
		t.Fatalf("oversized batch id validation error = %v", err)
	}
}

func TestPullRequestResponseRev2RetainsPreBoundValuesWhileCurrentRejectsThem(t *testing.T) {
	subjects := []contracts.Subject{{
		ID: "primary", Role: contracts.SubjectRolePrimary, Input: "pr", Type: "pull-request/v1",
		Digest: snapshot.Digest("sha256:" + strings.Repeat("a", 64)),
	}}
	context := validationContextFor(t, map[string]snapshot.SnapshotRef{"pr": {ID: 1, Type: "pull-request/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("a", 64))}})
	for _, tc := range []struct {
		name  string
		setup func(*contracts.PullRequestResponseBody)
	}{
		{"batch id bytes", func(body *contracts.PullRequestResponseBody) {
			body.BatchID = strings.Repeat("b", 257)
		}},
		{"thread id bytes", func(body *contracts.PullRequestResponseBody) {
			body.Replies[0].ThreadID = strings.Repeat("t", 257)
		}},
		{"reply count", func(body *contracts.PullRequestResponseBody) {
			body.Replies = nil
			for index := 0; index < 513; index++ {
				body.Replies = append(body.Replies, contracts.PullRequestThreadResponse{
					ThreadID: fmt.Sprintf("thread-%03d", index), Body: "Updated in the latest revision.",
				})
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := validPullRequestResponseBody()
			tc.setup(&body)
			assertRevisionThreeBoundCompatibility(
				t,
				"pull-request-response/v1",
				subjects,
				body,
				func() error { return body.Validate(nil) },
				context,
			)
		})
	}
}

func TestNormalizeRawPullRequestResponseBodyUsesCurrentBoundsWhenSchemaIsUnset(t *testing.T) {
	body := validPullRequestResponseBody()
	body.Replies = nil
	for index := 0; index < 513; index++ {
		body.Replies = append(body.Replies, contracts.PullRequestThreadResponse{
			ThreadID: fmt.Sprintf("thread-%03d", index), Body: "Updated in the latest revision.",
		})
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	subjects := []contracts.Subject{{
		ID: "primary", Role: contracts.SubjectRolePrimary, Input: "pr", Type: "pull-request/v1",
		Digest: snapshot.Digest("sha256:" + strings.Repeat("a", 64)),
	}}
	if _, _, err := contracts.NormalizeRawRecordBody(snapshot.TypeRef("pull-request-response/v1"), subjects, raw); err == nil {
		t.Fatal("raw current-output normalization accepted an over-cap body with no schema")
	}
}
