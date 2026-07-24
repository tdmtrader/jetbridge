package contracts

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/concourse/concourse/agent/snapshot"
)

type SelectionBody struct {
	Selected   string                `json:"selected"`
	Candidates []CandidateAssessment `json:"candidates"`
	Rationale  string                `json:"rationale"`
}

type CandidateAssessment struct {
	ID      string       `json:"id"`
	Rank    int          `json:"rank"`
	Summary string       `json:"summary"`
	Scores  []NamedScore `json:"scores"`
}

type NamedScore struct {
	ID    string `json:"id"`
	Score Score  `json:"score"`
}

func (body SelectionBody) Validate(subjects []Subject) error {
	if err := ValidateIdentifier("selected", body.Selected); err != nil {
		return err
	}
	if strings.TrimSpace(body.Rationale) == "" {
		return fmt.Errorf("rationale is required")
	}
	if len(subjects) == 0 {
		return fmt.Errorf("selection requires at least one candidate subject")
	}
	candidateType := subjects[0].Type
	subjectIDs := make(map[string]struct{}, len(subjects))
	for _, subject := range subjects {
		if subject.Role != SubjectRoleCandidate {
			return fmt.Errorf("selection subject %q must have candidate role", subject.ID)
		}
		if subject.Type != candidateType {
			return fmt.Errorf("selection candidate subjects must have one common snapshot type")
		}
		subjectIDs[subject.ID] = struct{}{}
	}
	ids := make([]string, len(body.Candidates))
	ranks := make(map[int]struct{}, len(body.Candidates))
	selectedCount := 0
	for index, candidate := range body.Candidates {
		ids[index] = candidate.ID
		if _, found := subjectIDs[candidate.ID]; !found {
			return fmt.Errorf("candidates[%d].id %q is not a declared candidate subject", index, candidate.ID)
		}
		if candidate.ID == body.Selected {
			selectedCount++
		}
		if candidate.Rank < 1 || candidate.Rank > len(body.Candidates) {
			return fmt.Errorf("candidates[%d].rank must be contiguous from one", index)
		}
		if _, found := ranks[candidate.Rank]; found {
			return fmt.Errorf("candidates[%d].rank %d is duplicate", index, candidate.Rank)
		}
		ranks[candidate.Rank] = struct{}{}
		if strings.TrimSpace(candidate.Summary) == "" {
			return fmt.Errorf("candidates[%d].summary is required", index)
		}
		scoreIDs := make([]string, len(candidate.Scores))
		for scoreIndex, named := range candidate.Scores {
			scoreIDs[scoreIndex] = named.ID
			if err := named.Score.Validate(); err != nil {
				return fmt.Errorf("candidates[%d].scores[%d]: %w", index, scoreIndex, err)
			}
		}
		if err := ValidateEntityIDs(fmt.Sprintf("candidates[%d].scores", index), scoreIDs); err != nil {
			return err
		}
	}
	if err := ValidateEntityIDs("candidates", ids); err != nil {
		return err
	}
	if len(body.Candidates) != len(subjects) {
		return fmt.Errorf("candidates must assess every candidate subject exactly once")
	}
	if selectedCount != 1 {
		return fmt.Errorf("selected candidate must occur exactly once in candidates")
	}
	return nil
}

func ResolveSelection(record Record[SelectionBody], validationContext snapshot.ValidationContext) (snapshot.SnapshotRef, error) {
	if err := validateSelectionRecord(record, validationContext); err != nil {
		return snapshot.SnapshotRef{}, err
	}
	for _, subject := range record.Subjects {
		if subject.ID == record.Body.Selected {
			ref, found := validationContext.Input(subject.Input)
			if !found {
				return snapshot.SnapshotRef{}, fmt.Errorf("selected candidate input %q is unavailable", subject.Input)
			}
			return ref, nil
		}
	}
	return snapshot.SnapshotRef{}, fmt.Errorf("selected candidate %q is not declared", record.Body.Selected)
}

func validateSelectionRecord(record Record[SelectionBody], validationContext snapshot.ValidationContext) error {
	if err := record.ValidateEnvelope(snapshot.TypeRef("selection/v1"), validationContext); err != nil {
		return err
	}
	if err := record.Body.Validate(record.Subjects); err != nil {
		return err
	}
	if len(record.Subjects) != len(validationContext.Inputs()) {
		return fmt.Errorf("selection subjects must represent every exposed input exactly once")
	}
	return nil
}

type selectionValidator struct{}

func (selectionValidator) Validate(ctx context.Context, root *os.Root, validationContext snapshot.ValidationContext) (snapshot.ValidationResult, error) {
	record, err := readRecord[SelectionBody](ctx, root, snapshot.TypeRef("selection/v1"), validationContext)
	if err != nil {
		return snapshot.ValidationResult{}, err
	}
	if err := validateSelectionRecord(record, validationContext); err != nil {
		return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: selection record: %w", err)
	}
	return snapshot.ValidationResult{}, nil
}
