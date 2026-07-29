package atc

import (
	"fmt"

	"github.com/concourse/concourse/agent/snapshot"
)

// These requirements are private, rendered execution facts for the three
// governed consumers. They intentionally are not a general purpose gate API.
type ReviewValidationRequirement struct {
	Candidate  string                  `json:"candidate"`
	Validation string                  `json:"validation"`
	Authority  *DevValidationAuthority `json:"authority"`
}
type MergeApprovalValidationRequirement struct {
	Candidate  string                  `json:"candidate"`
	Validation string                  `json:"validation"`
	Authority  *DevValidationAuthority `json:"authority"`
}
type PublishValidationRequirement struct {
	Candidate  string                  `json:"candidate"`
	Validation string                  `json:"validation"`
	Authority  *DevValidationAuthority `json:"authority"`
}

func (r *ReviewValidationRequirement) Clone() *ReviewValidationRequirement {
	if r == nil {
		return nil
	}
	c := *r
	c.Authority = r.Authority.Clone()
	return &c
}
func (r *MergeApprovalValidationRequirement) Clone() *MergeApprovalValidationRequirement {
	if r == nil {
		return nil
	}
	c := *r
	c.Authority = r.Authority.Clone()
	return &c
}
func (r *PublishValidationRequirement) Clone() *PublishValidationRequirement {
	if r == nil {
		return nil
	}
	c := *r
	c.Authority = r.Authority.Clone()
	return &c
}

func validateValidationRequirement(candidate, validation string, authority *DevValidationAuthority) error {
	for _, item := range []struct{ label, value string }{{"candidate", candidate}, {"validation", validation}} {
		if warning, err := ValidateIdentifier(item.value); err != nil || warning != nil {
			return fmt.Errorf("validation requirement %s is not a safe identifier", item.label)
		}
	}
	if authority == nil {
		return fmt.Errorf("validation requirement authority is required")
	}
	if err := authority.Validate(); err != nil {
		return fmt.Errorf("validation requirement authority: %w", err)
	}
	if authority.CandidateInput != candidate {
		return fmt.Errorf("validation requirement candidate does not match authority")
	}
	return nil
}
func (r ReviewValidationRequirement) Validate() error {
	return validateValidationRequirement(r.Candidate, r.Validation, r.Authority)
}
func (r MergeApprovalValidationRequirement) Validate() error {
	return validateValidationRequirement(r.Candidate, r.Validation, r.Authority)
}
func (r PublishValidationRequirement) Validate() error {
	return validateValidationRequirement(r.Candidate, r.Validation, r.Authority)
}

func requirementAuthority(candidateName string, authority *DevValidationAuthority, candidate snapshot.SnapshotRef, bases []snapshot.ValidationAuthorityInput) (snapshot.ValidationAttestationAuthority, error) {
	if err := validateValidationRequirement(candidateName, "validation", authority); err != nil {
		return snapshot.ValidationAttestationAuthority{}, err
	}
	result := snapshot.ValidationAttestationAuthority{CandidateInput: candidateName, Candidate: candidate, BaseInputs: append([]snapshot.ValidationAuthorityInput(nil), bases...), ProfileDigest: authority.ProfileDigest, ProtectedConfigDigest: authority.ProtectedConfigDigest, CapabilityImage: authority.CapabilityImage, CapabilityImageDigest: authority.CapabilityImageDigest, WorkflowDefinitionID: authority.WorkflowDefinitionID, WorkflowVersion: authority.WorkflowVersion, Toolchain: "dev-capability/" + authority.CapabilityImageDigest.String()}
	if err := result.Validate(); err != nil {
		return snapshot.ValidationAttestationAuthority{}, err
	}
	return result, nil
}
func (r ReviewValidationRequirement) AuthorityFor(c snapshot.SnapshotRef, b []snapshot.ValidationAuthorityInput) (snapshot.ValidationAttestationAuthority, error) {
	return requirementAuthority(r.Candidate, r.Authority, c, b)
}
func (r MergeApprovalValidationRequirement) AuthorityFor(c snapshot.SnapshotRef, b []snapshot.ValidationAuthorityInput) (snapshot.ValidationAttestationAuthority, error) {
	return requirementAuthority(r.Candidate, r.Authority, c, b)
}
func (r PublishValidationRequirement) AuthorityFor(c snapshot.SnapshotRef, b []snapshot.ValidationAuthorityInput) (snapshot.ValidationAttestationAuthority, error) {
	return requirementAuthority(r.Candidate, r.Authority, c, b)
}
