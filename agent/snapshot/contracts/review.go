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
		return publicRecordFailure(snapshot.RecordFieldValueNotAllowed,
			"conclusion must be one of accept, changes-required, inconclusive")
	}
	if strings.TrimSpace(body.Summary) == "" {
		return publicRecordFailure(snapshot.RecordFieldMissing, "summary is required")
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
		return publicRecordFailure(snapshot.RecordConclusionInconsistent,
			"changes-required conclusion requires at least one blocking finding")
	}
	if body.Conclusion == "accept" && hasBlocking {
		return publicRecordFailure(snapshot.RecordConclusionInconsistent, "accept conclusion cannot contain a blocking finding")
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
			return publicRecordFailure(snapshot.RecordBlockingInconsistent, "observation finding cannot be blocking")
		}
	case "low", "medium":
	case "high", "critical":
		if !finding.Blocking {
			return publicRecordFailure(snapshot.RecordBlockingInconsistent, "%s finding must be blocking", finding.Severity)
		}
	default:
		return publicRecordFailure(snapshot.RecordFieldValueNotAllowed,
			"severity must be one of observation, low, medium, high, critical")
	}
	if err := ValidateIdentifier("finding category", finding.Category); err != nil {
		return err
	}
	if strings.TrimSpace(finding.Title) == "" {
		return publicRecordFailure(snapshot.RecordFieldMissing, "finding title is required")
	}
	if strings.TrimSpace(finding.Description) == "" {
		return publicRecordFailure(snapshot.RecordFieldMissing, "finding description is required")
	}
	if finding.Severity != "observation" && len(finding.Evidence) == 0 {
		return publicRecordFailure(snapshot.RecordEvidenceMissing, "non-observation finding evidence is required")
	}
	for index, anchor := range finding.Evidence {
		if err := anchor.Validate(subjects); err != nil {
			return fmt.Errorf("evidence[%d]: %w", index, err)
		}
	}
	return nil
}

type reviewValidator struct{}

func (reviewValidator) AdmitForSeal(ctx context.Context, root *os.Root, declarations snapshot.ValidationContext) (snapshot.ValidationResult, error) {
	record, err := admitRecordForSeal[ReviewBody](ctx, root, reviewType, declarations)
	if err != nil {
		return snapshot.ValidationResult{}, err
	}
	return snapshot.ValidationResult{}, reviewBody(record)
}

func (reviewValidator) RevalidateSealed(ctx context.Context, root *os.Root, _ snapshot.ValidationContext) (snapshot.ValidationResult, error) {
	record, err := readSealedRecord[ReviewBody](ctx, root, reviewType)
	if err != nil {
		return snapshot.ValidationResult{}, err
	}
	return snapshot.ValidationResult{}, reviewBody(record)
}

// DecodeSealedReviewRecord decodes one stored review/v1 record.json at the
// READ-TIME gate and runs the SAME composed body gate reviewValidator.
// RevalidateSealed runs: the declared core first, then review/v1's semantic rules.
//
// It exists because the composition is the contract, and a read site that
// assembled it by hand would be a third description of it. One already had:
// agent/projection ran the envelope gate and ReviewBody.Validate and never the
// declared schema at all, so a stored review could be projected into the reviews
// table without the declared layer ever judging it — silently, and only on that
// path. The other stored-record readers go through ReadSealedRepositoryChangeRecord
// or ReadSealedSelectionRecord, which compose both halves; this is the equivalent
// entry point for readers that hold BYTES rather than a directory.
func DecodeSealedReviewRecord(data []byte) (Record[ReviewBody], error) {
	var record Record[ReviewBody]
	if err := DecodeSealedRecord(data, reviewType, &record); err != nil {
		return Record[ReviewBody]{}, err
	}
	if err := reviewBody(record); err != nil {
		return Record[ReviewBody]{}, err
	}
	return record, nil
}

func reviewBody(record Record[ReviewBody]) error {
	if err := validateDeclaredBody(reviewType, record.Subjects, record.Body); err != nil {
		return err
	}
	if err := record.Body.Validate(record.Subjects); err != nil {
		return fmt.Errorf("snapshot contracts: record.json body: %w", err)
	}
	return nil
}
