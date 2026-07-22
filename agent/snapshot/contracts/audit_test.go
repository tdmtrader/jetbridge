package contracts_test

import (
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func TestDatabaseAndDeploymentSnapshotContractsValidateLocalPayloads(t *testing.T) {
	tests := []struct {
		name     string
		typeRef  string
		fileName string
		document any
		payload  string
	}{
		{
			name: "database", typeRef: "database-snapshot/v1", fileName: "database-snapshot.json", payload: "data/database.dump",
			document: contracts.DatabaseSnapshotDocument{SchemaVersion: "1.0.0", CapturedAt: "2026-07-22T12:00:00Z", Engine: "postgresql", Format: "custom", DataPath: "data/database.dump"},
		},
		{
			name: "deployment", typeRef: "deployment-snapshot/v1", fileName: "deployment-snapshot.json", payload: "manifests/deployment.yaml",
			document: contracts.DeploymentSnapshotDocument{SchemaVersion: "1.0.0", CapturedAt: "2026-07-22T12:00:00Z", Platform: "kubernetes", Format: "yaml", ManifestPath: "manifests/deployment.yaml"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string][]byte{tc.fileName: marshalDocument(t, tc.document), tc.payload: []byte("payload")}
			if _, err := validateFiles(t, tc.typeRef, files, emptyValidationContext(t)); err != nil {
				t.Fatalf("valid snapshot error = %v", err)
			}
		})
	}
}

func TestDatabaseAndDeploymentSnapshotContractsRejectUnsafeOrMissingPayloads(t *testing.T) {
	for name, tc := range map[string]struct {
		typeRef  string
		fileName string
		document any
		want     string
	}{
		"database traversal": {
			typeRef: "database-snapshot/v1", fileName: "database-snapshot.json",
			document: contracts.DatabaseSnapshotDocument{SchemaVersion: "1.0.0", CapturedAt: "2026-07-22T12:00:00Z", Engine: "postgresql", Format: "custom", DataPath: "../dump"}, want: "data_path",
		},
		"deployment missing": {
			typeRef: "deployment-snapshot/v1", fileName: "deployment-snapshot.json",
			document: contracts.DeploymentSnapshotDocument{SchemaVersion: "1.0.0", CapturedAt: "2026-07-22T12:00:00Z", Platform: "kubernetes", Format: "yaml", ManifestPath: "missing.yaml"}, want: "missing",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := validateFiles(t, tc.typeRef, map[string][]byte{tc.fileName: marshalDocument(t, tc.document)}, emptyValidationContext(t)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validation error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestAuditFindingsAndDiagnosisContractsValidateNestedData(t *testing.T) {
	audit := contracts.AuditFindingsDocument{
		SchemaVersion: "1.0.0", Subject: "database configuration", Summary: "one finding",
		Findings: []contracts.AuditFinding{{ID: "AUD-1", Severity: "high", Category: "access-control", Title: "weak setting", Description: "setting permits unsafe access", Path: "evidence/config.txt", Line: 1}},
	}
	if _, err := validateFiles(t, "audit-findings/v1", map[string][]byte{
		"audit-findings.json": marshalDocument(t, audit), "evidence/config.txt": []byte("setting=true"),
	}, emptyValidationContext(t)); err != nil {
		t.Fatalf("valid audit findings error = %v", err)
	}

	diagnosis := contracts.DiagnosisDocument{
		SchemaVersion: "1.0.0", Subject: "failed deployment", Summary: "image was unavailable",
		Causes:   []string{"registry rejected the digest"},
		Evidence: []contracts.DiagnosisEvidence{{Description: "pod event", Path: "evidence/event.txt"}},
	}
	if _, err := validateFiles(t, "diagnosis/v1", map[string][]byte{
		"diagnosis.json": marshalDocument(t, diagnosis), "evidence/event.txt": []byte("pull failed"),
	}, emptyValidationContext(t)); err != nil {
		t.Fatalf("valid diagnosis error = %v", err)
	}

	audit.Findings = append(audit.Findings, audit.Findings[0])
	if _, err := validateFiles(t, "audit-findings/v1", map[string][]byte{
		"audit-findings.json": marshalDocument(t, audit), "evidence/config.txt": []byte("setting=true"),
	}, emptyValidationContext(t)); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate finding error = %v, want duplicate", err)
	}
	diagnosis.Evidence[0].Path = "/absolute"
	if _, err := validateFiles(t, "diagnosis/v1", map[string][]byte{"diagnosis.json": marshalDocument(t, diagnosis)}, emptyValidationContext(t)); err == nil || !strings.Contains(err.Error(), "path") {
		t.Fatalf("unsafe evidence error = %v, want path", err)
	}
	audit.Findings = audit.Findings[:1]
	audit.Findings[0].Category = " "
	if _, err := validateFiles(t, "audit-findings/v1", map[string][]byte{"audit-findings.json": marshalDocument(t, audit)}, emptyValidationContext(t)); err == nil || !strings.Contains(err.Error(), "category") {
		t.Fatalf("blank category error = %v, want category", err)
	}
	audit.Findings[0].Category = "access-control"
	audit.Findings[0].Description = " "
	if _, err := validateFiles(t, "audit-findings/v1", map[string][]byte{"audit-findings.json": marshalDocument(t, audit)}, emptyValidationContext(t)); err == nil || !strings.Contains(err.Error(), "description") {
		t.Fatalf("blank description error = %v, want description", err)
	}
}
