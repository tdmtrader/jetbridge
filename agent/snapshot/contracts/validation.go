package contracts

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
)

type ValidationBody struct {
	Conclusion string            `json:"conclusion"`
	Summary    string            `json:"summary"`
	Checks     []ValidationCheck `json:"checks"`
}

type ValidationCheck struct {
	ID       string              `json:"id"`
	Kind     string              `json:"kind"`
	Name     string              `json:"name"`
	Status   string              `json:"status"`
	Attempts []ValidationAttempt `json:"attempts"`
	Detail   string              `json:"detail,omitempty"`
}

type ValidationAttempt struct {
	Number   int      `json:"number"`
	Status   string   `json:"status"`
	Duration string   `json:"duration"`
	Evidence []Anchor `json:"evidence"`
	Detail   string   `json:"detail,omitempty"`
}

func (body ValidationBody) Validate(subjects []Subject) error {
	subjectIDs, err := validateRecordSubjects(subjects, onePrimaryWithSupportingSubjects)
	if err != nil {
		return err
	}
	if strings.TrimSpace(body.Summary) == "" {
		return fmt.Errorf("summary is required")
	}
	ids := make([]string, len(body.Checks))
	for index, check := range body.Checks {
		ids[index] = check.ID
		if err := check.Validate(subjectIDs); err != nil {
			return fmt.Errorf("checks[%d]: %w", index, err)
		}
	}
	if err := ValidateEntityIDs("checks", ids); err != nil {
		return err
	}
	derived := DeriveValidationConclusion(body.Checks)
	if body.Conclusion != derived {
		return fmt.Errorf("conclusion must match derived conclusion %q", derived)
	}
	return nil
}

func (check ValidationCheck) Validate(subjects map[string]struct{}) error {
	if err := ValidateIdentifier("check id", check.ID); err != nil {
		return err
	}
	switch check.Kind {
	case "build", "test", "lint", "security", "policy", "custom":
	default:
		return fmt.Errorf("check kind must be one of build, test, lint, security, policy, custom")
	}
	if strings.TrimSpace(check.Name) == "" {
		return fmt.Errorf("check name is required")
	}
	switch check.Status {
	case "skipped":
		if len(check.Attempts) != 0 {
			return fmt.Errorf("skipped check must have no attempts")
		}
		return nil
	case "passed", "failed", "error":
		if len(check.Attempts) == 0 {
			return fmt.Errorf("%s check requires at least one attempt", check.Status)
		}
	default:
		return fmt.Errorf("check status must be one of passed, failed, error, skipped")
	}
	for index, attempt := range check.Attempts {
		if attempt.Number != index+1 {
			return fmt.Errorf("attempt numbers must be contiguous from 1")
		}
		if err := attempt.Validate(subjects); err != nil {
			return fmt.Errorf("attempts[%d]: %w", index, err)
		}
	}
	if final := check.Attempts[len(check.Attempts)-1].Status; check.Status != final {
		return fmt.Errorf("check status %q must match final attempt status %q", check.Status, final)
	}
	return nil
}

func (attempt ValidationAttempt) Validate(subjects map[string]struct{}) error {
	if attempt.Number < 1 {
		return fmt.Errorf("attempt number must be positive")
	}
	switch attempt.Status {
	case "passed", "failed", "error":
	default:
		return fmt.Errorf("attempt status must be one of passed, failed, error")
	}
	duration, err := time.ParseDuration(attempt.Duration)
	if err != nil || duration < 0 {
		return fmt.Errorf("attempt duration must be a nonnegative Go duration")
	}
	for index, anchor := range attempt.Evidence {
		if err := anchor.Validate(subjects); err != nil {
			return fmt.Errorf("evidence[%d]: %w", index, err)
		}
	}
	return nil
}

func (check ValidationCheck) Flaky() bool {
	if check.Status != "passed" || len(check.Attempts) < 2 {
		return false
	}
	for _, attempt := range check.Attempts[:len(check.Attempts)-1] {
		if attempt.Status == "failed" || attempt.Status == "error" {
			return true
		}
	}
	return false
}

func (check ValidationCheck) Duration() time.Duration {
	var total time.Duration
	for _, attempt := range check.Attempts {
		duration, err := time.ParseDuration(attempt.Duration)
		if err == nil && duration >= 0 {
			total += duration
		}
	}
	return total
}

func DeriveValidationConclusion(checks []ValidationCheck) string {
	if len(checks) == 0 {
		return "incomplete"
	}
	hasFailed := false
	hasSkipped := false
	for _, check := range checks {
		switch check.Status {
		case "error":
			return "error"
		case "failed":
			hasFailed = true
		case "skipped":
			hasSkipped = true
		}
	}
	if hasFailed {
		return "failed"
	}
	if hasSkipped {
		return "incomplete"
	}
	return "passed"
}

type validationValidator struct{}

func (validationValidator) Validate(ctx context.Context, root *os.Root, validationContext snapshot.ValidationContext) (snapshot.ValidationResult, error) {
	record, err := readRecord[ValidationBody](ctx, root, "validation/v1", validationContext)
	if err != nil {
		return snapshot.ValidationResult{}, err
	}
	if err := record.Body.Validate(record.Subjects); err != nil {
		return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: record.json body: %w", err)
	}
	return snapshot.ValidationResult{}, nil
}
