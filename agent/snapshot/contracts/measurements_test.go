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

// TestMeasurementsRecordRejectsAPartialConclusionWithNoMetrics pins the
// partial-with-no-metrics arm of conclusion-governs-the-metric-count. The witness
// discharges the measured arm; this is its readable sibling for partial.
// Explanation is set so the metric-count rule is unambiguously the only thing
// wrong: partial checks its metric count before it checks for an explanation.
func TestMeasurementsRecordRejectsAPartialConclusionWithNoMetrics(t *testing.T) {
	body := contracts.MeasurementsBody{
		Conclusion: "partial", Explanation: "one rubric dimension was unavailable", Metrics: nil,
	}
	validateMeasurementsBody(t, body, "partial conclusion requires at least one metric")
}

// TestMeasurementsRecordRequiresAnExplanationForPartialAndNotApplicable names the
// two conclusions that require prose. Each keeps its metric count valid so the
// missing explanation is the only defect.
func TestMeasurementsRecordRequiresAnExplanationForPartialAndNotApplicable(t *testing.T) {
	metric := contracts.Measurement{ID: "quality", Value: 8, Unit: "score", Direction: "higher-is-better"}
	for name, tc := range map[string]struct {
		body contracts.MeasurementsBody
		want string
	}{
		"partial": {
			body: contracts.MeasurementsBody{Conclusion: "partial", Metrics: []contracts.Measurement{metric}},
			want: "partial conclusion requires an explanation",
		},
		"not-applicable": {
			body: contracts.MeasurementsBody{Conclusion: "not-applicable"},
			want: "not-applicable conclusion requires an explanation",
		},
	} {
		t.Run(name, func(t *testing.T) {
			validateMeasurementsBody(t, tc.body, tc.want)
		})
	}
}

// TestMeasurementsMetricDirectionGovernsItsTarget pins the Measurement-level
// cross-rule between direction and target. It is a distinct rule from
// Score.Validate's identically shaped one — two types, two rules, and only
// selection/v1 exercises the score one — so a metric, which carries no score, is
// the only place the measurement rule is caught.
func TestMeasurementsMetricDirectionGovernsItsTarget(t *testing.T) {
	base := contracts.Measurement{ID: "latency", Value: 1.5, Unit: "milliseconds", Direction: "lower-is-better"}
	for name, tc := range map[string]struct {
		mutate func(*contracts.Measurement)
		want   string
	}{
		"higher-is-better carries a target": {
			mutate: func(m *contracts.Measurement) { m.Direction = "higher-is-better"; m.Target = floatPointer(3) },
			want:   "measurement target is valid only for target direction",
		},
		"target direction carries none": {
			mutate: func(m *contracts.Measurement) { m.Direction = "target"; m.Target = nil },
			want:   "target direction requires a finite target",
		},
	} {
		t.Run(name, func(t *testing.T) {
			metric := base
			tc.mutate(&metric)
			body := contracts.MeasurementsBody{Conclusion: "measured", Metrics: []contracts.Measurement{metric}}
			validateMeasurementsBody(t, body, tc.want)
		})
	}
}

// validateMeasurementsBody drives the SEAL gate over a measurements body carrying
// no subjects — measurements admits an empty subject set — and asserts the gate
// rejects with the named fragment.
func validateMeasurementsBody(t *testing.T, body contracts.MeasurementsBody, wantFragment string) {
	t.Helper()
	record, err := contracts.NewRecord(snapshot.TypeRef("measurements/v1"), nil, body)
	if err != nil {
		t.Fatalf("NewRecord(): %v", err)
	}
	_, err = validateFiles(t, "measurements/v1", map[string][]byte{
		"record.json": marshalRecord(t, record),
	}, emptyValidationContext(t))
	if err == nil || !strings.Contains(err.Error(), wantFragment) {
		t.Fatalf("measurements error = %v, want %q", err, wantFragment)
	}
}
