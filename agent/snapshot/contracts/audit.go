package contracts

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
)

type DatabaseSnapshotDocument struct {
	SchemaVersion string `json:"schema_version"`
	CapturedAt    string `json:"captured_at"`
	Engine        string `json:"engine"`
	Format        string `json:"format"`
	DataPath      string `json:"data_path"`
}

func (d DatabaseSnapshotDocument) Validate() error {
	if err := validateDocumentVersion(d.SchemaVersion); err != nil {
		return err
	}
	if err := requireStrings([]namedString{{"captured_at", d.CapturedAt}, {"engine", d.Engine}, {"format", d.Format}}); err != nil {
		return err
	}
	if _, err := time.Parse(time.RFC3339, d.CapturedAt); err != nil {
		return fmt.Errorf("captured_at must be RFC3339: %w", err)
	}
	return validatePOSIXPath("data_path", d.DataPath)
}

type DeploymentSnapshotDocument struct {
	SchemaVersion string `json:"schema_version"`
	CapturedAt    string `json:"captured_at"`
	Platform      string `json:"platform"`
	Format        string `json:"format"`
	ManifestPath  string `json:"manifest_path"`
}

func (d DeploymentSnapshotDocument) Validate() error {
	if err := validateDocumentVersion(d.SchemaVersion); err != nil {
		return err
	}
	if err := requireStrings([]namedString{{"captured_at", d.CapturedAt}, {"platform", d.Platform}, {"format", d.Format}}); err != nil {
		return err
	}
	if _, err := time.Parse(time.RFC3339, d.CapturedAt); err != nil {
		return fmt.Errorf("captured_at must be RFC3339: %w", err)
	}
	return validatePOSIXPath("manifest_path", d.ManifestPath)
}

type AuditFindingsDocument struct {
	SchemaVersion string         `json:"schema_version"`
	Subject       string         `json:"subject"`
	Summary       string         `json:"summary"`
	Findings      []AuditFinding `json:"findings"`
}

type AuditFinding struct {
	ID          string `json:"id"`
	Severity    string `json:"severity"`
	Category    string `json:"category"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Path        string `json:"path,omitempty"`
	Line        int    `json:"line,omitempty"`
}

func (d AuditFindingsDocument) Validate() error {
	if err := validateDocumentVersion(d.SchemaVersion); err != nil {
		return err
	}
	if err := requireStrings([]namedString{{"subject", d.Subject}, {"summary", d.Summary}}); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(d.Findings))
	for i, finding := range d.Findings {
		if err := requireStrings([]namedString{{"id", finding.ID}, {"category", finding.Category}, {"title", finding.Title}, {"description", finding.Description}}); err != nil {
			return fmt.Errorf("findings[%d]: %w", i, err)
		}
		switch finding.Severity {
		case "critical", "high", "medium", "low":
		default:
			return fmt.Errorf("findings[%d].severity must be one of critical, high, medium, low", i)
		}
		if finding.Path != "" {
			if err := validatePOSIXPath("path", finding.Path); err != nil {
				return fmt.Errorf("findings[%d]: %w", i, err)
			}
		}
		if finding.Line < 0 {
			return fmt.Errorf("findings[%d].line must be positive when present", i)
		}
		if finding.Line > 0 && finding.Path == "" {
			return fmt.Errorf("findings[%d].path is required when line is present", i)
		}
		if _, found := seen[finding.ID]; found {
			return fmt.Errorf("findings[%d].id %q is duplicate", i, finding.ID)
		}
		seen[finding.ID] = struct{}{}
	}
	return nil
}

type DiagnosisDocument struct {
	SchemaVersion string              `json:"schema_version"`
	Subject       string              `json:"subject"`
	Summary       string              `json:"summary"`
	Causes        []string            `json:"causes"`
	Evidence      []DiagnosisEvidence `json:"evidence,omitempty"`
}

type DiagnosisEvidence struct {
	Description string `json:"description"`
	Path        string `json:"path"`
}

func (d DiagnosisDocument) Validate() error {
	if err := validateDocumentVersion(d.SchemaVersion); err != nil {
		return err
	}
	if err := requireStrings([]namedString{{"subject", d.Subject}, {"summary", d.Summary}}); err != nil {
		return err
	}
	if len(d.Causes) == 0 {
		return fmt.Errorf("causes must contain at least one diagnosis")
	}
	for i, cause := range d.Causes {
		if strings.TrimSpace(cause) == "" {
			return fmt.Errorf("causes[%d] is required", i)
		}
	}
	for i, evidence := range d.Evidence {
		if strings.TrimSpace(evidence.Description) == "" {
			return fmt.Errorf("evidence[%d].description is required", i)
		}
		if err := validatePOSIXPath("path", evidence.Path); err != nil {
			return fmt.Errorf("evidence[%d]: %w", i, err)
		}
	}
	return nil
}

type auditValidator struct {
	kind string
}

func (v auditValidator) Validate(ctx context.Context, root *os.Root, _ snapshot.ValidationContext) (snapshot.ValidationResult, error) {
	switch v.kind {
	case "database":
		var document DatabaseSnapshotDocument
		if err := decodeStrictDocument(ctx, root, "database-snapshot.json", &document); err != nil {
			return snapshot.ValidationResult{}, err
		}
		if err := document.Validate(); err != nil {
			return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: database-snapshot.json: %w", err)
		}
		if document.DataPath == "database-snapshot.json" {
			return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: data_path must name snapshot payload")
		}
		if err := requireRegularPayload(root, "data_path", document.DataPath); err != nil {
			return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: database-snapshot.json: %w", err)
		}
	case "deployment":
		var document DeploymentSnapshotDocument
		if err := decodeStrictDocument(ctx, root, "deployment-snapshot.json", &document); err != nil {
			return snapshot.ValidationResult{}, err
		}
		if err := document.Validate(); err != nil {
			return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: deployment-snapshot.json: %w", err)
		}
		if document.ManifestPath == "deployment-snapshot.json" {
			return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: manifest_path must name snapshot payload")
		}
		if err := requireRegularPayload(root, "manifest_path", document.ManifestPath); err != nil {
			return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: deployment-snapshot.json: %w", err)
		}
	case "findings":
		var document AuditFindingsDocument
		if err := decodeStrictDocument(ctx, root, "audit-findings.json", &document); err != nil {
			return snapshot.ValidationResult{}, err
		}
		if err := document.Validate(); err != nil {
			return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: audit-findings.json: %w", err)
		}
		for i, finding := range document.Findings {
			if finding.Path != "" {
				if err := requireRegularPayload(root, "path", finding.Path); err != nil {
					return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: findings[%d]: %w", i, err)
				}
			}
		}
	case "diagnosis":
		var document DiagnosisDocument
		if err := decodeStrictDocument(ctx, root, "diagnosis.json", &document); err != nil {
			return snapshot.ValidationResult{}, err
		}
		if err := document.Validate(); err != nil {
			return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: diagnosis.json: %w", err)
		}
		for i, evidence := range document.Evidence {
			if err := requireRegularPayload(root, "path", evidence.Path); err != nil {
				return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: evidence[%d]: %w", i, err)
			}
		}
	default:
		return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: unknown audit validator %q", v.kind)
	}
	return snapshot.ValidationResult{}, nil
}
