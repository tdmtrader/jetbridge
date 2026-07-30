package contracts

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/concourse/concourse/agent/snapshot"
)

type PullRequestState string

const (
	PullRequestActive    PullRequestState = "active"
	PullRequestMissing   PullRequestState = "missing"
	PullRequestCompleted PullRequestState = "completed"
	PullRequestAbandoned PullRequestState = "abandoned"
)

type PullRequestMergeability string

const (
	PullRequestMergeable     PullRequestMergeability = "mergeable"
	PullRequestConflicted    PullRequestMergeability = "conflicted"
	PullRequestPolicyBlocked PullRequestMergeability = "policy_blocked"
	PullRequestUnknown       PullRequestMergeability = "unknown"
)

// PullRequestTrigger is the semantic observation that made this version
// actionable. It is platform-authored; adapters only normalize provider facts.
type PullRequestTrigger string

const (
	PullRequestInitialPublishTrigger PullRequestTrigger = "initial_publish"
	PullRequestReviewBatchTrigger    PullRequestTrigger = "review_batch"
	PullRequestConflictTrigger       PullRequestTrigger = "conflict"
	PullRequestFreshnessTrigger      PullRequestTrigger = "freshness"
	PullRequestCompletedTrigger      PullRequestTrigger = "completed"
	PullRequestAbandonedTrigger      PullRequestTrigger = "abandoned"
)

type PullRequestHeadExpectation struct {
	Exists bool   `json:"exists"`
	SHA    string `json:"sha,omitempty"`
}

type PullRequestBody struct {
	Provider       string                      `json:"provider"`
	Repository     string                      `json:"repository"`
	ExternalID     string                      `json:"external_id"`
	URL            string                      `json:"url"`
	State          PullRequestState            `json:"state"`
	Mergeability   PullRequestMergeability     `json:"mergeability"`
	SourceRef      string                      `json:"source_ref"`
	SourceSHA      string                      `json:"source_sha"`
	ExpectedSource *PullRequestHeadExpectation `json:"expected_source,omitempty"`
	TargetRef      string                      `json:"target_ref"`
	TargetSHA      string                      `json:"target_sha"`
	Iteration      string                      `json:"iteration"`
	Trigger        PullRequestTrigger          `json:"trigger"`
	ReviewBatches  []PullRequestReviewBatch    `json:"review_batches"`
	Threads        []PullRequestThread         `json:"threads"`
}

// PullRequestReviewBatch is one provider-complete review that can be acted on
// exactly once. ThreadIDs is the response authority for this batch.
type PullRequestReviewBatch struct {
	ID        string   `json:"id"`
	ReviewID  string   `json:"review_id"`
	CommitSHA string   `json:"commit_sha"`
	Reviewer  string   `json:"reviewer"`
	Ready     bool     `json:"ready"`
	ThreadIDs []string `json:"thread_ids"`
}

type PullRequestThread struct {
	ID        string               `json:"id"`
	Iteration string               `json:"iteration"`
	Anchor    *PullRequestAnchor   `json:"anchor,omitempty"`
	Comments  []PullRequestComment `json:"comments"`
}

type PullRequestAnchor struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

type PullRequestComment struct {
	ID        string `json:"id"`
	Author    string `json:"author"`
	Body      string `json:"body"`
	CommitSHA string `json:"commit_sha"`
}

const maxPullRequestMarkdownBytes = 32 * 1024

func (body PullRequestBody) Validate(_ []Subject) error {
	if strings.TrimSpace(body.Provider) == "" {
		return fmt.Errorf("provider is required")
	}
	if strings.TrimSpace(body.Repository) == "" {
		return fmt.Errorf("repository is required")
	}
	if err := validatePullRequestRef("source ref", body.SourceRef); err != nil {
		return err
	}
	if err := validatePullRequestRef("target ref", body.TargetRef); err != nil {
		return err
	}
	if err := validateGitObjectID("target sha", body.TargetSHA); err != nil {
		return err
	}
	if err := ValidateIdentifier("iteration", body.Iteration); err != nil {
		return err
	}
	if err := body.validateState(); err != nil {
		return err
	}
	if err := body.validateTrigger(); err != nil {
		return err
	}

	threadIDs := make([]string, len(body.Threads))
	for index, thread := range body.Threads {
		threadIDs[index] = thread.ID
		if err := thread.Validate(); err != nil {
			return fmt.Errorf("threads[%d]: %w", index, err)
		}
	}
	if err := ValidateEntityIDs("threads", threadIDs); err != nil {
		return err
	}
	knownThreads := make(map[string]struct{}, len(threadIDs))
	for _, id := range threadIDs {
		knownThreads[id] = struct{}{}
	}
	batchIDs := make([]string, len(body.ReviewBatches))
	for index, batch := range body.ReviewBatches {
		batchIDs[index] = batch.ID
		if err := batch.Validate(knownThreads); err != nil {
			return fmt.Errorf("review_batches[%d]: %w", index, err)
		}
	}
	return ValidateEntityIDs("review_batches", batchIDs)
}

func (body PullRequestBody) validateState() error {
	switch body.State {
	case PullRequestMissing:
		if body.ExternalID != "" || body.URL != "" || body.SourceSHA != "" {
			return fmt.Errorf("missing pull request requires empty external id, url, and source sha")
		}
		if body.ExpectedSource == nil {
			return fmt.Errorf("missing pull request requires an expected source")
		}
		if err := body.ExpectedSource.Validate(); err != nil {
			return fmt.Errorf("expected source: %w", err)
		}
	case PullRequestActive, PullRequestCompleted, PullRequestAbandoned:
		if err := ValidateIdentifier("external id", body.ExternalID); err != nil {
			return err
		}
		if err := validatePullRequestURL(body.URL); err != nil {
			return err
		}
		if err := validateGitObjectID("source sha", body.SourceSHA); err != nil {
			return err
		}
		if body.ExpectedSource != nil {
			return fmt.Errorf("%s pull request must not carry an expected source", body.State)
		}
	default:
		return fmt.Errorf("state must be one of active, missing, completed, abandoned")
	}
	switch body.Mergeability {
	case PullRequestMergeable, PullRequestConflicted, PullRequestPolicyBlocked, PullRequestUnknown:
		return nil
	default:
		return fmt.Errorf("mergeability must be one of mergeable, conflicted, policy_blocked, unknown")
	}
}

func (body PullRequestBody) validateTrigger() error {
	if body.State == PullRequestMissing {
		if body.Trigger != PullRequestInitialPublishTrigger {
			return fmt.Errorf("missing pull request trigger must be initial_publish")
		}
		return nil
	}
	if body.Trigger == PullRequestInitialPublishTrigger {
		return fmt.Errorf("initial_publish trigger is valid only for a missing pull request")
	}
	switch body.Trigger {
	case PullRequestReviewBatchTrigger, PullRequestConflictTrigger, PullRequestFreshnessTrigger:
		if body.State != PullRequestActive {
			return fmt.Errorf("%s trigger requires an active pull request", body.Trigger)
		}
	case PullRequestCompletedTrigger:
		if body.State != PullRequestCompleted {
			return fmt.Errorf("completed trigger requires a completed pull request")
		}
	case PullRequestAbandonedTrigger:
		if body.State != PullRequestAbandoned {
			return fmt.Errorf("abandoned trigger requires an abandoned pull request")
		}
	default:
		return fmt.Errorf("trigger must be one of initial_publish, review_batch, conflict, freshness, completed, abandoned")
	}
	return nil
}

func (expectation PullRequestHeadExpectation) Validate() error {
	if expectation.Exists {
		return validateGitObjectID("expected source sha", expectation.SHA)
	}
	if expectation.SHA != "" {
		return fmt.Errorf("expected absent source must carry an empty sha")
	}
	return nil
}

func (batch PullRequestReviewBatch) Validate(knownThreads map[string]struct{}) error {
	if err := ValidateIdentifier("batch id", batch.ID); err != nil {
		return err
	}
	if err := ValidateIdentifier("review id", batch.ReviewID); err != nil {
		return err
	}
	if err := validateGitObjectID("review commit sha", batch.CommitSHA); err != nil {
		return err
	}
	if err := ValidateIdentifier("reviewer", batch.Reviewer); err != nil {
		return err
	}
	if !batch.Ready {
		return fmt.Errorf("review batch must be ready")
	}
	if err := validateSortedIdentifiers("thread ids", batch.ThreadIDs); err != nil {
		return err
	}
	for _, threadID := range batch.ThreadIDs {
		if _, found := knownThreads[threadID]; !found {
			return fmt.Errorf("thread id %q is not present in the observation", threadID)
		}
	}
	return nil
}

func (thread PullRequestThread) Validate() error {
	if err := ValidateIdentifier("thread id", thread.ID); err != nil {
		return err
	}
	if err := ValidateIdentifier("thread iteration", thread.Iteration); err != nil {
		return err
	}
	if thread.Anchor != nil {
		if err := thread.Anchor.Validate(); err != nil {
			return fmt.Errorf("anchor: %w", err)
		}
	}
	commentIDs := make([]string, len(thread.Comments))
	for index, comment := range thread.Comments {
		commentIDs[index] = comment.ID
		if err := comment.Validate(); err != nil {
			return fmt.Errorf("comments[%d]: %w", index, err)
		}
	}
	return ValidateEntityIDs("comments", commentIDs)
}

func (anchor PullRequestAnchor) Validate() error {
	if err := validatePOSIXPath("thread anchor path", anchor.Path); err != nil {
		return err
	}
	if anchor.StartLine < 1 || anchor.EndLine < anchor.StartLine {
		return fmt.Errorf("thread anchor requires positive start line and end line at or after it")
	}
	return nil
}

func (comment PullRequestComment) Validate() error {
	if err := ValidateIdentifier("comment id", comment.ID); err != nil {
		return err
	}
	if err := ValidateIdentifier("comment author", comment.Author); err != nil {
		return err
	}
	if err := validateBoundedMarkdown("comment body", comment.Body); err != nil {
		return err
	}
	return validateGitObjectID("comment commit sha", comment.CommitSHA)
}

func validatePullRequestRef(label, value string) error {
	if strings.TrimSpace(value) == "" || strings.ContainsAny(value, "\x00\r\n \t") {
		return fmt.Errorf("%s is required and must not contain whitespace", label)
	}
	return nil
}

func validatePullRequestURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
		return fmt.Errorf("url must be an absolute http or https URL")
	}
	return nil
}

func validateGitObjectID(label, value string) error {
	if len(value) != 40 && len(value) != 64 {
		return fmt.Errorf("%s must be a full 40- or 64-character object id", label)
	}
	for _, character := range value {
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f') {
			return fmt.Errorf("%s must be lowercase hexadecimal", label)
		}
	}
	return nil
}

func validateSortedIdentifiers(label string, values []string) error {
	for index, value := range values {
		if err := ValidateIdentifier(fmt.Sprintf("%s[%d]", label, index), value); err != nil {
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

func validateBoundedMarkdown(label, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", label)
	}
	if !utf8.ValidString(value) || len(value) > maxPullRequestMarkdownBytes {
		return fmt.Errorf("%s must be valid UTF-8 and at most %d bytes", label, maxPullRequestMarkdownBytes)
	}
	return nil
}

type pullRequestValidator struct{}

func (pullRequestValidator) AdmitForSeal(ctx context.Context, root *os.Root, declarations snapshot.ValidationContext) (snapshot.ValidationResult, error) {
	record, err := admitRecordForSeal[PullRequestBody](ctx, root, pullRequestType, declarations)
	if err != nil {
		return snapshot.ValidationResult{}, err
	}
	return snapshot.ValidationResult{}, pullRequestBody(record)
}

func (pullRequestValidator) RevalidateSealed(ctx context.Context, root *os.Root, _ snapshot.ValidationContext) (snapshot.ValidationResult, error) {
	record, err := readSealedRecord[PullRequestBody](ctx, root, pullRequestType)
	if err != nil {
		return snapshot.ValidationResult{}, err
	}
	return snapshot.ValidationResult{}, pullRequestBody(record)
}

func ReadSealedPullRequestRecord(ctx context.Context, root *os.Root) (Record[PullRequestBody], error) {
	record, err := readSealedRecord[PullRequestBody](ctx, root, pullRequestType)
	if err != nil {
		return Record[PullRequestBody]{}, err
	}
	return record, pullRequestBody(record)
}

func pullRequestBody(record Record[PullRequestBody]) error {
	if err := validateDeclaredBody(pullRequestType, record.Subjects, record.Body); err != nil {
		return err
	}
	if err := record.Body.Validate(record.Subjects); err != nil {
		return fmt.Errorf("snapshot contracts: pull request record: %w", err)
	}
	return nil
}
