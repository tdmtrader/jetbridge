package pullrequest_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/pullrequest"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func TestObservationValidateAcceptsOpaqueCursorAndCopiesOwnedData(t *testing.T) {
	observation := reviewObservation(`{not-json-and-intentionally-opaque`, "batch-2", "review-2")

	if err := observation.Validate(); err != nil {
		t.Fatalf("validate opaque cursor: %v", err)
	}

	clone := observation.Clone()
	observation.ExpectedSource = nil
	observation.ReviewBatches[0].ThreadIDs[0] = "mutated"
	observation.Threads[0].Comments = append(observation.Threads[0].Comments, contracts.PullRequestComment{ID: "comment-2", Author: "author", Body: "body", CommitSHA: sha('a')})
	if clone.ReviewBatches[0].ThreadIDs[0] != "thread-1" || len(clone.Threads[0].Comments) != 1 {
		t.Fatal("clone retained mutable observation storage")
	}
}

func TestObservationValidateRejectsInvalidProviderContextAndOrdering(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*pullrequest.Observation)
	}{
		{"unknown provider", func(observation *pullrequest.Observation) { observation.Provider = "gitlab" }},
		{"active needs external id", func(observation *pullrequest.Observation) { observation.ExternalID = "" }},
		{"missing permits omitted external id", func(observation *pullrequest.Observation) {
			observation.State = contracts.PullRequestMissing
			observation.Mergeability = contracts.PullRequestUnknown
			observation.ExternalID, observation.URL, observation.SourceSHA = "", "", ""
			observation.ExpectedSource = &contracts.PullRequestHeadExpectation{Exists: true, SHA: sha('a')}
			observation.Cursor = ""
			observation.ReviewBatches, observation.Threads = nil, nil
		}},
		{"missing rejects external id", func(observation *pullrequest.Observation) {
			observation.State = contracts.PullRequestMissing
			observation.Mergeability = contracts.PullRequestUnknown
			observation.URL, observation.SourceSHA = "", ""
			observation.ExpectedSource = &contracts.PullRequestHeadExpectation{Exists: false}
			observation.ReviewBatches, observation.Threads = nil, nil
		}},
		{"oversized cursor", func(observation *pullrequest.Observation) {
			observation.Cursor = pullrequest.Cursor(strings.Repeat("x", 4097))
		}},
		{"active empty cursor", func(observation *pullrequest.Observation) { observation.Cursor = "" }},
		{"completed empty cursor", func(observation *pullrequest.Observation) {
			*observation = terminalObservation(contracts.PullRequestCompleted, "")
		}},
		{"abandoned empty cursor", func(observation *pullrequest.Observation) {
			*observation = terminalObservation(contracts.PullRequestAbandoned, "")
		}},
		{"unsorted batches", func(observation *pullrequest.Observation) {
			*observation = reviewObservation("cursor-2", "batch-1", "review-1")
			observation.ReviewBatches = append(observation.ReviewBatches, contracts.PullRequestReviewBatch{ID: "batch-0", ReviewID: "review-0", CommitSHA: sha('a'), Reviewer: "reviewer", Ready: true, ThreadIDs: []string{"thread-1"}})
		}},
		{"duplicate review identity", func(observation *pullrequest.Observation) {
			*observation = reviewObservation("cursor-2", "batch-1", "review-1")
			observation.ReviewBatches = append(observation.ReviewBatches, contracts.PullRequestReviewBatch{ID: "batch-2", ReviewID: "review-1", CommitSHA: sha('a'), Reviewer: "reviewer", Ready: true, ThreadIDs: []string{"thread-1"}})
		}},
		{"too many review batches", func(observation *pullrequest.Observation) {
			observation.ReviewBatches = make([]contracts.PullRequestReviewBatch, 129)
			for index := range observation.ReviewBatches {
				id := fmt.Sprintf("batch-%03d", index)
				observation.ReviewBatches[index] = contracts.PullRequestReviewBatch{ID: id, ReviewID: "review-" + id, CommitSHA: sha('a'), Reviewer: "reviewer", Ready: true}
			}
		}},
		{"too many threads", func(observation *pullrequest.Observation) {
			observation.Threads = make([]contracts.PullRequestThread, 513)
			for index := range observation.Threads {
				observation.Threads[index] = contracts.PullRequestThread{ID: fmt.Sprintf("thread-%03d", index), Iteration: "1"}
			}
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observation := baseObservation()
			test.mutate(&observation)
			err := observation.Validate()
			if test.name == "missing permits omitted external id" {
				if err != nil {
					t.Fatalf("validate missing pre-create observation: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
