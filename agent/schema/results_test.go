package schema_test

import (
	"encoding/json"
	"testing"

	"github.com/concourse/concourse/agent/schema"
)

func validResults() schema.Results {
	return schema.Results{
		SchemaVersion: "1.0",
		Status:        schema.StatusPass,
		Confidence:    0.92,
		Summary:       "All checks passed",
		Artifacts:     []schema.Artifact{},
	}
}

func TestResultsValidate(t *testing.T) {
	t.Run("accepts a valid Results with all required fields", func(t *testing.T) {
		r := validResults()
		requireNoErr(t, r.Validate())
	})

	t.Run("rejects missing schema_version", func(t *testing.T) {
		r := validResults()
		r.SchemaVersion = ""
		requireErrContains(t, r.Validate(), "schema_version")
	})

	t.Run("rejects missing status", func(t *testing.T) {
		r := validResults()
		r.Status = ""
		requireErrContains(t, r.Validate(), "status")
	})

	t.Run("rejects invalid status value", func(t *testing.T) {
		r := validResults()
		r.Status = "unknown"
		requireErrContains(t, r.Validate(), "status")
	})

	t.Run("accepts all valid status values", func(t *testing.T) {
		for _, s := range []schema.Status{
			schema.StatusPass,
			schema.StatusFail,
			schema.StatusError,
			schema.StatusAbstain,
		} {
			r := validResults()
			r.Status = s
			requireNoErr(t, r.Validate(), "expected status %q to be valid", s)
		}
	})

	// PARK-V2 is gone. The runner has no park exit — a v3 human wait is a row
	// in agent_workflow_waits on the durable run — so 'parked' is not a status
	// a results.json may claim, and the DB CHECK no longer accepts it either.
	t.Run("rejects the retired parked wire status", func(t *testing.T) {
		r := validResults()
		r.Status = schema.Status("parked")
		requireErrContains(t, r.Validate(), "status")
	})

	t.Run("rejects missing summary", func(t *testing.T) {
		r := validResults()
		r.Summary = ""
		requireErrContains(t, r.Validate(), "summary")
	})

	t.Run("rejects confidence below 0.0", func(t *testing.T) {
		r := validResults()
		r.Confidence = -0.1
		requireErrContains(t, r.Validate(), "confidence")
	})

	t.Run("rejects confidence above 1.0", func(t *testing.T) {
		r := validResults()
		r.Confidence = 1.1
		requireErrContains(t, r.Validate(), "confidence")
	})

	t.Run("accepts confidence at 0.0", func(t *testing.T) {
		r := validResults()
		r.Confidence = 0.0
		requireNoErr(t, r.Validate())
	})

	t.Run("accepts confidence at 1.0", func(t *testing.T) {
		r := validResults()
		r.Confidence = 1.0
		requireNoErr(t, r.Validate())
	})

	t.Run("rejects nil artifacts (must be present, even if empty)", func(t *testing.T) {
		r := validResults()
		r.Artifacts = nil
		requireErrContains(t, r.Validate(), "artifacts")
	})

	t.Run("accepts an empty artifacts array", func(t *testing.T) {
		r := validResults()
		r.Artifacts = []schema.Artifact{}
		requireNoErr(t, r.Validate())
	})

	t.Run("rejects an artifact with missing name", func(t *testing.T) {
		r := validResults()
		r.Artifacts = []schema.Artifact{{
			Name:      "",
			Path:      "out/review.json",
			MediaType: "application/json",
		}}
		requireErrContains(t, r.Validate(), "name")
	})

	t.Run("rejects an artifact with missing path", func(t *testing.T) {
		r := validResults()
		r.Artifacts = []schema.Artifact{{
			Name:      "review",
			Path:      "",
			MediaType: "application/json",
		}}
		requireErrContains(t, r.Validate(), "path")
	})

	t.Run("rejects an artifact with missing media_type", func(t *testing.T) {
		r := validResults()
		r.Artifacts = []schema.Artifact{{
			Name:      "review",
			Path:      "out/review.json",
			MediaType: "",
		}}
		requireErrContains(t, r.Validate(), "media_type")
	})

	t.Run("accepts valid artifacts", func(t *testing.T) {
		r := validResults()
		r.Artifacts = []schema.Artifact{
			{
				Name:      "review-comments",
				Path:      "artifacts/comments.json",
				MediaType: "application/json",
			},
			{
				Name:      "summary",
				Path:      "artifacts/summary.md",
				MediaType: "text/markdown",
			},
		}
		requireNoErr(t, r.Validate())
	})

	t.Run("allows optional metadata", func(t *testing.T) {
		r := validResults()
		r.Metadata = map[string]interface{}{
			"files_reviewed": 42,
			"issues_found":   3,
		}
		requireNoErr(t, r.Validate())
	})
}

func TestResultsJSONRoundTrip(t *testing.T) {
	t.Run("marshals and unmarshals a full Results", func(t *testing.T) {
		original := schema.Results{
			SchemaVersion: "1.0",
			Status:        schema.StatusPass,
			Confidence:    0.85,
			Summary:       "Review complete",
			Artifacts: []schema.Artifact{
				{
					Name:      "review-comments",
					Path:      "artifacts/comments.json",
					MediaType: "application/json",
					Metadata: map[string]interface{}{
						"count": float64(5),
					},
				},
			},
			Metadata: map[string]interface{}{
				"files_reviewed": float64(10),
			},
		}

		data, err := json.Marshal(original)
		requireNoErr(t, err)

		var decoded schema.Results
		requireNoErr(t, json.Unmarshal(data, &decoded))

		requireEqual(t, decoded.SchemaVersion, original.SchemaVersion)
		requireEqual(t, decoded.Status, original.Status)
		requireEqual(t, decoded.Confidence, original.Confidence)
		requireEqual(t, decoded.Summary, original.Summary)
		requireLen(t, decoded.Artifacts, 1)
		requireEqual(t, decoded.Artifacts[0].Name, "review-comments")
		requireEqual(t, decoded.Artifacts[0].Path, "artifacts/comments.json")
		requireEqual(t, decoded.Artifacts[0].MediaType, "application/json")
		requireKeyVal(t, decoded.Metadata, "files_reviewed", float64(10))
	})

	t.Run("uses correct JSON field names", func(t *testing.T) {
		r := schema.Results{
			SchemaVersion: "1.0",
			Status:        schema.StatusFail,
			Confidence:    0.3,
			Summary:       "Issues found",
			Artifacts: []schema.Artifact{
				{
					Name:      "report",
					Path:      "out/report.md",
					MediaType: "text/markdown",
				},
			},
		}

		data, err := json.Marshal(r)
		requireNoErr(t, err)

		var raw map[string]interface{}
		requireNoErr(t, json.Unmarshal(data, &raw))

		requireHasKey(t, raw, "schema_version")
		requireHasKey(t, raw, "status")
		requireHasKey(t, raw, "confidence")
		requireHasKey(t, raw, "summary")
		requireHasKey(t, raw, "artifacts")
	})

	t.Run("serializes nil artifacts as empty array", func(t *testing.T) {
		r := schema.Results{
			SchemaVersion: "1.0",
			Status:        schema.StatusPass,
			Confidence:    1.0,
			Summary:       "ok",
		}

		data, err := json.Marshal(r)
		requireNoErr(t, err)

		var raw map[string]interface{}
		requireNoErr(t, json.Unmarshal(data, &raw))

		artifacts, ok := raw["artifacts"].([]interface{})
		requireTrue(t, ok, "artifacts should be an array")
		requireLen(t, artifacts, 0)
	})

	t.Run("omits metadata when nil", func(t *testing.T) {
		r := schema.Results{
			SchemaVersion: "1.0",
			Status:        schema.StatusPass,
			Confidence:    1.0,
			Summary:       "ok",
			Artifacts:     []schema.Artifact{},
		}

		data, err := json.Marshal(r)
		requireNoErr(t, err)

		var raw map[string]interface{}
		requireNoErr(t, json.Unmarshal(data, &raw))

		requireNoKey(t, raw, "metadata")
	})

	t.Run("includes metadata when present", func(t *testing.T) {
		r := schema.Results{
			SchemaVersion: "1.0",
			Status:        schema.StatusPass,
			Confidence:    1.0,
			Summary:       "ok",
			Artifacts:     []schema.Artifact{},
			Metadata: map[string]interface{}{
				"custom_key": "custom_value",
			},
		}

		data, err := json.Marshal(r)
		requireNoErr(t, err)

		var raw map[string]interface{}
		requireNoErr(t, json.Unmarshal(data, &raw))

		requireHasKey(t, raw, "metadata")
	})

	t.Run("round-trips artifact metadata", func(t *testing.T) {
		original := schema.Artifact{
			Name:      "test",
			Path:      "out/test.json",
			MediaType: "application/json",
			Metadata: map[string]interface{}{
				"size_bytes": float64(1024),
			},
		}

		data, err := json.Marshal(original)
		requireNoErr(t, err)

		var decoded schema.Artifact
		requireNoErr(t, json.Unmarshal(data, &decoded))

		requireEqual(t, decoded.Name, original.Name)
		requireEqual(t, decoded.Path, original.Path)
		requireEqual(t, decoded.MediaType, original.MediaType)
		requireKeyVal(t, decoded.Metadata, "size_bytes", float64(1024))
	})
}
