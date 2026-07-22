package contracts_test

import (
	"math"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func TestMeasurementsContractValidatesFiniteNamedMetrics(t *testing.T) {
	valid := contracts.MeasurementsDocument{
		SchemaVersion:    "1.0.0",
		EvaluatorVersion: "review-quality/v3",
		Valid:            true,
		Metrics: []contracts.Measurement{{
			Name: "accuracy", Value: 0.9, Unit: "ratio", Direction: "higher",
		}},
	}
	if _, err := validateFiles(t, "measurements/v1", map[string][]byte{"measurements.json": marshalDocument(t, valid)}, emptyValidationContext(t)); err != nil {
		t.Fatalf("valid measurements error = %v", err)
	}

	for _, tc := range []struct {
		name  string
		setup func(*contracts.MeasurementsDocument)
		want  string
	}{
		{"wrong version", func(d *contracts.MeasurementsDocument) { d.SchemaVersion = "1.0" }, "1.0.0"},
		{"blank evaluator", func(d *contracts.MeasurementsDocument) { d.EvaluatorVersion = " " }, "evaluator_version"},
		{"valid with no metrics", func(d *contracts.MeasurementsDocument) { d.Metrics = nil }, "metrics"},
		{"invalid direction", func(d *contracts.MeasurementsDocument) { d.Metrics[0].Direction = "sideways" }, "direction"},
		{"duplicate name", func(d *contracts.MeasurementsDocument) { d.Metrics = append(d.Metrics, d.Metrics[0]) }, "duplicate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			document := valid
			document.Metrics = append([]contracts.Measurement(nil), valid.Metrics...)
			tc.setup(&document)
			if _, err := validateFiles(t, "measurements/v1", map[string][]byte{"measurements.json": marshalDocument(t, document)}, emptyValidationContext(t)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validation error = %v, want %q", err, tc.want)
			}
		})
	}
	document := valid
	document.Metrics = append([]contracts.Measurement(nil), valid.Metrics...)
	document.Metrics[0].Value = math.Inf(1)
	if err := document.Validate(); err == nil || !strings.Contains(err.Error(), "finite") {
		t.Fatalf("nonfinite metric validation error = %v, want finite error", err)
	}
}

func TestMeasurementsContractAllowsExplainedInvalidDocumentWithoutMetrics(t *testing.T) {
	document := contracts.MeasurementsDocument{
		SchemaVersion:    "1.0.0",
		EvaluatorVersion: "review-quality/v3",
		Valid:            false,
		Explanation:      "evaluator output was not parseable",
	}
	if _, err := validateFiles(t, "measurements/v1", map[string][]byte{"measurements.json": marshalDocument(t, document)}, emptyValidationContext(t)); err != nil {
		t.Fatalf("explained invalid measurements error = %v", err)
	}

	document.Explanation = " "
	if _, err := validateFiles(t, "measurements/v1", map[string][]byte{"measurements.json": marshalDocument(t, document)}, emptyValidationContext(t)); err == nil || !strings.Contains(err.Error(), "explanation") {
		t.Fatalf("unexplained invalid measurements error = %v, want explanation error", err)
	}
}
