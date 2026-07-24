package contracts

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/concourse/concourse/agent/snapshot"
)

type DiagnosisBody struct {
	Summary    string            `json:"summary"`
	Conclusion string            `json:"conclusion"`
	Hypotheses []Hypothesis      `json:"hypotheses"`
	Actions    []DiagnosisAction `json:"actions"`
}

type Hypothesis struct {
	ID              string   `json:"id"`
	Rank            int      `json:"rank"`
	Statement       string   `json:"statement"`
	Confidence      Score    `json:"confidence"`
	Evidence        []Anchor `json:"evidence"`
	Counterevidence []Anchor `json:"counterevidence"`
}

type DiagnosisAction struct {
	ID          string   `json:"id"`
	Priority    string   `json:"priority"`
	Description string   `json:"description"`
	Addresses   []string `json:"addresses"`
	Rationale   string   `json:"rationale,omitempty"`
}

func (body DiagnosisBody) Validate(subjects []Subject) error {
	subjectIDs, err := validateRecordSubjects(subjects, onePrimaryWithSupportingSubjects)
	if err != nil {
		return err
	}
	if strings.TrimSpace(body.Summary) == "" {
		return fmt.Errorf("summary is required")
	}
	switch body.Conclusion {
	case "identified", "suspected":
		if len(body.Hypotheses) == 0 {
			return fmt.Errorf("%s conclusion requires hypotheses", body.Conclusion)
		}
	case "inconclusive":
	default:
		return fmt.Errorf("conclusion must be one of identified, suspected, inconclusive")
	}

	hypothesisIDs := make([]string, len(body.Hypotheses))
	ranks := make(map[int]struct{}, len(body.Hypotheses))
	hypotheses := make(map[string]struct{}, len(body.Hypotheses))
	var rankOne *Hypothesis
	for index := range body.Hypotheses {
		hypothesis := &body.Hypotheses[index]
		hypothesisIDs[index] = hypothesis.ID
		if err := hypothesis.Validate(subjectIDs); err != nil {
			return fmt.Errorf("hypotheses[%d]: %w", index, err)
		}
		if _, found := ranks[hypothesis.Rank]; found {
			return fmt.Errorf("hypotheses[%d].rank %d is duplicate", index, hypothesis.Rank)
		}
		ranks[hypothesis.Rank] = struct{}{}
		hypotheses[hypothesis.ID] = struct{}{}
		if hypothesis.Rank == 1 {
			rankOne = hypothesis
		}
	}
	if err := ValidateEntityIDs("hypotheses", hypothesisIDs); err != nil {
		return err
	}
	for rank := 1; rank <= len(body.Hypotheses); rank++ {
		if _, found := ranks[rank]; !found {
			return fmt.Errorf("hypothesis ranks must be unique and contiguous from 1")
		}
	}
	if body.Conclusion == "identified" && (rankOne == nil || len(rankOne.Evidence) == 0) {
		return fmt.Errorf("identified conclusion requires evidence for the rank-1 hypothesis")
	}

	actionIDs := make([]string, len(body.Actions))
	for index, action := range body.Actions {
		actionIDs[index] = action.ID
		if err := action.Validate(hypotheses); err != nil {
			return fmt.Errorf("actions[%d]: %w", index, err)
		}
	}
	return ValidateEntityIDs("actions", actionIDs)
}

func (hypothesis Hypothesis) Validate(subjects map[string]struct{}) error {
	if err := ValidateIdentifier("hypothesis id", hypothesis.ID); err != nil {
		return err
	}
	if hypothesis.Rank < 1 {
		return fmt.Errorf("hypothesis rank must be positive")
	}
	if strings.TrimSpace(hypothesis.Statement) == "" {
		return fmt.Errorf("hypothesis statement is required")
	}
	if err := hypothesis.Confidence.Validate(); err != nil {
		return fmt.Errorf("hypothesis confidence: %w", err)
	}
	if hypothesis.Confidence.Scale != "unit-interval" ||
		hypothesis.Confidence.Direction != "higher-is-better" {
		return fmt.Errorf("hypothesis confidence must be a higher-is-better unit-interval score")
	}
	for index, anchor := range hypothesis.Evidence {
		if err := anchor.Validate(subjects); err != nil {
			return fmt.Errorf("evidence[%d]: %w", index, err)
		}
	}
	for index, anchor := range hypothesis.Counterevidence {
		if err := anchor.Validate(subjects); err != nil {
			return fmt.Errorf("counterevidence[%d]: %w", index, err)
		}
	}
	return nil
}

func (action DiagnosisAction) Validate(hypotheses map[string]struct{}) error {
	if err := ValidateIdentifier("action id", action.ID); err != nil {
		return err
	}
	switch action.Priority {
	case "immediate", "next", "optional":
	default:
		return fmt.Errorf("action priority must be one of immediate, next, optional")
	}
	if strings.TrimSpace(action.Description) == "" {
		return fmt.Errorf("action description is required")
	}
	if err := ValidateEntityIDs("action addresses", action.Addresses); err != nil {
		return err
	}
	for _, addressed := range action.Addresses {
		if _, found := hypotheses[addressed]; !found {
			return fmt.Errorf("action addresses unknown hypothesis %q", addressed)
		}
	}
	return nil
}

type diagnosisValidator struct{}

func (diagnosisValidator) Validate(ctx context.Context, root *os.Root, validationContext snapshot.ValidationContext) (snapshot.ValidationResult, error) {
	record, err := readRecord[DiagnosisBody](ctx, root, "diagnosis/v1", validationContext)
	if err != nil {
		return snapshot.ValidationResult{}, err
	}
	if err := record.Body.Validate(record.Subjects); err != nil {
		return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: record.json body: %w", err)
	}
	return snapshot.ValidationResult{}, nil
}
