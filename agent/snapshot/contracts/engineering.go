package contracts

import (
	"context"
	"fmt"
	"math"
	"os"
	"strings"

	"github.com/concourse/concourse/agent/snapshot"
)

type UpgradeRequestDocument struct {
	SchemaVersion  string `json:"schema_version"`
	Component      string `json:"component"`
	CurrentVersion string `json:"current_version"`
	TargetVersion  string `json:"target_version"`
	Reason         string `json:"reason,omitempty"`
}

func (d UpgradeRequestDocument) Validate() error {
	if err := validateDocumentVersion(d.SchemaVersion); err != nil {
		return err
	}
	return requireStrings([]namedString{
		{"component", d.Component}, {"current_version", d.CurrentVersion},
		{"target_version", d.TargetVersion},
	})
}

type UpgradeReportDocument struct {
	SchemaVersion string          `json:"schema_version"`
	Status        string          `json:"status,omitempty"`
	Summary       string          `json:"summary"`
	Changes       []UpgradeChange `json:"changes"`
}

type UpgradeChange struct {
	Component string `json:"component"`
	From      string `json:"from"`
	To        string `json:"to"`
}

func (d UpgradeReportDocument) Validate() error {
	if err := validateDocumentVersion(d.SchemaVersion); err != nil {
		return err
	}
	if err := requireStrings([]namedString{{"summary", d.Summary}}); err != nil {
		return err
	}
	if d.Status != "" {
		if err := validateResultStatus(d.Status); err != nil {
			return fmt.Errorf("status: %w", err)
		}
	}
	if len(d.Changes) == 0 {
		return fmt.Errorf("changes must contain at least one upgrade")
	}
	seen := make(map[string]struct{}, len(d.Changes))
	for i, change := range d.Changes {
		if err := requireStrings([]namedString{{"component", change.Component}, {"from", change.From}, {"to", change.To}}); err != nil {
			return fmt.Errorf("changes[%d]: %w", i, err)
		}
		if _, found := seen[change.Component]; found {
			return fmt.Errorf("changes[%d].component %q is duplicate", i, change.Component)
		}
		seen[change.Component] = struct{}{}
	}
	return nil
}

type ValidationReportDocument struct {
	SchemaVersion string                  `json:"schema_version"`
	Subject       string                  `json:"subject"`
	Status        string                  `json:"status"`
	Summary       string                  `json:"summary"`
	Checks        []LegacyValidationCheck `json:"checks"`
}

type LegacyValidationCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

func (d ValidationReportDocument) Validate() error {
	if err := validateDocumentVersion(d.SchemaVersion); err != nil {
		return err
	}
	if err := requireStrings([]namedString{{"subject", d.Subject}, {"summary", d.Summary}}); err != nil {
		return err
	}
	if err := validateResultStatus(d.Status); err != nil {
		return fmt.Errorf("status: %w", err)
	}
	if len(d.Checks) == 0 {
		return fmt.Errorf("checks must contain at least one result")
	}
	seen := make(map[string]struct{}, len(d.Checks))
	worstStatus := "ok"
	for i, check := range d.Checks {
		if err := requireStrings([]namedString{{"name", check.Name}}); err != nil {
			return fmt.Errorf("checks[%d]: %w", i, err)
		}
		if err := validateResultStatus(check.Status); err != nil {
			return fmt.Errorf("checks[%d].status: %w", i, err)
		}
		if _, found := seen[check.Name]; found {
			return fmt.Errorf("checks[%d].name %q is duplicate", i, check.Name)
		}
		seen[check.Name] = struct{}{}
		if resultStatusRank(check.Status) > resultStatusRank(worstStatus) {
			worstStatus = check.Status
		}
	}
	if d.Status != worstStatus {
		return fmt.Errorf("status must match the worst check status %q", worstStatus)
	}
	return nil
}

type GateResultsDocument struct {
	SchemaVersion string        `json:"schema_version"`
	Gates         []GateOutcome `json:"gates"`
}

type GateOutcome struct {
	Gate            string  `json:"gate"`
	Scope           string  `json:"scope"`
	Status          string  `json:"status"`
	Attempt         int     `json:"attempt"`
	Flaky           bool    `json:"flaky,omitempty"`
	DurationSeconds float64 `json:"duration_seconds"`
	Detail          string  `json:"detail,omitempty"`
}

func (d GateResultsDocument) Validate() error {
	if err := validateDocumentVersion(d.SchemaVersion); err != nil {
		return err
	}
	if len(d.Gates) == 0 {
		return fmt.Errorf("gates must contain at least one gate outcome")
	}
	seen := make(map[string]struct{}, len(d.Gates))
	for i, outcome := range d.Gates {
		if err := requireStrings([]namedString{{"gate", outcome.Gate}, {"scope", outcome.Scope}}); err != nil {
			return fmt.Errorf("gates[%d]: %w", i, err)
		}
		if err := validateResultStatus(outcome.Status); err != nil {
			return fmt.Errorf("gates[%d].status: %w", i, err)
		}
		if outcome.Attempt < 1 {
			return fmt.Errorf("gates[%d].attempt must be positive", i)
		}
		if math.IsNaN(outcome.DurationSeconds) || math.IsInf(outcome.DurationSeconds, 0) || outcome.DurationSeconds < 0 {
			return fmt.Errorf("gates[%d].duration_seconds must be finite and nonnegative", i)
		}
		if outcome.Flaky != (outcome.Status == "ok" && outcome.Attempt > 1) {
			return fmt.Errorf("gates[%d].flaky must be true exactly for an ok result after a retry", i)
		}
		if outcome.Status == "error" && outcome.Attempt != 1 {
			return fmt.Errorf("gates[%d].attempt must be 1 for an error result", i)
		}
		if _, found := seen[outcome.Gate]; found {
			return fmt.Errorf("gates[%d].gate %q is duplicate", i, outcome.Gate)
		}
		seen[outcome.Gate] = struct{}{}
	}
	return nil
}

func validateResultStatus(status string) error {
	switch status {
	case "ok", "failed", "error":
		return nil
	default:
		return fmt.Errorf("must be one of ok, failed, error")
	}
}

func resultStatusRank(status string) int {
	switch status {
	case "error":
		return 2
	case "failed":
		return 1
	default:
		return 0
	}
}

type namedString struct {
	name  string
	value string
}

func validateDocumentVersion(version string) error {
	if version != "1.0.0" {
		return fmt.Errorf("schema_version must be exactly 1.0.0")
	}
	return nil
}

func requireStrings(values []namedString) error {
	for _, value := range values {
		if strings.TrimSpace(value.value) == "" {
			return fmt.Errorf("%s is required", value.name)
		}
	}
	return nil
}

type documentValidator[T interface{ Validate() error }] struct {
	fileName string
}

func (v documentValidator[T]) Validate(ctx context.Context, root *os.Root, _ snapshot.ValidationContext) (snapshot.ValidationResult, error) {
	var document T
	if err := decodeStrictDocument(ctx, root, v.fileName, &document); err != nil {
		return snapshot.ValidationResult{}, err
	}
	if err := document.Validate(); err != nil {
		return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: %s: %w", v.fileName, err)
	}
	return snapshot.ValidationResult{}, nil
}
