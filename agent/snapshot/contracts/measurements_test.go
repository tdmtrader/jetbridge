package contracts_test

import (
	"math"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func TestMeasurementsRecordValidatesStableFiniteMetricDefinitions(t *testing.T) {
	body := contracts.MeasurementsBody{
		Conclusion: "measured",
		Metrics: []contracts.Measurement{{
			ID: "accuracy", Value: 0.9, Unit: "ratio", Direction: "higher-is-better",
		}},
	}
	record, err := contracts.NewRecord(
		snapshot.TypeRef("measurements/v1"),
		nil,
		body,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validateFiles(t, "measurements/v1", map[string][]byte{
		"record.json": marshalRecord(t, record),
	}, emptyValidationContext(t)); err != nil {
		t.Fatalf("valid measurements error = %v", err)
	}

	for _, tc := range []struct {
		name  string
		setup func(*contracts.MeasurementsBody)
		want  string
	}{
		{"measured with no metrics", func(d *contracts.MeasurementsBody) { d.Metrics = nil }, "metric"},
		{"invalid direction", func(d *contracts.MeasurementsBody) { d.Metrics[0].Direction = "higher" }, "direction"},
		{"duplicate id", func(d *contracts.MeasurementsBody) { d.Metrics = append(d.Metrics, d.Metrics[0]) }, "duplicate"},
		{"unsorted ids", func(d *contracts.MeasurementsBody) {
			d.Metrics = append([]contracts.Measurement{{ID: "z", Value: 1, Unit: "ratio", Direction: "higher-is-better"}}, d.Metrics...)
		}, "sorted"},
		{"bounded minimum without maximum", func(d *contracts.MeasurementsBody) {
			minimum := 0.0
			d.Metrics[0].Minimum = &minimum
		}, "maximum"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := body
			candidate.Metrics = append([]contracts.Measurement(nil), body.Metrics...)
			tc.setup(&candidate)
			candidateRecord, err := contracts.NewRecord(snapshot.TypeRef("measurements/v1"), nil, candidate)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := validateFiles(t, "measurements/v1", map[string][]byte{
				"record.json": marshalRecord(t, candidateRecord),
			}, emptyValidationContext(t)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validation error = %v, want %q", err, tc.want)
			}
		})
	}
	body.Metrics[0].Value = math.Inf(1)
	if err := body.Validate(nil); err == nil || !strings.Contains(err.Error(), "finite") {
		t.Fatalf("nonfinite metric validation error = %v, want finite error", err)
	}
}

func TestMeasurementsRecordModelsPartialAndNotApplicableResults(t *testing.T) {
	partial := contracts.MeasurementsBody{
		Conclusion:  "partial",
		Explanation: "one rubric dimension was unavailable",
		Metrics: []contracts.Measurement{{
			ID: "quality", Value: 8, Unit: "score", Direction: "higher-is-better",
		}},
	}
	if err := partial.Validate(nil); err != nil {
		t.Fatalf("valid partial measurements: %v", err)
	}
	notApplicable := contracts.MeasurementsBody{
		Conclusion:  "not-applicable",
		Explanation: "fixture has no measurable output",
	}
	if err := notApplicable.Validate(nil); err != nil {
		t.Fatalf("valid not-applicable measurements: %v", err)
	}
	notApplicable.Metrics = partial.Metrics
	if err := notApplicable.Validate(nil); err == nil || !strings.Contains(err.Error(), "no metrics") {
		t.Fatalf("not-applicable metric error = %v", err)
	}
}
