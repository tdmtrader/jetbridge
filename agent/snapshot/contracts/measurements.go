package contracts

import (
	"context"
	"fmt"
	"math"
	"os"
	"strings"

	"github.com/concourse/concourse/agent/snapshot"
)

type MeasurementsDocument struct {
	SchemaVersion    string        `json:"schema_version"`
	EvaluatorVersion string        `json:"evaluator_version"`
	Valid            bool          `json:"valid"`
	Explanation      string        `json:"explanation,omitempty"`
	Metrics          []Measurement `json:"metrics"`
}

type Measurement struct {
	Name      string  `json:"name"`
	Value     float64 `json:"value"`
	Unit      string  `json:"unit"`
	Direction string  `json:"direction"`
}

func (d MeasurementsDocument) Validate() error {
	if d.SchemaVersion != "1.0.0" {
		return fmt.Errorf("schema_version must be exactly 1.0.0")
	}
	if strings.TrimSpace(d.EvaluatorVersion) == "" {
		return fmt.Errorf("evaluator_version is required")
	}
	if d.Valid && len(d.Metrics) == 0 {
		return fmt.Errorf("metrics must contain at least one measurement when valid is true")
	}
	if !d.Valid && strings.TrimSpace(d.Explanation) == "" {
		return fmt.Errorf("explanation is required when valid is false")
	}
	seen := make(map[string]struct{}, len(d.Metrics))
	for i, metric := range d.Metrics {
		for name, value := range map[string]string{"name": metric.Name, "unit": metric.Unit} {
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("metrics[%d].%s is required", i, name)
			}
		}
		if math.IsNaN(metric.Value) || math.IsInf(metric.Value, 0) {
			return fmt.Errorf("metrics[%d].value must be finite", i)
		}
		if metric.Direction != "higher" && metric.Direction != "lower" {
			return fmt.Errorf("metrics[%d].direction must be higher or lower", i)
		}
		if _, found := seen[metric.Name]; found {
			return fmt.Errorf("metrics[%d].name %q is duplicate", i, metric.Name)
		}
		seen[metric.Name] = struct{}{}
	}
	return nil
}

type measurementsValidator struct{}

func (measurementsValidator) Validate(ctx context.Context, root *os.Root, _ snapshot.ValidationContext) (snapshot.ValidationResult, error) {
	var document MeasurementsDocument
	if err := decodeStrictDocument(ctx, root, "measurements.json", &document); err != nil {
		return snapshot.ValidationResult{}, err
	}
	if err := document.Validate(); err != nil {
		return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: measurements.json: %w", err)
	}
	return snapshot.ValidationResult{}, nil
}
