package contracts

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/concourse/concourse/agent/snapshot"
)

type ReviewBody struct {
	Conclusion string    `json:"conclusion"`
	Summary    string    `json:"summary"`
	Findings   []Finding `json:"findings"`
}

type Finding struct {
	ID             string   `json:"id"`
	Severity       string   `json:"severity"`
	Blocking       bool     `json:"blocking"`
	Category       string   `json:"category"`
	Title          string   `json:"title"`
	Description    string   `json:"description"`
	Evidence       []Anchor `json:"evidence"`
	Recommendation string   `json:"recommendation,omitempty"`
}

func (body ReviewBody) Validate(subjects []Subject) error {
	subjectIDs, err := validateRecordSubjects(subjects, onePrimaryWithSupportingSubjects)
	if err != nil {
		return err
	}
	switch body.Conclusion {
	case "accept", "changes-required", "inconclusive":
	default:
		return fmt.Errorf("conclusion must be one of accept, changes-required, inconclusive")
	}
	if strings.TrimSpace(body.Summary) == "" {
		return fmt.Errorf("summary is required")
	}
	ids := make([]string, len(body.Findings))
	hasBlocking := false
	for index, finding := range body.Findings {
		ids[index] = finding.ID
		if err := finding.Validate(subjectIDs); err != nil {
			return fmt.Errorf("findings[%d]: %w", index, err)
		}
		hasBlocking = hasBlocking || finding.Blocking
	}
	if err := ValidateEntityIDs("findings", ids); err != nil {
		return err
	}
	if body.Conclusion == "changes-required" && !hasBlocking {
		return fmt.Errorf("changes-required conclusion requires at least one blocking finding")
	}
	if body.Conclusion == "accept" && hasBlocking {
		return fmt.Errorf("accept conclusion cannot contain a blocking finding")
	}
	return nil
}

func (finding Finding) Validate(subjects map[string]struct{}) error {
	if err := ValidateIdentifier("finding id", finding.ID); err != nil {
		return err
	}
	switch finding.Severity {
	case "observation":
		if finding.Blocking {
			return fmt.Errorf("observation finding cannot be blocking")
		}
	case "low", "medium":
	case "high", "critical":
		if !finding.Blocking {
			return fmt.Errorf("%s finding must be blocking", finding.Severity)
		}
	default:
		return fmt.Errorf("severity must be one of observation, low, medium, high, critical")
	}
	if err := ValidateIdentifier("finding category", finding.Category); err != nil {
		return err
	}
	if strings.TrimSpace(finding.Title) == "" {
		return fmt.Errorf("finding title is required")
	}
	if strings.TrimSpace(finding.Description) == "" {
		return fmt.Errorf("finding description is required")
	}
	if finding.Severity != "observation" && len(finding.Evidence) == 0 {
		return fmt.Errorf("non-observation finding evidence is required")
	}
	for index, anchor := range finding.Evidence {
		if err := anchor.Validate(subjects); err != nil {
			return fmt.Errorf("evidence[%d]: %w", index, err)
		}
	}
	return nil
}

type reviewValidator struct{}

func (reviewValidator) Validate(ctx context.Context, root *os.Root, validationContext snapshot.ValidationContext) (snapshot.ValidationResult, error) {
	record, err := readRecord[ReviewBody](ctx, root, "review/v1", validationContext)
	if err != nil {
		return snapshot.ValidationResult{}, err
	}
	if err := record.Body.Validate(record.Subjects); err != nil {
		return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: record.json body: %w", err)
	}
	return snapshot.ValidationResult{}, nil
}
