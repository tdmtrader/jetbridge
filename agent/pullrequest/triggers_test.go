package pullrequest_test

import (
	"testing"
	"time"

	"github.com/concourse/concourse/agent/pullrequest"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func TestActionForTriggerPrecedenceAndAcknowledgedState(t *testing.T) {
	tests := []struct {
		name        string
		observation pullrequest.Observation
		policy      pullrequest.TriggerPolicy
		kind        pullrequest.ActionKind
		actionable  bool
	}{
		{"unchanged", baseObservation(), basePolicy(), "", false},
		{"github submitted review", reviewObservation("github-review-2", "batch-2", "review-2"), basePolicy(), pullrequest.ActionReviewBatch, true},
		{"opaque provider cursor with a ready vote", reviewObservation(`forge:{"vote":-5,"reviewer":"alice"}`, "batch-2", "vote-alice-2"), basePolicy(), pullrequest.ActionReviewBatch, true},
		{"unchanged conflict", conflictedObservation("cursor-1"), basePolicy(), "", false},
		{"changed conflict signature", conflictedObservation("cursor-2"), basePolicy(), pullrequest.ActionConflict, true},
		{"target movement before freshness", observationWithTarget(sha('c')), policyWithAge(5*time.Hour + 59*time.Minute), "", false},
		{"target movement at freshness", observationWithTarget(sha('c')), policyWithAge(6 * time.Hour), pullrequest.ActionFreshness, true},
		{"target movement after freshness", observationWithTarget(sha('c')), policyWithAge(7 * time.Hour), pullrequest.ActionFreshness, true},
		{"equivalent active action", reviewObservation("github-review-2", "batch-2", "review-2"), policyWithActiveReview(), "", false},
		{"completed", terminalObservation(contracts.PullRequestCompleted, "completed-2"), basePolicy(), pullrequest.ActionCompleted, true},
		{"abandoned", terminalObservation(contracts.PullRequestAbandoned, "abandoned-2"), basePolicy(), pullrequest.ActionAbandoned, true},
		{"first terminal observation with empty acknowledged cursor", terminalObservation(contracts.PullRequestCompleted, "completed-1"), policyWithLastCursor(""), pullrequest.ActionCompleted, true},
		{"terminal wins over review", terminalObservation(contracts.PullRequestCompleted, "completed-2"), policyWithReadyReview(), pullrequest.ActionCompleted, true},
		{"review wins over conflict and freshness", conflictedReviewObservation("cursor-2"), policyWithAge(6 * time.Hour), pullrequest.ActionReviewBatch, true},
		{"missing is never actionable", missingObservation(), basePolicy(), "", false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			action, actionable, err := pullrequest.ActionFor(test.observation, test.policy)
			if err != nil {
				t.Fatalf("action for: %v", err)
			}
			if actionable != test.actionable {
				t.Fatalf("actionable = %v, want %v", actionable, test.actionable)
			}
			if actionable && action.Kind != test.kind {
				t.Fatalf("kind = %q, want %q", action.Kind, test.kind)
			}
		})
	}
}

func TestActionForDigestIsDeterministicOpaqueAndDetached(t *testing.T) {
	observation := reviewObservation(`forge:{"opaque":"cursor"}`, "batch-2", "review-2")
	policy := basePolicy()
	first, actionable, err := pullrequest.ActionFor(observation, policy)
	if err != nil || !actionable {
		t.Fatalf("first action = %#v, %v, %v", first, actionable, err)
	}
	second, actionable, err := pullrequest.ActionFor(observation, policy)
	if err != nil || !actionable || first.Digest != second.Digest {
		t.Fatalf("digest is not deterministic: %q %q (%v, %v)", first.Digest, second.Digest, actionable, err)
	}

	observation.SourceSHA = sha('d')
	observation.ReviewBatches[0].ThreadIDs[0] = "mutated"
	if first.Observation.SourceSHA != sha('a') || first.Observation.ReviewBatches[0].ThreadIDs[0] != "thread-1" {
		t.Fatal("action retained mutable observation storage")
	}
	changed, actionable, err := pullrequest.ActionFor(observation, policy)
	if err == nil || actionable || changed.Digest != "" {
		t.Fatal("invalid mutated observation must be rejected rather than normalized")
	}

	changedObservation := reviewObservation(`forge:{"opaque":"different"}`, "batch-3", "review-3")
	changed, actionable, err = pullrequest.ActionFor(changedObservation, policy)
	if err != nil || !actionable || changed.Digest == first.Digest {
		t.Fatalf("cursor mutation did not produce an exact distinct digest: %#v %v %v", changed, actionable, err)
	}

	headChanged := reviewObservation(`forge:{"opaque":"cursor"}`, "batch-2", "review-2")
	headChanged.SourceSHA = sha('d')
	headChanged.ReviewBatches[0].CommitSHA = sha('d')
	headChanged.Threads[0].Comments[0].CommitSHA = sha('d')
	changed, actionable, err = pullrequest.ActionFor(headChanged, policy)
	if err != nil || !actionable || changed.Digest == first.Digest {
		t.Fatalf("source-head mutation did not produce an exact distinct digest: %#v %v %v", changed, actionable, err)
	}
}

func TestActionForFreshnessDigestUsesAcknowledgedDeadlineBucket(t *testing.T) {
	observation := observationWithTarget(sha('c'))
	policy := basePolicy()
	policy.Now = policy.LastReconciledAt.Add(6 * time.Hour)

	first, actionable, err := pullrequest.ActionFor(observation, policy)
	if err != nil || !actionable || first.Kind != pullrequest.ActionFreshness {
		t.Fatalf("hour-six freshness = %#v, %v, %v", first, actionable, err)
	}
	policy.Now = policy.LastReconciledAt.Add(12 * time.Hour)
	later, actionable, err := pullrequest.ActionFor(observation, policy)
	if err != nil || !actionable || later.Digest != first.Digest {
		t.Fatalf("hour-twelve freshness digest changed: %q != %q (%v, %v)", later.Digest, first.Digest, actionable, err)
	}
	policy.ActiveActionDigest = first.Digest
	_, actionable, err = pullrequest.ActionFor(observation, policy)
	if err != nil || actionable {
		t.Fatalf("equivalent active freshness action was not suppressed: %v, %v", actionable, err)
	}
}

func TestActionForRejectsInvalidPolicyAndObservation(t *testing.T) {
	policy := basePolicy()
	policy.FreshnessInterval = 0
	if _, _, err := pullrequest.ActionFor(baseObservation(), policy); err == nil {
		t.Fatal("expected invalid policy error")
	}
	observation := baseObservation()
	observation.Cursor = pullrequest.Cursor(string([]byte{0xff}))
	if _, _, err := pullrequest.ActionFor(observation, basePolicy()); err == nil {
		t.Fatal("expected invalid opaque cursor error")
	}
}

func baseObservation() pullrequest.Observation {
	return pullrequest.Observation{
		Locator: pullrequest.Locator{Provider: pullrequest.ProviderGitHub, Repository: "acme/widget", ExternalID: "42"},
		Cursor:  "cursor-1", URL: "https://github.example/acme/widget/pull/42",
		State: contracts.PullRequestActive, Mergeability: contracts.PullRequestMergeable,
		SourceRef: "refs/heads/agent/change", SourceSHA: sha('a'), TargetRef: "refs/heads/main", TargetSHA: sha('b'), Iteration: "1",
	}
}

func basePolicy() pullrequest.TriggerPolicy {
	return pullrequest.TriggerPolicy{Now: time.Date(2026, time.July, 29, 12, 0, 0, 0, time.UTC), PollInterval: 5 * time.Minute, FreshnessInterval: 6 * time.Hour, LastCursor: "cursor-1", LastTargetSHA: sha('b'), LastReconciledAt: time.Date(2026, time.July, 29, 6, 0, 0, 0, time.UTC)}
}

func reviewObservation(cursor pullrequest.Cursor, batchID, reviewID string) pullrequest.Observation {
	observation := baseObservation()
	observation.Cursor = cursor
	observation.ReviewBatches = []contracts.PullRequestReviewBatch{{ID: batchID, ReviewID: reviewID, CommitSHA: sha('a'), Reviewer: "reviewer", Ready: true, ThreadIDs: []string{"thread-1"}}}
	observation.Threads = []contracts.PullRequestThread{{ID: "thread-1", Iteration: "1", Comments: []contracts.PullRequestComment{{ID: "comment-1", Author: "author", Body: "body", CommitSHA: sha('a')}}}}
	return observation
}
func observationWithTarget(target string) pullrequest.Observation {
	observation := baseObservation()
	observation.ReviewBatches = nil
	observation.Threads = nil
	observation.TargetSHA = target
	return observation
}
func conflictedObservation(cursor pullrequest.Cursor) pullrequest.Observation {
	observation := baseObservation()
	observation.Cursor = cursor
	observation.Mergeability = contracts.PullRequestConflicted
	observation.ReviewBatches, observation.Threads = nil, nil
	return observation
}
func conflictedReviewObservation(cursor pullrequest.Cursor) pullrequest.Observation {
	observation := reviewObservation(cursor, "batch-2", "review-2")
	observation.Mergeability = contracts.PullRequestConflicted
	return observation
}
func terminalObservation(state contracts.PullRequestState, cursor pullrequest.Cursor) pullrequest.Observation {
	observation := baseObservation()
	observation.Cursor = cursor
	observation.State = state
	observation.ReviewBatches = nil
	observation.Threads = nil
	return observation
}
func missingObservation() pullrequest.Observation {
	observation := baseObservation()
	observation.State = contracts.PullRequestMissing
	observation.Mergeability = contracts.PullRequestUnknown
	observation.ExternalID, observation.URL, observation.SourceSHA = "", "", ""
	observation.ExpectedSource = &contracts.PullRequestHeadExpectation{Exists: true, SHA: sha('a')}
	observation.ReviewBatches, observation.Threads = nil, nil
	return observation
}
func policyWithAge(age time.Duration) pullrequest.TriggerPolicy {
	policy := basePolicy()
	policy.LastReconciledAt = policy.Now.Add(-age)
	return policy
}
func policyWithActiveReview() pullrequest.TriggerPolicy {
	policy := basePolicy()
	action, actionable, err := pullrequest.ActionFor(reviewObservation("github-review-2", "batch-2", "review-2"), policy)
	if err != nil || !actionable {
		panic("invalid review fixture")
	}
	policy.ActiveActionDigest = action.Digest
	return policy
}
func policyWithLastCursor(cursor pullrequest.Cursor) pullrequest.TriggerPolicy {
	policy := basePolicy()
	policy.LastCursor = cursor
	return policy
}
func policyWithReadyReview() pullrequest.TriggerPolicy { return policyWithAge(6 * time.Hour) }
func sha(character byte) string                        { return string(makeRepeated(character, 40)) }
func makeRepeated(character byte, count int) []byte {
	result := make([]byte, count)
	for index := range result {
		result[index] = character
	}
	return result
}
