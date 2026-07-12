package schema_test

import (
	"encoding/json"
	"testing"

	schema "github.com/concourse/concourse/agent/schema"
)

func validQA() schema.QAOutput {
	return schema.QAOutput{
		SchemaVersion: "1.0.0",
		Results: []schema.RequirementResult{
			{
				ID:             "R1",
				Text:           "System authenticates users",
				Status:         schema.CoverageCovered,
				CoveragePoints: 1.0,
			},
			{
				ID:             "R2",
				Text:           "Sessions expire",
				Status:         schema.CoveragePartial,
				CoveragePoints: 0.5,
			},
		},
		Score: schema.QAScore{
			Value:     7.5,
			Max:       10.0,
			Pass:      true,
			Threshold: 7.0,
		},
		Gaps: []schema.Gap{
			{RequirementID: "R2", Severity: "medium", Description: "Partial coverage"},
		},
		Metadata: schema.QAMetadata{
			SpecFile:            "spec.md",
			RequirementsTotal:   2,
			RequirementsCovered: 1,
		},
	}
}

func TestQAOutputJSONRoundTrip(t *testing.T) {
	// marshals and unmarshals correctly
	qa := validQA()
	data, err := json.Marshal(qa)
	requireNoErr(t, err)

	var decoded schema.QAOutput
	requireNoErr(t, json.Unmarshal(data, &decoded))
	requireEqual(t, decoded.SchemaVersion, "1.0.0")
	requireLen(t, decoded.Results, 2)
	requireApprox(t, decoded.Score.Value, 7.5, 0.01)
	requireLen(t, decoded.Gaps, 1)
}

func TestQAOutputValidate(t *testing.T) {
	t.Run("passes for valid output", func(t *testing.T) {
		qa := validQA()
		requireNoErr(t, qa.Validate())
	})

	t.Run("defaults schema_version to 1.0.0", func(t *testing.T) {
		qa := validQA()
		qa.SchemaVersion = ""
		requireNoErr(t, qa.Validate())
		requireEqual(t, qa.SchemaVersion, "1.0.0")
	})

	t.Run("requires result id", func(t *testing.T) {
		qa := validQA()
		qa.Results[0].ID = ""
		requireErrContains(t, qa.Validate(), "id")
	})

	t.Run("requires result text", func(t *testing.T) {
		qa := validQA()
		qa.Results[0].Text = ""
		requireErrContains(t, qa.Validate(), "text")
	})

	t.Run("rejects invalid coverage status", func(t *testing.T) {
		qa := validQA()
		qa.Results[0].Status = "unknown"
		requireErrContains(t, qa.Validate(), "invalid status")
	})

	t.Run("requires score.max > 0", func(t *testing.T) {
		qa := validQA()
		qa.Score.Max = 0
		requireErrContains(t, qa.Validate(), "score.max")
	})

	t.Run("accepts all valid coverage statuses", func(t *testing.T) {
		for _, s := range []schema.CoverageStatus{
			schema.CoverageCovered,
			schema.CoveragePartial,
			schema.CoverageUncoveredImplemented,
			schema.CoverageUncoveredBroken,
			schema.CoverageFailing,
		} {
			qa := validQA()
			qa.Results[0].Status = s
			requireNoErr(t, qa.Validate())
		}
	})
}

func TestCoveragePoints(t *testing.T) {
	requireEqual(t, schema.CoverageCovered.CoveragePoints(), 1.0)
	requireEqual(t, schema.CoveragePartial.CoveragePoints(), 0.5)
	requireEqual(t, schema.CoverageUncoveredImplemented.CoveragePoints(), 0.75)
	requireEqual(t, schema.CoverageUncoveredBroken.CoveragePoints(), 0.0)
	requireEqual(t, schema.CoverageFailing.CoveragePoints(), 0.0)
}
