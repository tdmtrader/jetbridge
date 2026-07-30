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

func TestPullRequestResponseNoResponseIsSemanticAbsence(t *testing.T) {
	noResponse := contracts.PullRequestResponseBody{
		Kind: contracts.PullRequestResponseNoResponse,
	}
	if err := noResponse.Validate(nil); err != nil {
		t.Fatalf("no-response Validate() = %v", err)
	}
	raw, err := json.Marshal(noResponse)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(raw), `{"kind":"no_response"}`; got != want {
		t.Fatalf("no-response JSON = %s, want exact semantic absence %s", got, want)
	}

	for _, test := range []struct {
		name   string
		mutate func(*contracts.PullRequestResponseBody)
	}{
		{name: "batch", mutate: func(body *contracts.PullRequestResponseBody) {
			body.BatchID = "fabricated-batch"
		}},
		{name: "summary", mutate: func(body *contracts.PullRequestResponseBody) {
			body.Summary = "This must never become a provider comment."
		}},
		{name: "reply", mutate: func(body *contracts.PullRequestResponseBody) {
			body.Replies = []contracts.PullRequestThreadResponse{{
				ThreadID: "thread-1",
				Body:     "This must never become a provider reply.",
			}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := noResponse
			test.mutate(&body)
			if err := body.Validate(nil); err == nil {
				t.Fatal("no-response admitted provider response fields")
			}
		})
	}

	unknown := contracts.PullRequestResponseBody{
		Kind: contracts.PullRequestResponseKind("provider_comment"),
	}
	if err := unknown.Validate(nil); err == nil {
		t.Fatal("response body admitted an open provider-effect kind")
	}
}

func TestPullRequestResponseKindMatchesObservationTrigger(t *testing.T) {
	review := validPullRequestResponseBody()
	review.Kind = contracts.PullRequestResponseReviewResponse
	noResponse := contracts.PullRequestResponseBody{
		Kind: contracts.PullRequestResponseNoResponse,
	}

	reviewObservation := pullRequestWithAuthorizedThreads("thread-1")
	if err := contracts.ValidatePullRequestResponseAgainst(
		review,
		reviewObservation,
	); err != nil {
		t.Fatalf("review response against review batch = %v", err)
	}
	if err := contracts.ValidatePullRequestResponseAgainst(
		noResponse,
		reviewObservation,
	); err == nil {
		t.Fatal("review batch accepted no-response instead of an authorized response")
	}

	for _, trigger := range []contracts.PullRequestTrigger{
		contracts.PullRequestConflictTrigger,
		contracts.PullRequestFreshnessTrigger,
		contracts.PullRequestCompletedTrigger,
		contracts.PullRequestAbandonedTrigger,
	} {
		t.Run(string(trigger), func(t *testing.T) {
			observation := pullRequestWithAuthorizedThreads()
			observation.ReviewBatches = nil
			observation.Trigger = trigger
			switch trigger {
			case contracts.PullRequestCompletedTrigger:
				observation.State = contracts.PullRequestCompleted
			case contracts.PullRequestAbandonedTrigger:
				observation.State = contracts.PullRequestAbandoned
			}
			if err := contracts.ValidatePullRequestResponseAgainst(
				noResponse,
				observation,
			); err != nil {
				t.Fatalf("no-response against %s = %v", trigger, err)
			}
			if err := contracts.ValidatePullRequestResponseAgainst(
				review,
				observation,
			); err == nil {
				t.Fatalf("%s observation admitted provider review authority", trigger)
			}
		})
	}
}

func TestPullRequestResponseLegacyMissingKindRemainsAReviewResponse(t *testing.T) {
	legacy := validPullRequestResponseBody()
	if legacy.Kind != "" {
		t.Fatalf("legacy response kind = %q, want omitted", legacy.Kind)
	}
	if err := legacy.Validate(nil); err != nil {
		t.Fatalf("legacy response Validate() = %v", err)
	}
	if err := contracts.ValidatePullRequestResponseAgainst(
		legacy,
		pullRequestWithAuthorizedThreads("thread-1"),
	); err != nil {
		t.Fatalf("legacy response against review batch = %v", err)
	}
}

func TestPullRequestResponseKindIsRevisionBoundAtReadTime(t *testing.T) {
	subjects := []contracts.Subject{{
		ID: "primary", Role: contracts.SubjectRolePrimary, Input: "pr", Type: "pull-request/v1",
		Digest: snapshot.Digest("sha256:" + strings.Repeat("a", 64)),
	}}
	validationContext := emptyValidationContext(t)

	t.Run("revision 3 retains omitted review kind", func(t *testing.T) {
		record, err := contracts.NewRecord(
			snapshot.TypeRef("pull-request-response/v1"),
			subjects,
			validPullRequestResponseBody(),
		)
		if err != nil {
			t.Fatalf("NewRecord(): %v", err)
		}
		revisionThree, found := contracts.SchemaDigestForRevision(
			snapshot.TypeRef("pull-request-response/v1"),
			3,
		)
		if !found {
			t.Fatal("revision 3 response schema was not found")
		}
		record.Schema = revisionThree
		if _, err := revalidateSealedFiles(
			t,
			"pull-request-response/v1",
			map[string][]byte{"record.json": marshalRecord(t, record)},
			validationContext,
		); err != nil {
			t.Fatalf("revision 3 omitted review kind was rejected: %v", err)
		}
	})

	t.Run("revision 3 rejects revision 4 no-response union arm", func(t *testing.T) {
		record, err := contracts.NewRecord(
			snapshot.TypeRef("pull-request-response/v1"),
			subjects,
			contracts.PullRequestResponseBody{Kind: contracts.PullRequestResponseNoResponse},
		)
		if err != nil {
			t.Fatalf("NewRecord(): %v", err)
		}
		revisionThree, found := contracts.SchemaDigestForRevision(
			snapshot.TypeRef("pull-request-response/v1"),
			3,
		)
		if !found {
			t.Fatal("revision 3 response schema was not found")
		}
		record.Schema = revisionThree
		if _, err := revalidateSealedFiles(
			t,
			"pull-request-response/v1",
			map[string][]byte{"record.json": marshalRecord(t, record)},
			validationContext,
		); err == nil {
			t.Fatal("revision 3 admitted the revision 4 no-response union arm")
		}
	})

	t.Run("revision 4 requires an explicit union arm", func(t *testing.T) {
		record, err := contracts.NewRecord(
			snapshot.TypeRef("pull-request-response/v1"),
			subjects,
			validPullRequestResponseBody(),
		)
		if err != nil {
			t.Fatalf("NewRecord(): %v", err)
		}
		if _, err := revalidateSealedFiles(
			t,
			"pull-request-response/v1",
			map[string][]byte{"record.json": marshalRecord(t, record)},
			validationContext,
		); err == nil {
			t.Fatal("revision 4 admitted an omitted response kind")
		}
	})
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
