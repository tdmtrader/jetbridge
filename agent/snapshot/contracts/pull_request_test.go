package contracts_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
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

func TestPullRequestBodyRejectsOversizedCollectionsAndText(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*contracts.PullRequestBody)
		want  string
	}{
		{"review batches", func(body *contracts.PullRequestBody) {
			body.ReviewBatches = nil
			for index := 0; index < 129; index++ {
				id := fmt.Sprintf("batch-%03d", index)
				body.ReviewBatches = append(body.ReviewBatches, contracts.PullRequestReviewBatch{ID: id, ReviewID: "review-" + id, CommitSHA: strings.Repeat("a", 40), Reviewer: "reviewer-1", Ready: true})
			}
		}, "review batches"},
		{"threads", func(body *contracts.PullRequestBody) {
			body.ReviewBatches = nil
			body.Threads = nil
			for index := 0; index < 513; index++ {
				body.Threads = append(body.Threads, contracts.PullRequestThread{ID: fmt.Sprintf("thread-%03d", index), Iteration: "iteration-1"})
			}
		}, "threads"},
		{"comments", func(body *contracts.PullRequestBody) {
			body.ReviewBatches = nil
			body.Threads[0].Comments = nil
			for index := 0; index < 257; index++ {
				body.Threads[0].Comments = append(body.Threads[0].Comments, contracts.PullRequestComment{ID: fmt.Sprintf("comment-%03d", index), Author: "reviewer-1", Body: "Please revise this.", CommitSHA: strings.Repeat("a", 40)})
			}
		}, "comments"},
		{"provider", func(body *contracts.PullRequestBody) { body.Provider = strings.Repeat("a", 65) }, "provider"},
		{"repository", func(body *contracts.PullRequestBody) { body.Repository = strings.Repeat("a", 513) }, "repository"},
		{"external id", func(body *contracts.PullRequestBody) { body.ExternalID = strings.Repeat("a", 257) }, "external id"},
		{"source ref", func(body *contracts.PullRequestBody) { body.SourceRef = strings.Repeat("a", 513) }, "source ref"},
		{"url", func(body *contracts.PullRequestBody) {
			body.URL = "https://example.invalid/" + strings.Repeat("a", 2025)
		}, "url"},
		{"anchor path", func(body *contracts.PullRequestBody) {
			body.Threads[0].Anchor = &contracts.PullRequestAnchor{Path: strings.Repeat("a", 1025), StartLine: 1, EndLine: 1}
		}, "anchor path"},
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

func TestPullRequestRev2RetainsPreBoundObservationWhileCurrentRejectsIt(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*contracts.PullRequestBody)
	}{
		{"provider bytes", func(body *contracts.PullRequestBody) {
			body.Provider = strings.Repeat("p", 65)
		}},
		{"repository bytes", func(body *contracts.PullRequestBody) {
			body.Repository = strings.Repeat("r", 513)
		}},
		{"url bytes", func(body *contracts.PullRequestBody) {
			body.URL = "https://example.invalid/" + strings.Repeat("u", 2025)
		}},
		{"source ref bytes", func(body *contracts.PullRequestBody) {
			body.SourceRef = strings.Repeat("s", 513)
		}},
		{"target ref bytes", func(body *contracts.PullRequestBody) {
			body.TargetRef = strings.Repeat("t", 513)
		}},
		{"external id bytes", func(body *contracts.PullRequestBody) {
			body.ExternalID = strings.Repeat("e", 257)
		}},
		{"iteration id bytes", func(body *contracts.PullRequestBody) {
			body.Iteration = strings.Repeat("i", 257)
		}},
		{"batch id bytes", func(body *contracts.PullRequestBody) {
			body.ReviewBatches[0].ID = strings.Repeat("b", 257)
		}},
		{"review id bytes", func(body *contracts.PullRequestBody) {
			body.ReviewBatches[0].ReviewID = strings.Repeat("r", 257)
		}},
		{"reviewer id bytes", func(body *contracts.PullRequestBody) {
			body.ReviewBatches[0].Reviewer = strings.Repeat("r", 257)
		}},
		{"thread and batch thread id bytes", func(body *contracts.PullRequestBody) {
			id := strings.Repeat("t", 257)
			body.Threads[0].ID = id
			body.ReviewBatches[0].ThreadIDs[0] = id
		}},
		{"thread iteration id bytes", func(body *contracts.PullRequestBody) {
			body.Threads[0].Iteration = strings.Repeat("i", 257)
		}},
		{"comment id bytes", func(body *contracts.PullRequestBody) {
			body.Threads[0].Comments[0].ID = strings.Repeat("c", 257)
		}},
		{"comment author id bytes", func(body *contracts.PullRequestBody) {
			body.Threads[0].Comments[0].Author = strings.Repeat("a", 257)
		}},
		{"anchor path bytes", func(body *contracts.PullRequestBody) {
			body.Threads[0].Anchor = &contracts.PullRequestAnchor{
				Path: strings.Repeat("p", 1025), StartLine: 1, EndLine: 1,
			}
		}},
		{"review batch count", func(body *contracts.PullRequestBody) {
			body.ReviewBatches = nil
			for index := 0; index < 129; index++ {
				id := fmt.Sprintf("batch-%03d", index)
				body.ReviewBatches = append(body.ReviewBatches, contracts.PullRequestReviewBatch{
					ID: id, ReviewID: "review-" + id, CommitSHA: strings.Repeat("a", 40), Reviewer: "reviewer-1", Ready: true,
				})
			}
		}},
		{"thread count", func(body *contracts.PullRequestBody) {
			body.ReviewBatches = nil
			body.Threads = nil
			for index := 0; index < 513; index++ {
				body.Threads = append(body.Threads, contracts.PullRequestThread{
					ID: fmt.Sprintf("thread-%03d", index), Iteration: "iteration-1",
				})
			}
		}},
		{"comment count", func(body *contracts.PullRequestBody) {
			body.ReviewBatches = nil
			body.Threads[0].Comments = nil
			for index := 0; index < 257; index++ {
				body.Threads[0].Comments = append(body.Threads[0].Comments, contracts.PullRequestComment{
					ID: fmt.Sprintf("comment-%03d", index), Author: "reviewer-1",
					Body: "Please revise this.", CommitSHA: strings.Repeat("a", 40),
				})
			}
		}},
		{"thread and batch thread id counts", func(body *contracts.PullRequestBody) {
			body.Threads = nil
			body.ReviewBatches[0].ThreadIDs = nil
			for index := 0; index < 513; index++ {
				id := fmt.Sprintf("thread-%03d", index)
				body.Threads = append(body.Threads, contracts.PullRequestThread{ID: id, Iteration: "iteration-1"})
				body.ReviewBatches[0].ThreadIDs = append(body.ReviewBatches[0].ThreadIDs, id)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := validPullRequestBody()
			tc.setup(&body)
			assertRevisionThreeBoundCompatibility(
				t,
				"pull-request/v1",
				nil,
				body,
				func() error { return body.Validate(nil) },
				emptyValidationContext(t),
			)
		})
	}
}

func TestNormalizeRawPullRequestBodyUsesCurrentBoundsWhenSchemaIsUnset(t *testing.T) {
	body := validPullRequestBody()
	body.ReviewBatches = nil
	for index := 0; index < 129; index++ {
		id := fmt.Sprintf("batch-%03d", index)
		body.ReviewBatches = append(body.ReviewBatches, contracts.PullRequestReviewBatch{
			ID: id, ReviewID: "review-" + id, CommitSHA: strings.Repeat("a", 40), Reviewer: "reviewer-1", Ready: true,
		})
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := contracts.NormalizeRawRecordBody(snapshot.TypeRef("pull-request/v1"), nil, raw); err == nil {
		t.Fatal("raw current-output normalization accepted an over-cap body with no schema")
	}
}
