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

const (
	maxPullRequestMarkdownBytes       = 32 * 1024
	maxPullRequestProviderBytes       = 64
	maxPullRequestRepositoryBytes     = 512
	maxPullRequestURLBytes            = 2048
	maxPullRequestRefBytes            = 512
	maxPullRequestIdentifierBytes     = 256
	maxPullRequestPathBytes           = 1024
	maxPullRequestReviewBatches       = 128
	maxPullRequestThreads             = 512
	maxPullRequestComments            = 256
	maxPullRequestThreadIDs           = 512
	maxPullRequestReplies             = 512
	maxPublishImpactChangedFiles      = 1024
	maxPublishImpactRules             = 512
	maxPublishImpactValidationChanges = 512
	maxPublishImpactReasons           = 512
)

type recordValidationPolicy struct {
	enforceRevisionThreeBounds bool
}

var currentRecordValidationPolicy = recordValidationPolicy{enforceRevisionThreeBounds: true}

func recordValidationPolicyFor(ref snapshot.TypeRef, schema snapshot.Digest) (recordValidationPolicy, error) {
	if schema == "" {
		return currentRecordValidationPolicy, nil
	}
	revision, found := SchemaRevisionFor(ref, schema)
	if !found {
		return recordValidationPolicy{}, fmt.Errorf("%q carries an unknown schema digest %q", ref, schema)
	}
	return recordValidationPolicy{enforceRevisionThreeBounds: revision >= 3}, nil
}

func (body PullRequestBody) Validate(_ []Subject) error {
	return body.validate(currentRecordValidationPolicy)
}

func (body PullRequestBody) validate(policy recordValidationPolicy) error {
	if err := validateRequiredBoundedText("provider", body.Provider, maxPullRequestProviderBytes, policy); err != nil {
		return err
	}
	if err := validateRequiredBoundedText("repository", body.Repository, maxPullRequestRepositoryBytes, policy); err != nil {
		return err
	}
	if err := validatePullRequestRef("source ref", body.SourceRef, policy); err != nil {
		return err
	}
	if err := validatePullRequestRef("target ref", body.TargetRef, policy); err != nil {
		return err
	}
	if err := validateGitObjectID("target sha", body.TargetSHA); err != nil {
		return err
	}
	if err := validatePullRequestIdentifier("iteration", body.Iteration, policy); err != nil {
		return err
	}
	if err := body.validateState(policy); err != nil {
		return err
	}
	if err := body.validateTrigger(); err != nil {
		return err
	}

	if err := validateMaxItems("threads", len(body.Threads), maxPullRequestThreads, policy); err != nil {
		return err
	}
	threadIDs := make([]string, len(body.Threads))
	for index, thread := range body.Threads {
		threadIDs[index] = thread.ID
		if err := thread.validate(policy); err != nil {
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
	if err := validateMaxItems("review batches", len(body.ReviewBatches), maxPullRequestReviewBatches, policy); err != nil {
		return err
	}
	batchIDs := make([]string, len(body.ReviewBatches))
	for index, batch := range body.ReviewBatches {
		batchIDs[index] = batch.ID
		if err := batch.validate(knownThreads, policy); err != nil {
			return fmt.Errorf("review_batches[%d]: %w", index, err)
		}
	}
	return ValidateEntityIDs("review_batches", batchIDs)
}

func (body PullRequestBody) validateState(policy recordValidationPolicy) error {
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
		if err := validatePullRequestIdentifier("external id", body.ExternalID, policy); err != nil {
			return err
		}
		if err := validatePullRequestURL(body.URL, policy); err != nil {
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
	return batch.validate(knownThreads, currentRecordValidationPolicy)
}

func (batch PullRequestReviewBatch) validate(knownThreads map[string]struct{}, policy recordValidationPolicy) error {
	if err := validatePullRequestIdentifier("batch id", batch.ID, policy); err != nil {
		return err
	}
	if err := validatePullRequestIdentifier("review id", batch.ReviewID, policy); err != nil {
		return err
	}
	if err := validateGitObjectID("review commit sha", batch.CommitSHA); err != nil {
		return err
	}
	if err := validatePullRequestIdentifier("reviewer", batch.Reviewer, policy); err != nil {
		return err
	}
	if !batch.Ready {
		return fmt.Errorf("review batch must be ready")
	}
	if err := validateMaxItems("thread ids", len(batch.ThreadIDs), maxPullRequestThreadIDs, policy); err != nil {
		return err
	}
	if err := validateSortedIdentifiers("thread ids", batch.ThreadIDs, policy); err != nil {
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
	return thread.validate(currentRecordValidationPolicy)
}

func (thread PullRequestThread) validate(policy recordValidationPolicy) error {
	if err := validatePullRequestIdentifier("thread id", thread.ID, policy); err != nil {
		return err
	}
	if err := validatePullRequestIdentifier("thread iteration", thread.Iteration, policy); err != nil {
		return err
	}
	if thread.Anchor != nil {
		if err := thread.Anchor.validate(policy); err != nil {
			return fmt.Errorf("anchor: %w", err)
		}
	}
	if err := validateMaxItems("comments", len(thread.Comments), maxPullRequestComments, policy); err != nil {
		return err
	}
	commentIDs := make([]string, len(thread.Comments))
	for index, comment := range thread.Comments {
		commentIDs[index] = comment.ID
		if err := comment.validate(policy); err != nil {
			return fmt.Errorf("comments[%d]: %w", index, err)
		}
	}
	return ValidateEntityIDs("comments", commentIDs)
}

func (anchor PullRequestAnchor) Validate() error {
	return anchor.validate(currentRecordValidationPolicy)
}

func (anchor PullRequestAnchor) validate(policy recordValidationPolicy) error {
	if err := validatePullRequestPath("thread anchor path", anchor.Path, policy); err != nil {
		return err
	}
	if anchor.StartLine < 1 || anchor.EndLine < anchor.StartLine {
		return fmt.Errorf("thread anchor requires positive start line and end line at or after it")
	}
	return nil
}

func (comment PullRequestComment) Validate() error {
	return comment.validate(currentRecordValidationPolicy)
}

func (comment PullRequestComment) validate(policy recordValidationPolicy) error {
	if err := validatePullRequestIdentifier("comment id", comment.ID, policy); err != nil {
		return err
	}
	if err := validatePullRequestIdentifier("comment author", comment.Author, policy); err != nil {
		return err
	}
	if err := validateBoundedMarkdown("comment body", comment.Body); err != nil {
		return err
	}
	return validateGitObjectID("comment commit sha", comment.CommitSHA)
}

func validatePullRequestRef(label, value string, policy recordValidationPolicy) error {
	if strings.TrimSpace(value) == "" || strings.ContainsAny(value, "\x00\r\n \t") {
		return fmt.Errorf("%s is required and must not contain whitespace", label)
	}
	if policy.enforceRevisionThreeBounds && len(value) > maxPullRequestRefBytes {
		return fmt.Errorf("%s must be at most %d bytes", label, maxPullRequestRefBytes)
	}
	return nil
}

func validatePullRequestURL(value string, policy recordValidationPolicy) error {
	if policy.enforceRevisionThreeBounds && len(value) > maxPullRequestURLBytes {
		return fmt.Errorf("url must be at most %d bytes", maxPullRequestURLBytes)
	}
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

func validateSortedIdentifiers(label string, values []string, policy recordValidationPolicy) error {
	for index, value := range values {
		if err := validatePullRequestIdentifier(fmt.Sprintf("%s[%d]", label, index), value, policy); err != nil {
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

func validatePullRequestIdentifier(label, value string, policy recordValidationPolicy) error {
	if err := ValidateIdentifier(label, value); err != nil {
		return err
	}
	if policy.enforceRevisionThreeBounds && len(value) > maxPullRequestIdentifierBytes {
		return fmt.Errorf("%s must be at most %d bytes", label, maxPullRequestIdentifierBytes)
	}
	return nil
}

func validatePullRequestPath(label, value string, policy recordValidationPolicy) error {
	if err := validatePOSIXPath(label, value); err != nil {
		return err
	}
	if policy.enforceRevisionThreeBounds && len(value) > maxPullRequestPathBytes {
		return fmt.Errorf("%s must be at most %d bytes", label, maxPullRequestPathBytes)
	}
	return nil
}

func validateRequiredBoundedText(label, value string, maximum int, policy recordValidationPolicy) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", label)
	}
	if !utf8.ValidString(value) || policy.enforceRevisionThreeBounds && len(value) > maximum {
		return fmt.Errorf("%s must be valid UTF-8 and at most %d bytes", label, maximum)
	}
	return nil
}

func validateMaxItems(label string, count, maximum int, policy recordValidationPolicy) error {
	if policy.enforceRevisionThreeBounds && count > maximum {
		return fmt.Errorf("%s allows at most %d items", label, maximum)
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
	policy, err := recordValidationPolicyFor(pullRequestType, record.Schema)
	if err != nil {
		return fmt.Errorf("snapshot contracts: pull request record: %w", err)
	}
	if err := record.Body.validate(policy); err != nil {
		return fmt.Errorf("snapshot contracts: pull request record: %w", err)
	}
	return nil
}
