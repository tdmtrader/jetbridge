package contracts

import (
	"context"
	"fmt"
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
		return publicRecordFailure(snapshot.RecordFieldMissing, "changes must contain at least one upgrade")
	}
	seen := make(map[string]struct{}, len(d.Changes))
	for i, change := range d.Changes {
		if err := requireStrings([]namedString{{"component", change.Component}, {"from", change.From}, {"to", change.To}}); err != nil {
			return fmt.Errorf("changes[%d]: %w", i, err)
		}
		if _, found := seen[change.Component]; found {
			return publicRecordFailure(snapshot.RecordEntityIDDuplicate, "changes[%d].component %q is duplicate", i, change.Component)
		}
		seen[change.Component] = struct{}{}
	}
	return nil
}

func validateResultStatus(status string) error {
	switch status {
	case "ok", "failed", "error":
		return nil
	default:
		return publicRecordFailure(snapshot.RecordFieldValueNotAllowed, "must be one of ok, failed, error")
	}
}

type namedString struct {
	name  string
	value string
}

func validateDocumentVersion(version string) error {
	if version != "1.0.0" {
		return publicRecordFailure(snapshot.RecordFieldValueNotAllowed, "schema_version must be exactly 1.0.0")
	}
	return nil
}

func requireStrings(values []namedString) error {
	for _, value := range values {
		if strings.TrimSpace(value.value) == "" {
			return publicRecordFailure(snapshot.RecordFieldMissing, "%s is required", value.name)
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
