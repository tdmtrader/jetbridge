package contracts

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/concourse/concourse/agent/snapshot"
)

// ConsultationBody is the provider-independent terminal contract returned by
// consult_agent. It keeps claims and their evidence distinct from the answer's
// explicitly stated assumptions and uncertainty.
type ConsultationBody struct {
	Answer          string              `json:"answer"`
	Claims          []ConsultationClaim `json:"claims"`
	Assumptions     []string            `json:"assumptions"`
	Uncertainties   []string            `json:"uncertainties"`
	Recommendations []string            `json:"recommendations"`
}

type ConsultationClaim struct {
	ID        string   `json:"id"`
	Statement string   `json:"statement"`
	Evidence  []Anchor `json:"evidence"`
}

func (body ConsultationBody) Validate(subjects []Subject) error {
	subjectIDs, err := validateRecordSubjects(subjects, onePrimaryWithSupportingSubjects)
	if err != nil {
		return err
	}
	if strings.TrimSpace(body.Answer) == "" {
		return fmt.Errorf("answer is required")
	}
	claimIDs := make([]string, len(body.Claims))
	for index, claim := range body.Claims {
		claimIDs[index] = claim.ID
		if err := claim.Validate(subjectIDs); err != nil {
			return fmt.Errorf("claims[%d]: %w", index, err)
		}
	}
	if err := ValidateEntityIDs("claims", claimIDs); err != nil {
		return err
	}
	for name, values := range map[string][]string{
		"assumptions":     body.Assumptions,
		"uncertainties":   body.Uncertainties,
		"recommendations": body.Recommendations,
	} {
		for index, value := range values {
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("%s[%d] must not be blank", name, index)
			}
		}
	}
	return nil
}

func (claim ConsultationClaim) Validate(subjects map[string]struct{}) error {
	if err := ValidateIdentifier("claim id", claim.ID); err != nil {
		return err
	}
	if strings.TrimSpace(claim.Statement) == "" {
		return fmt.Errorf("claim statement is required")
	}
	for index, anchor := range claim.Evidence {
		if err := anchor.Validate(subjects); err != nil {
			return fmt.Errorf("evidence[%d]: %w", index, err)
		}
	}
	return nil
}

type consultationValidator struct{}

func (consultationValidator) AdmitForSeal(ctx context.Context, root *os.Root, declarations snapshot.ValidationContext) (snapshot.ValidationResult, error) {
	record, err := admitRecordForSeal[ConsultationBody](ctx, root, consultationType, declarations)
	if err != nil {
		return snapshot.ValidationResult{}, err
	}
	return snapshot.ValidationResult{}, consultationBody(record)
}

func (consultationValidator) RevalidateSealed(ctx context.Context, root *os.Root, _ snapshot.ValidationContext) (snapshot.ValidationResult, error) {
	record, err := readSealedRecord[ConsultationBody](ctx, root, consultationType)
	if err != nil {
		return snapshot.ValidationResult{}, err
	}
	return snapshot.ValidationResult{}, consultationBody(record)
}

func consultationBody(record Record[ConsultationBody]) error {
	if err := validateDeclaredBody(consultationType, record.Subjects, record.Body); err != nil {
		return err
	}
	if err := record.Body.Validate(record.Subjects); err != nil {
		return fmt.Errorf("snapshot contracts: record.json body: %w", err)
	}
	return nil
}
