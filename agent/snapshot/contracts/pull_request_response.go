package contracts

import (
	"context"
	"fmt"
	"os"

	"github.com/concourse/concourse/agent/snapshot"
)

type PullRequestResponseKind string

const (
	// PullRequestResponseReviewResponse carries response authority derived
	// from one exact completed review batch.
	PullRequestResponseReviewResponse PullRequestResponseKind = "review_response"
	// PullRequestResponseNoResponse is semantic absence. It deliberately
	// carries no batch, summary, or reply that a provider adapter could emit.
	PullRequestResponseNoResponse PullRequestResponseKind = "no_response"
)

type PullRequestResponseBody struct {
	Kind    PullRequestResponseKind     `json:"kind,omitempty"`
	BatchID string                      `json:"batch_id,omitempty"`
	Summary string                      `json:"summary,omitempty"`
	Replies []PullRequestThreadResponse `json:"replies,omitempty"`
}

type PullRequestThreadResponse struct {
	ThreadID string `json:"thread_id"`
	Body     string `json:"body"`
}

// Validate judges intrinsic response shape only. Authority to name a batch or a
// thread belongs to the reopened pull-request/v1 observation and is deliberately
// checked by ValidatePullRequestResponseAgainst.
func (body PullRequestResponseBody) Validate(_ []Subject) error {
	return body.validate(currentRecordValidationPolicy)
}

func (body PullRequestResponseBody) validate(policy recordValidationPolicy) error {
	return body.validateIntrinsicWithPolicy(policy)
}

func (body PullRequestResponseBody) validateForRevision(
	policy recordValidationPolicy,
	revision int,
) error {
	if revision < 4 && body.Kind != "" {
		return fmt.Errorf(
			"body/kind: schema revision %d requires the legacy omitted kind",
			revision,
		)
	}
	if revision >= 4 && body.Kind == "" {
		return fmt.Errorf(
			"body/kind: schema revision %d requires an explicit kind",
			revision,
		)
	}
	return body.validateIntrinsicWithPolicy(policy)
}

func (body PullRequestResponseBody) validateIntrinsic() error {
	return body.validateIntrinsicWithPolicy(currentRecordValidationPolicy)
}

func (body PullRequestResponseBody) validateIntrinsicWithPolicy(policy recordValidationPolicy) error {
	switch body.Kind {
	case PullRequestResponseNoResponse:
		if body.BatchID != "" || body.Summary != "" || len(body.Replies) != 0 {
			return fmt.Errorf("no-response must not carry a batch, summary, or replies")
		}
		return nil
	case "", PullRequestResponseReviewResponse:
		// An omitted kind is the legacy revision 1-3 wire image. It remains a
		// review response so stored records keep their original meaning.
	default:
		return fmt.Errorf("kind must be one of review_response, no_response")
	}
	if err := validatePullRequestIdentifier("batch id", body.BatchID, policy); err != nil {
		return err
	}
	if err := validateBoundedMarkdown("summary", body.Summary); err != nil {
		return err
	}
	if err := validateMaxItems("replies", len(body.Replies), maxPullRequestReplies, policy); err != nil {
		return err
	}
	threadIDs := make([]string, len(body.Replies))
	for index, reply := range body.Replies {
		threadIDs[index] = reply.ThreadID
		if err := reply.validate(policy); err != nil {
			return fmt.Errorf("replies[%d]: %w", index, err)
		}
	}
	return validateSortedIdentifiers("replies", threadIDs, policy)
}

func (response PullRequestThreadResponse) Validate() error {
	return response.validate(currentRecordValidationPolicy)
}

func (response PullRequestThreadResponse) validate(policy recordValidationPolicy) error {
	if err := validatePullRequestIdentifier("thread id", response.ThreadID, policy); err != nil {
		return err
	}
	return validateBoundedMarkdown("reply body", response.Body)
}

// ValidatePullRequestResponseAgainst applies the authority that generic response
// validation intentionally cannot see: every reply must name a thread included
// by the exact completed review batch in the reopened observation.
func ValidatePullRequestResponseAgainst(response PullRequestResponseBody, observation PullRequestBody) error {
	if err := response.validateIntrinsic(); err != nil {
		return err
	}
	if err := observation.Validate(nil); err != nil {
		return fmt.Errorf("pull request observation: %w", err)
	}
	if response.Kind == PullRequestResponseNoResponse {
		if observation.Trigger == PullRequestReviewBatchTrigger {
			return fmt.Errorf("review batch requires an authorized review response")
		}
		return nil
	}
	if observation.Trigger != PullRequestReviewBatchTrigger {
		return fmt.Errorf(
			"%s observation does not authorize a review response",
			observation.Trigger,
		)
	}
	for _, batch := range observation.ReviewBatches {
		if batch.ID != response.BatchID {
			continue
		}
		allowed := make(map[string]struct{}, len(batch.ThreadIDs))
		for _, threadID := range batch.ThreadIDs {
			allowed[threadID] = struct{}{}
		}
		for _, reply := range response.Replies {
			if _, found := allowed[reply.ThreadID]; !found {
				return fmt.Errorf("reply thread %q is not authorized by batch %q", reply.ThreadID, response.BatchID)
			}
		}
		return nil
	}
	return fmt.Errorf("response batch %q is not present in the pull request observation", response.BatchID)
}

type pullRequestResponseValidator struct{}

func (pullRequestResponseValidator) AdmitForSeal(ctx context.Context, root *os.Root, declarations snapshot.ValidationContext) (snapshot.ValidationResult, error) {
	record, err := admitRecordForSeal[PullRequestResponseBody](ctx, root, pullRequestResponseType, declarations)
	if err != nil {
		return snapshot.ValidationResult{}, err
	}
	return snapshot.ValidationResult{}, pullRequestResponseBody(record)
}

func (pullRequestResponseValidator) RevalidateSealed(ctx context.Context, root *os.Root, _ snapshot.ValidationContext) (snapshot.ValidationResult, error) {
	record, err := readSealedRecord[PullRequestResponseBody](ctx, root, pullRequestResponseType)
	if err != nil {
		return snapshot.ValidationResult{}, err
	}
	return snapshot.ValidationResult{}, pullRequestResponseBody(record)
}

func ReadSealedPullRequestResponseRecord(ctx context.Context, root *os.Root) (Record[PullRequestResponseBody], error) {
	record, err := readSealedRecord[PullRequestResponseBody](ctx, root, pullRequestResponseType)
	if err != nil {
		return Record[PullRequestResponseBody]{}, err
	}
	return record, pullRequestResponseBody(record)
}

func pullRequestResponseBody(record Record[PullRequestResponseBody]) error {
	revision, found := SchemaRevisionFor(pullRequestResponseType, record.Schema)
	if !found {
		return fmt.Errorf(
			"snapshot contracts: pull request response record: schema %q has no accepted revision",
			record.Schema,
		)
	}
	if err := validateDeclaredBody(
		pullRequestResponseType,
		record.Subjects,
		pullRequestResponseDeclaredBody(record.Body, revision),
	); err != nil {
		return err
	}
	if err := validatePullRequestResponseSubjects(record.Subjects); err != nil {
		return fmt.Errorf("snapshot contracts: pull request response record: %w", err)
	}
	policy, err := recordValidationPolicyFor(pullRequestResponseType, record.Schema)
	if err != nil {
		return fmt.Errorf("snapshot contracts: pull request response record: %w", err)
	}
	if err := record.Body.validateForRevision(policy, revision); err != nil {
		return fmt.Errorf("snapshot contracts: pull request response record: %w", err)
	}
	return nil
}

func pullRequestResponseDeclaredBody(body PullRequestResponseBody, revision int) PullRequestResponseBody {
	if revision < 4 && body.Kind == "" {
		body.Kind = PullRequestResponseReviewResponse
	}
	return body
}

func validatePullRequestResponseSubjects(subjects []Subject) error {
	if len(subjects) != 1 || subjects[0].Role != SubjectRolePrimary || subjects[0].Type != pullRequestType {
		return fmt.Errorf("record requires exactly one primary pull-request/v1 subject")
	}
	return nil
}
