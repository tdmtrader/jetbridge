package contracts_test

import (
	"math"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func TestEngineeringContractsValidateMinimalVersionedDocuments(t *testing.T) {
	tests := []struct {
		name     string
		typeRef  string
		fileName string
		valid    any
		invalid  any
		want     string
	}{
		{
			name: "upgrade request", typeRef: "upgrade-request/v1", fileName: "upgrade-request.json",
			valid:   contracts.UpgradeRequestDocument{SchemaVersion: "1.0.0", Component: "postgres", CurrentVersion: "15", TargetVersion: "16"},
			invalid: contracts.UpgradeRequestDocument{SchemaVersion: "1.0.0", Component: "postgres", CurrentVersion: "15", TargetVersion: " "},
			want:    "target_version",
		},
		{
			name: "upgrade report", typeRef: "upgrade-report/v1", fileName: "upgrade-report.json",
			valid:   contracts.UpgradeReportDocument{SchemaVersion: "1.0.0", Summary: "upgrade completed", Changes: []contracts.UpgradeChange{{Component: "postgres", From: "15", To: "16"}, {Component: "redis", From: "7", To: "8"}}},
			invalid: contracts.UpgradeReportDocument{SchemaVersion: "1.0.0", Summary: "upgrade completed", Changes: []contracts.UpgradeChange{{Component: "postgres", From: "15", To: " "}}},
			want:    "to",
		},
		{
			name: "validation report", typeRef: "validation-report/v1", fileName: "validation-report.json",
			valid:   contracts.ValidationReportDocument{SchemaVersion: "1.0.0", Subject: "upgrade", Status: "error", Summary: "one check errored", Checks: []contracts.ValidationCheck{{Name: "schema", Status: "failed", Detail: "migration missing"}, {Name: "tool", Status: "error", Detail: "scanner unavailable"}}},
			invalid: contracts.ValidationReportDocument{SchemaVersion: "1.0.0", Subject: "upgrade", Status: "failed", Summary: "one check failed", Checks: []contracts.ValidationCheck{{Name: "schema", Status: "unknown"}}},
			want:    "status",
		},
		{
			name: "gate results", typeRef: "gate-results/v1", fileName: "gate-results.json",
			valid:   contracts.GateResultsDocument{SchemaVersion: "1.0.0", Gates: []contracts.GateOutcome{{Gate: "build", Scope: "full", Status: "ok", Attempt: 1, DurationSeconds: 1.5}, {Gate: "test", Scope: "full", Status: "ok", Attempt: 2, Flaky: true, DurationSeconds: 2.5}}},
			invalid: contracts.GateResultsDocument{SchemaVersion: "1.0.0", Gates: []contracts.GateOutcome{{Gate: "test", Scope: "full", Status: "ok", Attempt: 2, Flaky: false, DurationSeconds: 2.5}}},
			want:    "flaky",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := validateFiles(t, tc.typeRef, map[string][]byte{tc.fileName: marshalDocument(t, tc.valid)}, emptyValidationContext(t)); err != nil {
				t.Fatalf("valid document error = %v", err)
			}
			if _, err := validateFiles(t, tc.typeRef, map[string][]byte{tc.fileName: marshalDocument(t, tc.invalid)}, emptyValidationContext(t)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("invalid document error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestEngineeringContractsRejectWrongVersionDuplicateNamesAndStrictJSON(t *testing.T) {
	document := contracts.ValidationReportDocument{
		SchemaVersion: "1.0.1", Subject: "subject", Status: "ok", Summary: "summary",
		Checks: []contracts.ValidationCheck{{Name: "same", Status: "ok"}, {Name: "same", Status: "ok"}},
	}
	if _, err := validateFiles(t, "validation-report/v1", map[string][]byte{"validation-report.json": marshalDocument(t, document)}, emptyValidationContext(t)); err == nil || !strings.Contains(err.Error(), "1.0.0") {
		t.Fatalf("wrong-version error = %v, want exact version error", err)
	}
	document.SchemaVersion = "1.0.0"
	if _, err := validateFiles(t, "validation-report/v1", map[string][]byte{"validation-report.json": marshalDocument(t, document)}, emptyValidationContext(t)); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate-check error = %v, want duplicate error", err)
	}

	unknown := []byte(`{"schema_version":"1.0.0","component":"c","current_version":"1","target_version":"2","unknown":true}`)
	if _, err := validateFiles(t, "upgrade-request/v1", map[string][]byte{"upgrade-request.json": unknown}, emptyValidationContext(t)); err == nil {
		t.Fatal("unknown field validation succeeded")
	}
}

func TestGateResultsContractValidatesGateOutcomeSemantics(t *testing.T) {
	valid := contracts.GateResultsDocument{SchemaVersion: "1.0.0", Gates: []contracts.GateOutcome{{
		Gate: "test", Scope: "full", Status: "failed", Attempt: 3, DurationSeconds: 1,
	}}}
	for _, tc := range []struct {
		name  string
		setup func(*contracts.GateResultsDocument)
		want  string
	}{
		{"duplicate gate", func(d *contracts.GateResultsDocument) { d.Gates = append(d.Gates, d.Gates[0]) }, "duplicate"},
		{"invalid status", func(d *contracts.GateResultsDocument) { d.Gates[0].Status = "pass" }, "status"},
		{"zero attempt", func(d *contracts.GateResultsDocument) { d.Gates[0].Attempt = 0 }, "attempt"},
		{"negative duration", func(d *contracts.GateResultsDocument) { d.Gates[0].DurationSeconds = -1 }, "duration_seconds"},
		{"flaky failure", func(d *contracts.GateResultsDocument) { d.Gates[0].Flaky = true }, "flaky"},
		{"retried error", func(d *contracts.GateResultsDocument) { d.Gates[0].Status = "error" }, "attempt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			document := valid
			document.Gates = append([]contracts.GateOutcome(nil), valid.Gates...)
			tc.setup(&document)
			if _, err := validateFiles(t, "gate-results/v1", map[string][]byte{"gate-results.json": marshalDocument(t, document)}, emptyValidationContext(t)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validation error = %v, want %q", err, tc.want)
			}
		})
	}
	document := valid
	document.Gates = append([]contracts.GateOutcome(nil), valid.Gates...)
	document.Gates[0].DurationSeconds = math.Inf(1)
	if err := document.Validate(); err == nil || !strings.Contains(err.Error(), "duration_seconds") {
		t.Fatalf("nonfinite duration error = %v, want duration_seconds error", err)
	}
}

func TestValidationReportStatusReflectsWorstCheck(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status string
		checks []contracts.ValidationCheck
	}{
		{"ok cannot hide failure", "ok", []contracts.ValidationCheck{{Name: "check", Status: "failed"}}},
		{"failed cannot hide error", "failed", []contracts.ValidationCheck{{Name: "check", Status: "error"}}},
		{"error cannot overstate failed checks", "error", []contracts.ValidationCheck{{Name: "check", Status: "failed"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			document := contracts.ValidationReportDocument{
				SchemaVersion: "1.0.0", Subject: "subject", Status: tc.status,
				Summary: "summary", Checks: tc.checks,
			}
			if _, err := validateFiles(t, "validation-report/v1", map[string][]byte{"validation-report.json": marshalDocument(t, document)}, emptyValidationContext(t)); err == nil || !strings.Contains(err.Error(), "status") {
				t.Fatalf("status consistency error = %v, want status", err)
			}
		})
	}
}
