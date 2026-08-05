// Package pullrequest defines the provider-neutral boundary for observing and
// deciding work for a single provider-native pull request.
package pullrequest

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/concourse/concourse/agent/snapshot/contracts"
)

const (
	maxProviderRepositoryBytes = 512
	maxExternalIDBytes         = 256
	maxURLBytes                = 2048
	maxRefBytes                = 512
	maxIterationBytes          = 256
	maxCursorBytes             = 4096
	maxReviewBatches           = 128
	maxThreads                 = 512
)

// Provider is deliberately closed for Day 1. Adapters normalize their native
// APIs into these provider-neutral facts rather than leaking provider shapes.
type Provider string

const (
	ProviderGitHub Provider = "github"
)

// Locator identifies a provider-native pull request. ExternalID is omitted
// only by a sealed missing observation while a PR is being created.
type Locator struct {
	Provider   Provider `json:"provider"`
	Repository string   `json:"repository"`
	ExternalID string   `json:"external_id"`
}

// Cursor is an exact, opaque provider continuation/action token. The core only
// bounds, stores, compares, and hashes it; it never parses or normalizes it.
type Cursor string

// ReviewBatch and Thread intentionally share the sealed record shapes. Their
// identifiers and ordering are therefore the same response authority later
// passed to the publisher.
type ReviewBatch = contracts.PullRequestReviewBatch
type Thread = contracts.PullRequestThread

// Observation is one normalized provider observation. ReviewBatches is a
// strict delta: it contains only ready batches not acknowledged by the Cursor
// passed to Observer.Observe. The core cannot infer this by decoding Cursor,
// because Cursor is deliberately opaque. The adapter conformance suite must
// prove that acknowledged batches are not replayed and that each new batch
// advances the cursor.
//
// Observation is not a sealed pull-request/v1 record yet: ActionFor selects
// the platform-authored trigger without asking a provider adapter to fabricate
// that trigger.
type Observation struct {
	Locator
	Cursor         Cursor                                `json:"cursor"`
	URL            string                                `json:"url"`
	State          contracts.PullRequestState            `json:"state"`
	Mergeability   contracts.PullRequestMergeability     `json:"mergeability"`
	SourceRef      string                                `json:"source_ref"`
	SourceSHA      string                                `json:"source_sha"`
	ExpectedSource *contracts.PullRequestHeadExpectation `json:"expected_source,omitempty"`
	TargetRef      string                                `json:"target_ref"`
	TargetSHA      string                                `json:"target_sha"`
	Iteration      string                                `json:"iteration"`
	ReviewBatches  []ReviewBatch                         `json:"review_batches"`
	Threads        []Thread                              `json:"threads"`
}

// Clone returns a detached copy safe to retain across adapter or caller
// mutation. It deliberately preserves opaque cursor bytes exactly.
func (observation Observation) Clone() Observation {
	cloned := observation
	if observation.ExpectedSource != nil {
		expected := *observation.ExpectedSource
		cloned.ExpectedSource = &expected
	}
	cloned.ReviewBatches = make([]ReviewBatch, len(observation.ReviewBatches))
	for index, batch := range observation.ReviewBatches {
		cloned.ReviewBatches[index] = batch
		cloned.ReviewBatches[index].ThreadIDs = append([]string(nil), batch.ThreadIDs...)
	}
	cloned.Threads = make([]Thread, len(observation.Threads))
	for index, thread := range observation.Threads {
		cloned.Threads[index] = thread
		if thread.Anchor != nil {
			anchor := *thread.Anchor
			cloned.Threads[index].Anchor = &anchor
		}
		cloned.Threads[index].Comments = append([]contracts.PullRequestComment(nil), thread.Comments...)
	}
	return cloned
}

// Validate checks the provider-neutral observation boundary. It deliberately
// does not call PullRequestBody.Validate: that record includes the
// platform-authored trigger which ActionFor is responsible for choosing.
func (observation Observation) Validate() error {
	if err := observation.Locator.validate(observation.State); err != nil {
		return err
	}
	if err := observation.Cursor.Validate(); err != nil {
		return err
	}
	if observation.State != contracts.PullRequestMissing && observation.Cursor == "" {
		return fmt.Errorf("%s pull request requires a non-empty cursor", observation.State)
	}
	if err := validateRef("source ref", observation.SourceRef); err != nil {
		return err
	}
	if err := validateRef("target ref", observation.TargetRef); err != nil {
		return err
	}
	if err := validateObjectID("target sha", observation.TargetSHA); err != nil {
		return err
	}
	if err := validateIdentifier("iteration", observation.Iteration, maxIterationBytes); err != nil {
		return err
	}
	if err := validateState(observation); err != nil {
		return err
	}
	if len(observation.Threads) > maxThreads {
		return fmt.Errorf("threads allows at most %d items", maxThreads)
	}
	if len(observation.ReviewBatches) > maxReviewBatches {
		return fmt.Errorf("review batches allows at most %d items", maxReviewBatches)
	}
	threadIDs := make(map[string]struct{}, len(observation.Threads))
	previousThreadID := ""
	for index, thread := range observation.Threads {
		if index > 0 && previousThreadID >= thread.ID {
			return fmt.Errorf("threads must be lexicographically sorted by id")
		}
		if err := thread.Validate(); err != nil {
			return fmt.Errorf("threads[%d]: %w", index, err)
		}
		threadIDs[thread.ID] = struct{}{}
		previousThreadID = thread.ID
	}
	previousBatchID := ""
	reviewIDs := make(map[string]struct{}, len(observation.ReviewBatches))
	for index, batch := range observation.ReviewBatches {
		if index > 0 && previousBatchID >= batch.ID {
			return fmt.Errorf("review batches must be lexicographically sorted by id")
		}
		if err := batch.Validate(threadIDs); err != nil {
			return fmt.Errorf("review_batches[%d]: %w", index, err)
		}
		if _, found := reviewIDs[batch.ReviewID]; found {
			return fmt.Errorf("review_batches[%d] review id %q is duplicate", index, batch.ReviewID)
		}
		reviewIDs[batch.ReviewID] = struct{}{}
		previousBatchID = batch.ID
	}
	return nil
}

func (locator Locator) validate(state contracts.PullRequestState) error {
	switch locator.Provider {
	case ProviderGitHub:
	default:
		return fmt.Errorf("provider must be github")
	}
	if err := validateBoundedText("repository", locator.Repository, maxProviderRepositoryBytes); err != nil {
		return err
	}
	if state == contracts.PullRequestMissing && locator.ExternalID == "" {
		return nil
	}
	return validateIdentifier("external id", locator.ExternalID, maxExternalIDBytes)
}

func (cursor Cursor) Validate() error {
	if !utf8.ValidString(string(cursor)) || len(cursor) > maxCursorBytes || strings.IndexByte(string(cursor), 0) >= 0 {
		return fmt.Errorf("cursor must be valid UTF-8 opaque bytes of at most %d bytes", maxCursorBytes)
	}
	return nil
}

func validateState(observation Observation) error {
	switch observation.State {
	case contracts.PullRequestMissing:
		if observation.ExternalID != "" || observation.URL != "" || observation.SourceSHA != "" {
			return fmt.Errorf("missing pull request requires empty external id, url, and source sha")
		}
		if observation.ExpectedSource == nil {
			return fmt.Errorf("missing pull request requires an expected source")
		}
		if err := observation.ExpectedSource.Validate(); err != nil {
			return fmt.Errorf("expected source: %w", err)
		}
	case contracts.PullRequestActive, contracts.PullRequestCompleted, contracts.PullRequestAbandoned:
		if err := validateURL(observation.URL); err != nil {
			return err
		}
		if err := validateObjectID("source sha", observation.SourceSHA); err != nil {
			return err
		}
		if observation.ExpectedSource != nil {
			return fmt.Errorf("%s pull request must not carry an expected source", observation.State)
		}
	default:
		return fmt.Errorf("state must be active, missing, completed, or abandoned")
	}
	switch observation.Mergeability {
	case contracts.PullRequestMergeable, contracts.PullRequestConflicted, contracts.PullRequestPolicyBlocked, contracts.PullRequestUnknown:
		return nil
	default:
		return fmt.Errorf("mergeability must be mergeable, conflicted, policy_blocked, or unknown")
	}
}

func validateBoundedText(label, value string, maximum int) error {
	if strings.TrimSpace(value) == "" || !utf8.ValidString(value) || len(value) > maximum {
		return fmt.Errorf("%s must be valid UTF-8 non-empty text of at most %d bytes", label, maximum)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("%s must not contain control characters", label)
		}
	}
	return nil
}

func validateIdentifier(label, value string, maximum int) error {
	if err := contracts.ValidateIdentifier(label, value); err != nil {
		return err
	}
	if len(value) > maximum {
		return fmt.Errorf("%s must be at most %d bytes", label, maximum)
	}
	return nil
}

func validateRef(label, value string) error {
	if strings.TrimSpace(value) == "" || !utf8.ValidString(value) || strings.ContainsAny(value, "\x00\r\n \t") || len(value) > maxRefBytes {
		return fmt.Errorf("%s must be non-empty, whitespace-free text of at most %d bytes", label, maxRefBytes)
	}
	return nil
}

func validateObjectID(label, value string) error {
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

func validateURL(value string) error {
	if len(value) > maxURLBytes {
		return fmt.Errorf("url must be at most %d bytes", maxURLBytes)
	}
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
		return fmt.Errorf("url must be an absolute http or https URL")
	}
	return nil
}

// Observer is the read-only provider seam used by the PR resource checker.
// Implementations may decode the supplied provider cursor internally, but the
// returned cursor remains opaque to callers and ReviewBatches must contain
// only the strictly newer, unacknowledged delta.
type Observer interface {
	Observe(context.Context, Locator, Cursor) (Observation, error)
}

// BranchMutation is an exact provider-native branch compare-and-swap request.
// ExpectedSource represents both an exact expected head and expected absence.
type BranchMutation struct {
	Locator           Locator
	Ref               string
	ExpectedSource    contracts.PullRequestHeadExpectation
	TargetRef         string
	ExpectedTargetSHA string
	NewSourceSHA      string
	OperationKey      string
}

type BranchResult struct {
	HeadSHA string
	Applied bool
}

type CreateRequest struct {
	Locator      Locator
	SourceRef    string
	SourceSHA    string
	TargetRef    string
	TargetSHA    string
	Title        string
	Body         string
	OperationKey string
}

type ExternalPullRequest struct {
	Locator
	URL       string
	State     contracts.PullRequestState
	SourceRef string
	SourceSHA string
	TargetRef string
	TargetSHA string
}

type StatusRequest struct {
	Locator      Locator
	TargetRef    string
	SourceSHA    string
	State        string
	Description  string
	TargetURL    string
	OperationKey string
}

type ResponseRequest struct {
	Locator      Locator
	TargetRef    string
	Batch        ReviewBatch
	Response     contracts.PullRequestResponseBody
	OperationKey string
}

type ExternalResult struct {
	OperationKey string
	ExternalID   string
	URL          string
}

// Mutator is the narrow trusted provider mutation seam. Implementations must
// use the operation key for provider-side recovery and never infer it from
// mutable pull request text.
type Mutator interface {
	CompareAndSwapBranch(context.Context, BranchMutation) (BranchResult, error)
	FindOrCreatePullRequest(context.Context, CreateRequest) (ExternalPullRequest, error)
	PublishValidationStatus(context.Context, StatusRequest) (ExternalResult, error)
	PublishReviewResponse(context.Context, ResponseRequest) (ExternalResult, error)
}
