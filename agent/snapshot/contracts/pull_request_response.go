package contracts

import (
	"context"
	"fmt"
	"os"

	"github.com/concourse/concourse/agent/snapshot"
)

type PullRequestResponseBody struct {
	BatchID string                      `json:"batch_id"`
	Summary string                      `json:"summary"`
	Replies []PullRequestThreadResponse `json:"replies"`
}

type PullRequestThreadResponse struct {
	ThreadID string `json:"thread_id"`
	Body     string `json:"body"`
}

// Validate judges intrinsic response shape only. Authority to name a batch or a
// thread belongs to the reopened pull-request/v1 observation and is deliberately
// checked by ValidatePullRequestResponseAgainst.
func (body PullRequestResponseBody) Validate(_ []Subject) error {
	return body.validateIntrinsic()
}

func (body PullRequestResponseBody) validateIntrinsic() error {
	if err := ValidateIdentifier("batch id", body.BatchID); err != nil {
		return err
	}
	if err := validateBoundedMarkdown("summary", body.Summary); err != nil {
		return err
	}
	threadIDs := make([]string, len(body.Replies))
	for index, reply := range body.Replies {
		threadIDs[index] = reply.ThreadID
		if err := reply.Validate(); err != nil {
			return fmt.Errorf("replies[%d]: %w", index, err)
		}
	}
	return validateSortedIdentifiers("replies", threadIDs)
}

func (response PullRequestThreadResponse) Validate() error {
	if err := ValidateIdentifier("thread id", response.ThreadID); err != nil {
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
	if err := validateDeclaredBody(pullRequestResponseType, record.Subjects, record.Body); err != nil {
		return err
	}
	if err := validatePullRequestResponseSubjects(record.Subjects); err != nil {
		return fmt.Errorf("snapshot contracts: pull request response record: %w", err)
	}
	if err := record.Body.validateIntrinsic(); err != nil {
		return fmt.Errorf("snapshot contracts: pull request response record: %w", err)
	}
	return nil
}

func validatePullRequestResponseSubjects(subjects []Subject) error {
	if len(subjects) != 1 || subjects[0].Role != SubjectRolePrimary || subjects[0].Type != pullRequestType {
		return fmt.Errorf("record requires exactly one primary pull-request/v1 subject")
	}
	return nil
}
