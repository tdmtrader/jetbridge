package workflow

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/concourse/concourse/atc"
)

func TestRequirePassingValidationAuthorityRejectsEveryExactMismatch(t *testing.T) {
	candidate := requirementRef(1, "repository-change/v1", 'a')
	base := requirementRef(2, "repository/v1", 'b')
	a := &atc.DevValidationAuthority{ProfileName: "gates", Profile: []byte("profile"), ProfileDigest: digestBytes([]byte("profile")), ProtectedConfig: []byte("config"), ProtectedConfigDigest: digestBytes([]byte("config")), CapabilityImage: "registry.example/validator@" + requirementDigest('c').String(), CapabilityImageDigest: requirementDigest('c'), WorkflowDefinitionID: 7, WorkflowVersion: 3, CandidateInput: "candidate", BaseInputs: []atc.DevValidationBaseInput{{Name: "base", Type: "repository/v1"}}}
	requirement := atc.ReviewValidationRequirement{Candidate: "candidate", Validation: "validation", Authority: a}
	authority, err := requirement.AuthorityFor(candidate, []snapshot.ValidationAuthorityInput{{Input: "base", Ref: base}})
	if err != nil {
		t.Fatal(err)
	}
	good := requirementRecord(t, candidate, authority)
	if err := RequirePassingValidationAuthority(context.Background(), candidate, good, authority); err != nil {
		t.Fatalf("good record rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*contracts.Record[contracts.ValidationBody], *snapshot.ValidationAttestationAuthority)
		want   string
	}{
		{"failed", func(r *contracts.Record[contracts.ValidationBody], _ *snapshot.ValidationAttestationAuthority) {
			r.Body.Conclusion = "failed"
		}, "conclusion"},
		{"candidate", func(r *contracts.Record[contracts.ValidationBody], _ *snapshot.ValidationAttestationAuthority) {
			r.Body.Attestation.CandidateDigest = requirementDigest('d')
		}, "attestation"},
		{"base", func(r *contracts.Record[contracts.ValidationBody], _ *snapshot.ValidationAttestationAuthority) {
			r.Body.Attestation.BaseInputs[0].Digest = requirementDigest('d')
		}, "base"},
		{"profile", func(r *contracts.Record[contracts.ValidationBody], _ *snapshot.ValidationAttestationAuthority) {
			r.Body.Attestation.ProfileDigest = requirementDigest('d')
		}, "attestation"},
		{"config", func(r *contracts.Record[contracts.ValidationBody], _ *snapshot.ValidationAttestationAuthority) {
			r.Body.Attestation.ProtectedConfigDigest = requirementDigest('d')
		}, "attestation"},
		{"image", func(r *contracts.Record[contracts.ValidationBody], _ *snapshot.ValidationAttestationAuthority) {
			r.Body.Attestation.CapabilityImageDigest = requirementDigest('d')
		}, "attestation"},
		{"toolchain", func(r *contracts.Record[contracts.ValidationBody], _ *snapshot.ValidationAttestationAuthority) {
			r.Body.Attestation.Toolchain = "other"
		}, "attestation"},
		{"definition", func(r *contracts.Record[contracts.ValidationBody], _ *snapshot.ValidationAttestationAuthority) {
			r.Body.Attestation.WorkflowDefinitionID++
		}, "attestation"},
		{"version", func(r *contracts.Record[contracts.ValidationBody], _ *snapshot.ValidationAttestationAuthority) {
			r.Body.Attestation.WorkflowVersion++
		}, "attestation"},
		{"revision two", func(r *contracts.Record[contracts.ValidationBody], _ *snapshot.ValidationAttestationAuthority) {
			r.Schema, _ = contracts.SchemaDigestForRevision("validation/v1", 2)
		}, "revision 3"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := good
			test.mutate(&record, &authority)
			if err := RequirePassingValidationAuthority(context.Background(), candidate, record, authority); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func requirementRecord(t *testing.T, candidate snapshot.SnapshotRef, authority snapshot.ValidationAttestationAuthority) contracts.Record[contracts.ValidationBody] {
	t.Helper()
	body := contracts.ValidationBody{Conclusion: "passed", Summary: "passed", Attestation: contracts.ValidationAttestation{CandidateDigest: candidate.Digest, ProfileDigest: authority.ProfileDigest, ProtectedConfigDigest: authority.ProtectedConfigDigest, CapabilityImage: authority.CapabilityImage, CapabilityImageDigest: authority.CapabilityImageDigest, WorkflowDefinitionID: authority.WorkflowDefinitionID, WorkflowVersion: authority.WorkflowVersion, Toolchain: authority.Toolchain, BaseInputs: []contracts.ValidationBaseInput{{Input: "base", Type: "repository/v1", Digest: authority.BaseInputs[0].Ref.Digest}}}, Checks: []contracts.ValidationCheck{{ID: "test", Kind: "test", Name: "test", Status: "passed", Attempts: []contracts.ValidationAttempt{{Number: 1, Status: "passed", Duration: "1s", Log: contracts.ValidationLog{Path: "content/logs/test.log", Digest: requirementDigest('e'), Size: 0, MediaType: "text/plain"}}}}}}
	record, err := contracts.NewRecord("validation/v1", []contracts.Subject{contracts.SubjectFromInput("base", contracts.SubjectRoleBase, "base", authority.BaseInputs[0].Ref), contracts.SubjectFromInput("candidate", contracts.SubjectRolePrimary, "candidate", candidate)}, body)
	if err != nil {
		t.Fatal(err)
	}
	return record
}
func requirementRef(id snapshot.SnapshotID, typ snapshot.TypeRef, c byte) snapshot.SnapshotRef {
	return snapshot.SnapshotRef{ID: id, Type: typ, Digest: requirementDigest(c)}
}
func requirementDigest(c byte) snapshot.Digest {
	return snapshot.Digest("sha256:" + strings.Repeat(string(c), 64))
}
func digestBytes(v []byte) snapshot.Digest {
	sum := sha256.Sum256(v)
	return snapshot.Digest(fmt.Sprintf("sha256:%x", sum))
}
