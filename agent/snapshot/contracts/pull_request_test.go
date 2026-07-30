package contracts_test

import (
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func validPullRequestBody() contracts.PullRequestBody {
	return contracts.PullRequestBody{
		Provider:     "github",
		Repository:   "github.example/acme/widget",
		ExternalID:   "42",
		URL:          "https://github.example/acme/widget/pull/42",
		State:        contracts.PullRequestActive,
		Mergeability: contracts.PullRequestMergeable,
		SourceRef:    "refs/heads/agent/change",
		SourceSHA:    strings.Repeat("a", 40),
		TargetRef:    "refs/heads/main",
		TargetSHA:    strings.Repeat("b", 40),
		Iteration:    "iteration-1",
		Trigger:      contracts.PullRequestReviewBatchTrigger,
		ReviewBatches: []contracts.PullRequestReviewBatch{{
			ID: "batch-1", ReviewID: "review-1", CommitSHA: strings.Repeat("a", 40), Reviewer: "reviewer-1", Ready: true,
			ThreadIDs: []string{"thread-1"},
		}},
		Threads: []contracts.PullRequestThread{{
			ID: "thread-1", Iteration: "iteration-1", Comments: []contracts.PullRequestComment{{
				ID: "comment-1", Author: "reviewer-1", Body: "Please revise this.", CommitSHA: strings.Repeat("a", 40),
			}},
		}},
	}
}

func TestPullRequestBodyRejectsUnknownStatesAndDuplicateStableIDs(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*contracts.PullRequestBody)
		want  string
	}{
		{"unknown lifecycle", func(body *contracts.PullRequestBody) { body.State = "invented" }, "state"},
		{"unknown mergeability", func(body *contracts.PullRequestBody) { body.Mergeability = "probably" }, "mergeability"},
		{"duplicate batches", func(body *contracts.PullRequestBody) {
			body.ReviewBatches = append(body.ReviewBatches, body.ReviewBatches[0])
		}, "duplicate"},
		{"duplicate threads", func(body *contracts.PullRequestBody) { body.Threads = append(body.Threads, body.Threads[0]) }, "duplicate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := validPullRequestBody()
			tc.setup(&body)
			if err := body.Validate(nil); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestPullRequestMissingObservationRequiresExplicitExpectedSource(t *testing.T) {
	body := validPullRequestBody()
	body.State = contracts.PullRequestMissing
	body.Mergeability = contracts.PullRequestUnknown
	body.ExternalID = ""
	body.URL = ""
	body.SourceSHA = ""
	body.Trigger = contracts.PullRequestInitialPublishTrigger
	body.ExpectedSource = &contracts.PullRequestHeadExpectation{Exists: false}
	body.ReviewBatches = nil
	body.Threads = nil
	if err := body.Validate(nil); err != nil {
		t.Fatalf("missing observation Validate() = %v", err)
	}
	body.ExpectedSource = nil
	if err := body.Validate(nil); err == nil || !strings.Contains(err.Error(), "expected source") {
		t.Fatalf("missing observation without expectation error = %v", err)
	}
}
