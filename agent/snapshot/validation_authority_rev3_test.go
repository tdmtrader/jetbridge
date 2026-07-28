package snapshot

import (
	"strings"
	"testing"
)

// This catches an authority channel that is either mutable at the caller or
// silently accepted more than once: both would let unrelated output sealing
// manufacture validation provenance.
func TestValidationContextRejectsDuplicateAttestationAuthority(t *testing.T) {
	authority := ValidationAttestationAuthority{
		CandidateInput: "candidate",
		Candidate:      SnapshotRef{ID: 1, Type: "repository-change/v1", Digest: Digest("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")},
		BaseInputs: []ValidationAuthorityInput{{
			Input: "base",
			Ref:   SnapshotRef{ID: 2, Type: "repository/v1", Digest: Digest("sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")},
		}},
		ProfileDigest:         Digest("sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"),
		ProtectedConfigDigest: Digest("sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"),
		CapabilityImage:       "example.invalid/validator@sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
		CapabilityImageDigest: Digest("sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"),
		WorkflowDefinitionID:  1,
		WorkflowVersion:       1,
		Toolchain:             "dev-capability/v1",
	}

	_, err := NewValidationContext(
		map[string]SnapshotRef{
			"candidate": authority.Candidate,
			"base":      authority.BaseInputs[0].Ref,
		},
		nil,
		WithValidationAttestationAuthority(authority),
		WithValidationAttestationAuthority(authority),
	)
	if err == nil {
		t.Fatal("NewValidationContext accepted duplicate validation attestation authority")
	}
}

func TestValidationAuthorityUsesCanonicalSnapshotPortNames(t *testing.T) {
	valid := ValidationAttestationAuthority{
		CandidateInput: "candidate-1", Candidate: SnapshotRef{ID: 1, Type: "repository-change/v1", Digest: Digest("sha256:" + strings.Repeat("a", 64))},
		BaseInputs:    []ValidationAuthorityInput{{Input: "base_1", Ref: SnapshotRef{ID: 2, Type: "repository/v1", Digest: Digest("sha256:" + strings.Repeat("b", 64))}}},
		ProfileDigest: Digest("sha256:" + strings.Repeat("c", 64)), ProtectedConfigDigest: Digest("sha256:" + strings.Repeat("d", 64)),
		CapabilityImage: "example.invalid/validator@sha256:" + strings.Repeat("e", 64), CapabilityImageDigest: Digest("sha256:" + strings.Repeat("e", 64)), WorkflowDefinitionID: 1, WorkflowVersion: 1, Toolchain: "tool/v1",
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid authority: %v", err)
	}
	for _, bad := range []string{"candidate/base", "candidate\nnext", "candidate\x00next"} {
		candidate := valid
		candidate.CandidateInput = bad
		if err := candidate.Validate(); err == nil {
			t.Fatalf("CandidateInput %q was accepted", bad)
		}
		base := valid
		base.BaseInputs[0].Input = bad
		if err := base.Validate(); err == nil {
			t.Fatalf("ValidationBaseInput %q was accepted", bad)
		}
	}
}

func TestSealRequestValidationAuthorityRejectsForgedOutputAndClonesBaseInputs(t *testing.T) {
	authority := ValidationAttestationAuthority{
		CandidateInput: "candidate", Candidate: SnapshotRef{ID: 1, Type: "repository-change/v1", Digest: Digest("sha256:" + strings.Repeat("a", 64))},
		BaseInputs:    []ValidationAuthorityInput{{Input: "base", Ref: SnapshotRef{ID: 2, Type: "repository/v1", Digest: Digest("sha256:" + strings.Repeat("b", 64))}}},
		ProfileDigest: Digest("sha256:" + strings.Repeat("c", 64)), ProtectedConfigDigest: Digest("sha256:" + strings.Repeat("d", 64)),
		CapabilityImage: "example.invalid/validator@sha256:" + strings.Repeat("e", 64), CapabilityImageDigest: Digest("sha256:" + strings.Repeat("e", 64)), WorkflowDefinitionID: 1, WorkflowVersion: 1, Toolchain: "tool/v1",
	}
	request := sealerRequest([]OutputSource{sealerSource("out", "out", "opaque/v1", tarBytes(t, "out", "x"))})
	request.Inputs = map[string]SnapshotRef{"candidate": authority.Candidate, "base": authority.BaseInputs[0].Ref}
	request.InputOrder = []string{"candidate", "base"}
	request.ValidationAttestationAuthorities = map[string]ValidationAttestationAuthority{"out": authority}
	clone := request.Clone()
	clone.ValidationAttestationAuthorities["out"] = ValidationAttestationAuthority{}
	if request.ValidationAttestationAuthorities["out"].Toolchain != "tool/v1" {
		t.Fatal("SealRequest.Clone aliased authority map")
	}
	forged := request.Clone()
	delete(forged.ValidationAttestationAuthorities, "out")
	forged.ValidationAttestationAuthorities["ghost"] = authority
	if err := forged.Validate(); err == nil || !strings.Contains(err.Error(), "undeclared output") {
		t.Fatalf("forged output authority error = %v", err)
	}
	if err := request.Validate(); err == nil || !strings.Contains(err.Error(), "must declare validation/v1") {
		t.Fatalf("opaque output authority error = %v", err)
	}
}
