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
			return publicRecordFailure(snapshot.RecordConclusionInconsistent, "measured conclusion requires at least one metric")
		}
	case "partial":
		if len(body.Metrics) == 0 {
			return publicRecordFailure(snapshot.RecordConclusionInconsistent, "partial conclusion requires at least one metric")
		}
		if strings.TrimSpace(body.Explanation) == "" {
			return publicRecordFailure(snapshot.RecordConclusionInconsistent, "partial conclusion requires an explanation")
		}
	case "not-applicable":
		if len(body.Metrics) != 0 {
			return publicRecordFailure(snapshot.RecordConclusionInconsistent, "not-applicable conclusion requires no metrics")
		}
		if strings.TrimSpace(body.Explanation) == "" {
			return publicRecordFailure(snapshot.RecordConclusionInconsistent, "not-applicable conclusion requires an explanation")
		}
	default:
		return publicRecordFailure(snapshot.RecordFieldValueNotAllowed, "conclusion must be one of measured, partial, not-applicable")
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
		return publicRecordFailure(snapshot.RecordFieldOutOfRange, "measurement value must be finite")
	}
	switch measurement.Direction {
	case "higher-is-better", "lower-is-better":
		if measurement.Target != nil {
			return publicRecordFailure(snapshot.RecordFieldOutOfRange, "measurement target is valid only for target direction")
		}
	case "target":
		if measurement.Target == nil || !finiteNumber(*measurement.Target) {
			return publicRecordFailure(snapshot.RecordFieldOutOfRange, "target direction requires a finite target")
		}
	default:
		return publicRecordFailure(snapshot.RecordFieldValueNotAllowed,
			"measurement direction must be one of higher-is-better, lower-is-better, target")
	}
	if (measurement.Minimum == nil) != (measurement.Maximum == nil) {
		return publicRecordFailure(snapshot.RecordFieldOutOfRange, "measurement minimum and maximum must be declared together")
	}
	if measurement.Minimum != nil {
		if !finiteNumber(*measurement.Minimum) || !finiteNumber(*measurement.Maximum) || *measurement.Minimum > *measurement.Maximum {
			return publicRecordFailure(snapshot.RecordFieldOutOfRange,
				"measurement bounds must be finite and minimum must not exceed maximum")
		}
		if measurement.Value < *measurement.Minimum || measurement.Value > *measurement.Maximum {
			return publicRecordFailure(snapshot.RecordFieldOutOfRange, "measurement value must be within its declared bounds")
		}
		if measurement.Target != nil && (*measurement.Target < *measurement.Minimum || *measurement.Target > *measurement.Maximum) {
			return publicRecordFailure(snapshot.RecordFieldOutOfRange, "measurement target must be within its declared bounds")
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

// ReadSealedMeasurementsRecord re-validates one stored measurements/v1 tree at
// the READ-TIME gate. It takes no step declarations, because a reader loading a
// stored record has none.
func ReadSealedMeasurementsRecord(ctx context.Context, root *os.Root) (Record[MeasurementsBody], error) {
	record, err := readSealedRecord[MeasurementsBody](ctx, root, measurementsType)
	if err != nil {
		return Record[MeasurementsBody]{}, err
	}
	return record, measurementsBody(record)
}

func (measurementsValidator) AdmitForSeal(ctx context.Context, root *os.Root, declarations snapshot.ValidationContext) (snapshot.ValidationResult, error) {
	record, err := admitRecordForSeal[MeasurementsBody](ctx, root, measurementsType, declarations)
	if err != nil {
		return snapshot.ValidationResult{}, err
	}
	return snapshot.ValidationResult{}, measurementsBody(record)
}

func (measurementsValidator) RevalidateSealed(ctx context.Context, root *os.Root, _ snapshot.ValidationContext) (snapshot.ValidationResult, error) {
	_, err := ReadSealedMeasurementsRecord(ctx, root)
	return snapshot.ValidationResult{}, err
}

func measurementsBody(record Record[MeasurementsBody]) error {
	if err := validateDeclaredBody(measurementsType, record.Subjects, record.Body); err != nil {
		return err
	}
	if err := record.Body.Validate(record.Subjects); err != nil {
		return fmt.Errorf("snapshot contracts: measurements record: %w", err)
	}
	return nil
}
