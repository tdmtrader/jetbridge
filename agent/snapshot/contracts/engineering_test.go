package contracts_test

import (
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

func TestEngineeringContractsRejectWrongVersionDuplicateComponentsAndStrictJSON(t *testing.T) {
	report := contracts.UpgradeReportDocument{
		SchemaVersion: "1.0.1", Summary: "summary",
		Changes: []contracts.UpgradeChange{
			{Component: "postgres", From: "15", To: "16"},
			{Component: "postgres", From: "16", To: "17"},
		},
	}
	if _, err := validateFiles(t, "upgrade-report/v1", map[string][]byte{"upgrade-report.json": marshalDocument(t, report)}, emptyValidationContext(t)); err == nil || !strings.Contains(err.Error(), "1.0.0") {
		t.Fatalf("wrong-version error = %v, want exact version error", err)
	}
	report.SchemaVersion = "1.0.0"
	if _, err := validateFiles(t, "upgrade-report/v1", map[string][]byte{"upgrade-report.json": marshalDocument(t, report)}, emptyValidationContext(t)); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate-component error = %v, want duplicate error", err)
	}

	unknown := []byte(`{"schema_version":"1.0.0","component":"c","current_version":"1","target_version":"2","unknown":true}`)
	if _, err := validateFiles(t, "upgrade-request/v1", map[string][]byte{"upgrade-request.json": unknown}, emptyValidationContext(t)); err == nil {
		t.Fatal("unknown field validation succeeded")
	}
}
