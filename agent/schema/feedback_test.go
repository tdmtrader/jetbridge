package schema_test

import (
	"encoding/json"
	"testing"

	schema "github.com/concourse/concourse/agent/schema"
)

func validFeedbackRecord() schema.FeedbackRecord {
	return schema.FeedbackRecord{
		ReviewRef: schema.ReviewRef{
			Repo:     "https://github.com/org/repo.git",
			Commit:   "abc123",
			ReviewTS: "2026-02-09T17:00:00Z",
		},
		FindingID:       "ISS-001",
		FindingType:     "proven_issue",
		FindingSnapshot: json.RawMessage(`{"severity":"high","title":"nil deref"}`),
		Verdict:         schema.VerdictAccurate,
		Confidence:      0.95,
		Notes:           "Good catch",
		Conversation: []schema.ConversationMessage{
			{Role: "system", Content: "Finding ISS-001: nil deref"},
			{Role: "human", Content: "yes this is real"},
		},
		Reviewer: "tdm",
		Source:   schema.SourceInteractive,
	}
}

func TestFeedbackRecordJSONRoundTrip(t *testing.T) {
	t.Run("marshals and unmarshals correctly", func(t *testing.T) {
		rec := validFeedbackRecord()
		data, err := json.Marshal(rec)
		requireNoErr(t, err)

		var decoded schema.FeedbackRecord
		requireNoErr(t, json.Unmarshal(data, &decoded))

		requireEqual(t, decoded.FindingID, "ISS-001")
		requireEqual(t, decoded.Verdict, schema.VerdictAccurate)
		requireApprox(t, decoded.Confidence, 0.95, 0.01)
		requireEqual(t, decoded.ReviewRef.Repo, "https://github.com/org/repo.git")
		requireLen(t, decoded.Conversation, 2)
		requireEqual(t, decoded.Source, schema.SourceInteractive)
	})

	t.Run("includes all JSON fields", func(t *testing.T) {
		rec := validFeedbackRecord()
		data, err := json.Marshal(rec)
		requireNoErr(t, err)

		var raw map[string]interface{}
		requireNoErr(t, json.Unmarshal(data, &raw))

		for _, key := range []string{
			"review_ref", "finding_id", "finding_type", "finding_snapshot",
			"verdict", "confidence", "notes", "conversation", "reviewer", "source",
		} {
			requireHasKey(t, raw, key)
		}
	})
}

func TestFeedbackRecordValidate(t *testing.T) {
	t.Run("passes for a valid record", func(t *testing.T) {
		rec := validFeedbackRecord()
		requireNoErr(t, rec.Validate())
	})

	t.Run("requires finding_id", func(t *testing.T) {
		rec := validFeedbackRecord()
		rec.FindingID = ""
		requireErrContains(t, rec.Validate(), "finding_id")
	})

	t.Run("requires verdict", func(t *testing.T) {
		rec := validFeedbackRecord()
		rec.Verdict = ""
		requireErrContains(t, rec.Validate(), "verdict")
	})

	t.Run("requires finding_snapshot", func(t *testing.T) {
		rec := validFeedbackRecord()
		rec.FindingSnapshot = nil
		requireErrContains(t, rec.Validate(), "finding_snapshot")
	})

	t.Run("requires review_ref.repo", func(t *testing.T) {
		rec := validFeedbackRecord()
		rec.ReviewRef.Repo = ""
		requireErrContains(t, rec.Validate(), "repo")
	})

	t.Run("requires review_ref.commit", func(t *testing.T) {
		rec := validFeedbackRecord()
		rec.ReviewRef.Commit = ""
		requireErrContains(t, rec.Validate(), "commit")
	})

	t.Run("rejects invalid verdict", func(t *testing.T) {
		rec := validFeedbackRecord()
		rec.Verdict = "invalid_verdict"
		requireErrContains(t, rec.Validate(), "invalid verdict")
	})

	t.Run("rejects invalid source", func(t *testing.T) {
		rec := validFeedbackRecord()
		rec.Source = "bad_source"
		requireErrContains(t, rec.Validate(), "invalid source")
	})

	t.Run("accepts all valid verdicts", func(t *testing.T) {
		for _, v := range []schema.Verdict{
			schema.VerdictAccurate,
			schema.VerdictFalsePositive,
			schema.VerdictNoisy,
			schema.VerdictOverlyStrict,
			schema.VerdictPartiallyCorrect,
			schema.VerdictMissedContext,
		} {
			rec := validFeedbackRecord()
			rec.Verdict = v
			requireNoErr(t, rec.Validate(), "verdict %s should be valid", v)
		}
	})

	t.Run("accepts all valid sources", func(t *testing.T) {
		for _, s := range []schema.FeedbackSource{
			schema.SourceInteractive,
			schema.SourceInferredConversation,
			schema.SourceInferredOutcome,
		} {
			rec := validFeedbackRecord()
			rec.Source = s
			requireNoErr(t, rec.Validate(), "source %s should be valid", s)
		}
	})
}
