package contracts_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

const emptyLogDigest = "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

func TestValidationRecordPreservesAttemptsAndDerivesConclusion(t *testing.T) {
	subject := snapshot.SnapshotRef{ID: 61, Type: "repository-change/v1", Digest: recordDigest('c')}
	context := validationContextFor(t, map[string]snapshot.SnapshotRef{"change": subject})
	valid := contracts.ValidationBody{
		Conclusion: "passed",
		Summary:    "all checks passed",
		Checks: []contracts.ValidationCheck{{
			ID: "tests", Kind: "test", Name: "unit tests", Status: "passed",
			Attempts: []contracts.ValidationAttempt{
				{Number: 1, Status: "failed", Duration: "2s", Detail: "one failure"},
				{Number: 2, Status: "passed", Duration: "1s"},
			},
		}},
	}
	if !valid.Checks[0].Flaky() {
		t.Fatal("passing retry did not derive flaky")
	}
	if got := valid.Checks[0].Duration(); got != 3*time.Second {
		t.Fatalf("Duration() = %s, want 3s", got)
	}
	validateValidationBody(t, valid, subject, context, false, "")

	tests := []struct {
		name  string
		setup func(*contracts.ValidationBody)
		want  string
	}{
		{"attempt gap", func(body *contracts.ValidationBody) { body.Checks[0].Attempts[1].Number = 3 }, "contiguous"},
		{"status differs from final attempt", func(body *contracts.ValidationBody) { body.Checks[0].Status = "failed" }, "final attempt"},
		{"skipped has attempts", func(body *contracts.ValidationBody) { body.Checks[0].Status = "skipped" }, "skipped"},
		{"failed conclusion hidden", func(body *contracts.ValidationBody) {
			body.Checks[0].Attempts[1].Status = "failed"
			body.Checks[0].Status = "failed"
		}, "conclusion"},
		{"error conclusion hidden", func(body *contracts.ValidationBody) {
			body.Checks[0].Attempts[1].Status = "error"
			body.Checks[0].Status = "error"
		}, "conclusion"},
		{"invalid duration", func(body *contracts.ValidationBody) { body.Checks[0].Attempts[0].Duration = "-1s" }, "duration"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := valid
			body.Checks = append([]contracts.ValidationCheck(nil), valid.Checks...)
			body.Checks[0].Attempts = append([]contracts.ValidationAttempt(nil), valid.Checks[0].Attempts...)
			tc.setup(&body)
			validateValidationBody(t, body, subject, context, true, tc.want)
		})
	}
}

func TestValidationRecordDerivesIncompleteForSkippedOrEmptyChecks(t *testing.T) {
	subject := snapshot.SnapshotRef{ID: 61, Type: "repository-change/v1", Digest: recordDigest('c')}
	context := validationContextFor(t, map[string]snapshot.SnapshotRef{"change": subject})
	for name, checks := range map[string][]contracts.ValidationCheck{
		"empty": {},
		"skipped": {{
			ID: "security", Kind: "security", Name: "security scan", Status: "skipped",
			Attempts: []contracts.ValidationAttempt{},
		}},
	} {
		t.Run(name, func(t *testing.T) {
			body := contracts.ValidationBody{
				Conclusion: "incomplete", Summary: "checks incomplete", Checks: checks,
			}
			validateValidationBody(t, body, subject, context, false, "")
		})
	}
}

func validateValidationBody(
	t *testing.T,
	body contracts.ValidationBody,
	subject snapshot.SnapshotRef,
	context snapshot.ValidationContext,
	wantError bool,
	want string,
) {
	t.Helper()
	body.Attestation = contracts.ValidationAttestation{
		CandidateDigest: subject.Digest, BaseInputs: []contracts.ValidationBaseInput{}, ProfileDigest: recordDigest('a'), ProtectedConfigDigest: recordDigest('b'),
		CapabilityImage:       "example.invalid/validator@sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
		CapabilityImageDigest: recordDigest('d'), WorkflowDefinitionID: 1, WorkflowVersion: 1, Toolchain: "dev-capability/v1",
	}
	files := map[string][]byte{}
	for checkIndex := range body.Checks {
		for attemptIndex := range body.Checks[checkIndex].Attempts {
			path := fmt.Sprintf("content/logs/check-%d-attempt-%d.log", checkIndex, attemptIndex)
			body.Checks[checkIndex].Attempts[attemptIndex].Log = contracts.ValidationLog{Path: path, Digest: snapshot.Digest(emptyLogDigest), Size: 0, MediaType: "text/plain"}
			files[path] = []byte{}
		}
	}
	var err error
	context, err = snapshot.NewValidationContext(context.Inputs(), nil, snapshot.WithValidationAttestationAuthority(snapshot.ValidationAttestationAuthority{
		CandidateInput: "change", Candidate: subject, ProfileDigest: body.Attestation.ProfileDigest, ProtectedConfigDigest: body.Attestation.ProtectedConfigDigest,
		CapabilityImage: body.Attestation.CapabilityImage, CapabilityImageDigest: body.Attestation.CapabilityImageDigest, WorkflowDefinitionID: 1, WorkflowVersion: 1, Toolchain: body.Attestation.Toolchain,
	}))
	if err != nil {
		t.Fatalf("NewValidationContext(): %v", err)
	}
	record, err := contracts.NewRecord(
		mustTypeRef(t, "validation/v1"),
		[]contracts.Subject{contracts.SubjectFromInput(
			"primary", contracts.SubjectRolePrimary, "change", subject,
		)},
		body,
	)
	if err != nil {
		t.Fatalf("NewRecord(): %v", err)
	}
	files["record.json"] = marshalRecord(t, record)
	_, err = validateFiles(t, "validation/v1", files, context)
	if !wantError && err != nil {
		t.Fatalf("valid validation error = %v", err)
	}
	if wantError && (err == nil || !strings.Contains(err.Error(), want)) {
		t.Fatalf("validation error = %v, want %q", err, want)
	}
}
