package contracts

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/concourse/concourse/agent/snapshot"
)

type PublishImpactBody struct {
	BaselineDigest     string                 `json:"baseline_digest"`
	CandidateDigest    string                 `json:"candidate_digest"`
	ChangedFiles       []PublishChangedFile   `json:"changed_files"`
	ChangedLines       int                    `json:"changed_lines"`
	ConflictResolution bool                   `json:"conflict_resolution"`
	ValidationChanges  []string               `json:"validation_changes"`
	RuleResults        []PublishImpactRule    `json:"rule_results"`
	AgentAssessment    *AgentImpactAssessment `json:"agent_assessment,omitempty"`
	ReapprovalRequired bool                   `json:"reapproval_required"`
	Reasons            []string               `json:"reasons"`
}

type PublishChangedFile struct {
	Path         string `json:"path"`
	AddedLines   int    `json:"added_lines"`
	RemovedLines int    `json:"removed_lines"`
}

type PublishImpactRule struct {
	ID     string `json:"id"`
	Passed bool   `json:"passed"`
	Reason string `json:"reason"`
}

// AgentImpactAssessment can only add a reapproval requirement. It is incapable
// of overriding deterministic rule results, which remain platform authority.
type AgentImpactAssessment struct {
	ReapprovalRequired bool   `json:"reapproval_required"`
	Rationale          string `json:"rationale"`
}

func (body PublishImpactBody) Validate(_ []Subject) error {
	if err := snapshot.Digest(body.BaselineDigest).Validate(); err != nil {
		return fmt.Errorf("baseline digest: %w", err)
	}
	if err := snapshot.Digest(body.CandidateDigest).Validate(); err != nil {
		return fmt.Errorf("candidate digest: %w", err)
	}
	if body.BaselineDigest == body.CandidateDigest {
		return fmt.Errorf("baseline and candidate digests must differ")
	}
	if body.ChangedLines < 0 {
		return fmt.Errorf("changed lines must not be negative")
	}
	filePaths := make([]string, len(body.ChangedFiles))
	calculatedLines := 0
	for index, file := range body.ChangedFiles {
		filePaths[index] = file.Path
		if err := file.Validate(); err != nil {
			return fmt.Errorf("changed_files[%d]: %w", index, err)
		}
		calculatedLines += file.AddedLines + file.RemovedLines
	}
	if err := validateSortedUniquePaths("changed files", filePaths); err != nil {
		return err
	}
	if body.ChangedLines != calculatedLines {
		return fmt.Errorf("changed lines must equal the total added and removed lines")
	}
	if err := validateSortedNonblankStrings("validation changes", body.ValidationChanges); err != nil {
		return err
	}
	ruleIDs := make([]string, len(body.RuleResults))
	failedDeterministicRule := false
	for index, rule := range body.RuleResults {
		ruleIDs[index] = rule.ID
		if err := rule.Validate(); err != nil {
			return fmt.Errorf("rule_results[%d]: %w", index, err)
		}
		failedDeterministicRule = failedDeterministicRule || !rule.Passed
	}
	if err := ValidateEntityIDs("rule results", ruleIDs); err != nil {
		return err
	}
	if body.AgentAssessment != nil {
		if err := body.AgentAssessment.Validate(); err != nil {
			return fmt.Errorf("agent assessment: %w", err)
		}
	}
	if failedDeterministicRule && !body.ReapprovalRequired {
		return fmt.Errorf("a failed deterministic rule requires reapproval")
	}
	if body.AgentAssessment != nil && body.AgentAssessment.ReapprovalRequired && !body.ReapprovalRequired {
		return fmt.Errorf("agent assessment requiring reapproval cannot be waived")
	}
	if body.ReapprovalRequired && len(body.Reasons) == 0 {
		return fmt.Errorf("reapproval requires at least one reason")
	}
	return validateSortedNonblankStrings("reasons", body.Reasons)
}

func (file PublishChangedFile) Validate() error {
	if err := validatePOSIXPath("changed file path", file.Path); err != nil {
		return err
	}
	if file.AddedLines < 0 || file.RemovedLines < 0 {
		return fmt.Errorf("added and removed lines must not be negative")
	}
	return nil
}

func (rule PublishImpactRule) Validate() error {
	if err := ValidateIdentifier("rule id", rule.ID); err != nil {
		return err
	}
	return validateBoundedMarkdown("rule reason", rule.Reason)
}

func (assessment AgentImpactAssessment) Validate() error {
	return validateBoundedMarkdown("agent assessment rationale", assessment.Rationale)
}

func validateSortedUniquePaths(label string, values []string) error {
	for index, value := range values {
		if err := validatePOSIXPath(fmt.Sprintf("%s[%d]", label, index), value); err != nil {
			return err
		}
		if index == 0 {
			continue
		}
		switch strings.Compare(values[index-1], value) {
		case 0:
			return fmt.Errorf("%s[%d] %q is duplicate", label, index, value)
		case 1:
			return fmt.Errorf("%s must be lexicographically sorted", label)
		}
	}
	return nil
}

func validateSortedNonblankStrings(label string, values []string) error {
	for index, value := range values {
		if err := validateBoundedMarkdown(fmt.Sprintf("%s[%d]", label, index), value); err != nil {
			return err
		}
		if index > 0 && strings.Compare(values[index-1], value) >= 0 {
			if values[index-1] == value {
				return fmt.Errorf("%s[%d] %q is duplicate", label, index, value)
			}
			return fmt.Errorf("%s must be lexicographically sorted", label)
		}
	}
	return nil
}

type publishImpactValidator struct{}

func (publishImpactValidator) AdmitForSeal(ctx context.Context, root *os.Root, declarations snapshot.ValidationContext) (snapshot.ValidationResult, error) {
	record, err := admitRecordForSeal[PublishImpactBody](ctx, root, publishImpactType, declarations)
	if err != nil {
		return snapshot.ValidationResult{}, err
	}
	return snapshot.ValidationResult{}, publishImpactBody(record)
}

func (publishImpactValidator) RevalidateSealed(ctx context.Context, root *os.Root, _ snapshot.ValidationContext) (snapshot.ValidationResult, error) {
	record, err := readSealedRecord[PublishImpactBody](ctx, root, publishImpactType)
	if err != nil {
		return snapshot.ValidationResult{}, err
	}
	return snapshot.ValidationResult{}, publishImpactBody(record)
}

func ReadSealedPublishImpactRecord(ctx context.Context, root *os.Root) (Record[PublishImpactBody], error) {
	record, err := readSealedRecord[PublishImpactBody](ctx, root, publishImpactType)
	if err != nil {
		return Record[PublishImpactBody]{}, err
	}
	return record, publishImpactBody(record)
}

func publishImpactBody(record Record[PublishImpactBody]) error {
	if err := validateDeclaredBody(publishImpactType, record.Subjects, record.Body); err != nil {
		return err
	}
	if err := record.Body.Validate(record.Subjects); err != nil {
		return fmt.Errorf("snapshot contracts: publish impact record: %w", err)
	}
	return nil
}
