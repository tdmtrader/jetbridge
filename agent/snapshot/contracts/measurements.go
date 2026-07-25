package contracts

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/concourse/concourse/agent/snapshot"
)

type MeasurementsBody struct {
	Conclusion  string        `json:"conclusion"`
	Explanation string        `json:"explanation,omitempty"`
	Metrics     []Measurement `json:"metrics"`
}

// MeasurementsDocument remains the in-process name used by experiment
// persistence. Its wire representation is always the body of record.json.
type MeasurementsDocument = MeasurementsBody

type Measurement struct {
	ID        string   `json:"id"`
	Value     float64  `json:"value"`
	Unit      string   `json:"unit"`
	Direction string   `json:"direction"`
	Minimum   *float64 `json:"minimum,omitempty"`
	Maximum   *float64 `json:"maximum,omitempty"`
	Target    *float64 `json:"target,omitempty"`
	Evidence  []Anchor `json:"evidence"`
}

func (body MeasurementsBody) Validate(subjects []Subject) error {
	subjectIDs := make(map[string]struct{}, len(subjects))
	for _, subject := range subjects {
		subjectIDs[subject.ID] = struct{}{}
	}
	metricIDs := make([]string, len(body.Metrics))
	for index, metric := range body.Metrics {
		metricIDs[index] = metric.ID
		if err := metric.Validate(subjectIDs); err != nil {
			return fmt.Errorf("metrics[%d]: %w", index, err)
		}
	}
	if err := ValidateEntityIDs("metrics", metricIDs); err != nil {
		return err
	}
	switch body.Conclusion {
	case "measured":
		if len(body.Metrics) == 0 {
			return fmt.Errorf("measured conclusion requires at least one metric")
		}
	case "partial":
		if len(body.Metrics) == 0 {
			return fmt.Errorf("partial conclusion requires at least one metric")
		}
		if strings.TrimSpace(body.Explanation) == "" {
			return fmt.Errorf("partial conclusion requires an explanation")
		}
	case "not-applicable":
		if len(body.Metrics) != 0 {
			return fmt.Errorf("not-applicable conclusion requires no metrics")
		}
		if strings.TrimSpace(body.Explanation) == "" {
			return fmt.Errorf("not-applicable conclusion requires an explanation")
		}
	default:
		return fmt.Errorf("conclusion must be one of measured, partial, not-applicable")
	}
	return nil
}

// ValidateDetached validates a body after it has been separated from its
// already-validated record envelope for experiment persistence. Evidence
// anchors remain part of the value but cannot be re-resolved without subjects.
func (body MeasurementsBody) ValidateDetached() error {
	detached := body
	detached.Metrics = append([]Measurement(nil), body.Metrics...)
	for index := range detached.Metrics {
		detached.Metrics[index].Evidence = nil
	}
	return detached.Validate(nil)
}

func (measurement Measurement) Validate(subjects map[string]struct{}) error {
	if err := ValidateIdentifier("measurement id", measurement.ID); err != nil {
		return err
	}
	if err := ValidateIdentifier("measurement unit", measurement.Unit); err != nil {
		return err
	}
	if !finiteNumber(measurement.Value) {
		return fmt.Errorf("measurement value must be finite")
	}
	switch measurement.Direction {
	case "higher-is-better", "lower-is-better":
		if measurement.Target != nil {
			return fmt.Errorf("measurement target is valid only for target direction")
		}
	case "target":
		if measurement.Target == nil || !finiteNumber(*measurement.Target) {
			return fmt.Errorf("target direction requires a finite target")
		}
	default:
		return fmt.Errorf("measurement direction must be one of higher-is-better, lower-is-better, target")
	}
	if (measurement.Minimum == nil) != (measurement.Maximum == nil) {
		return fmt.Errorf("measurement minimum and maximum must be declared together")
	}
	if measurement.Minimum != nil {
		if !finiteNumber(*measurement.Minimum) || !finiteNumber(*measurement.Maximum) || *measurement.Minimum > *measurement.Maximum {
			return fmt.Errorf("measurement bounds must be finite and minimum must not exceed maximum")
		}
		if measurement.Value < *measurement.Minimum || measurement.Value > *measurement.Maximum {
			return fmt.Errorf("measurement value must be within its declared bounds")
		}
		if measurement.Target != nil && (*measurement.Target < *measurement.Minimum || *measurement.Target > *measurement.Maximum) {
			return fmt.Errorf("measurement target must be within its declared bounds")
		}
	}
	for index, anchor := range measurement.Evidence {
		if err := anchor.Validate(subjects); err != nil {
			return fmt.Errorf("evidence[%d]: %w", index, err)
		}
	}
	return nil
}

type measurementsValidator struct{}

func ReadMeasurementsRecord(ctx context.Context, root *os.Root) (Record[MeasurementsBody], error) {
	var record Record[MeasurementsBody]
	if err := decodeStrictDocument(ctx, root, "record.json", &record); err != nil {
		return Record[MeasurementsBody]{}, err
	}
	if err := record.validateEnvelopeShape(snapshot.TypeRef("measurements/v1")); err != nil {
		return Record[MeasurementsBody]{}, fmt.Errorf("snapshot contracts: record.json: %w", err)
	}
	if err := record.Body.Validate(record.Subjects); err != nil {
		return Record[MeasurementsBody]{}, fmt.Errorf("snapshot contracts: measurements record: %w", err)
	}
	return record, nil
}

func (measurementsValidator) Validate(ctx context.Context, root *os.Root, validationContext snapshot.ValidationContext) (snapshot.ValidationResult, error) {
	record, err := ReadMeasurementsRecord(ctx, root)
	if err != nil {
		return snapshot.ValidationResult{}, err
	}
	if err := record.ValidateEnvelope(snapshot.TypeRef("measurements/v1"), validationContext); err != nil {
		return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: record.json: %w", err)
	}
	return snapshot.ValidationResult{}, nil
}
