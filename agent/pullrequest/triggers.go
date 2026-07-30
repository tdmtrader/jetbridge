package pullrequest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/concourse/concourse/atc"
)

// ActionKind is the closed set of monitor actions. initial_publish is a
// platform-authored creation trigger, never an action emitted by monitoring.
type ActionKind string

const (
	ActionReviewBatch ActionKind = "review_batch"
	ActionConflict    ActionKind = "conflict"
	ActionFreshness   ActionKind = "freshness"
	ActionCompleted   ActionKind = "completed"
	ActionAbandoned   ActionKind = "abandoned"
)

// Action is an immutable decision from one observation and acknowledged policy
// state. Observation is detached so a provider adapter cannot mutate a queued
// decision after ActionFor returns.
type Action struct {
	Kind            ActionKind  `json:"kind"`
	Digest          string      `json:"digest"`
	Cursor          Cursor      `json:"cursor"`
	FreshnessBucket int64       `json:"freshness_bucket,omitempty"`
	Observation     Observation `json:"observation"`
}

// TriggerPolicy is projected exclusively from acknowledged binding state. It
// deliberately has no previous-resource-version fields.
type TriggerPolicy struct {
	Now                time.Time
	PollInterval       time.Duration
	FreshnessInterval  time.Duration
	LastCursor         Cursor
	LastTargetSHA      string
	LastReconciledAt   time.Time
	ActiveActionDigest string
}

// ActionFor selects the next semantic monitor action. An active digest only
// suppresses exactly the same canonical action; unrelated work cannot hide a
// later provider observation.
func ActionFor(observation Observation, policy TriggerPolicy) (Action, bool, error) {
	if err := observation.Validate(); err != nil {
		return Action{}, false, fmt.Errorf("validate observation: %w", err)
	}
	if err := policy.validate(); err != nil {
		return Action{}, false, fmt.Errorf("validate trigger policy: %w", err)
	}

	var kind ActionKind
	var freshnessBucket int64
	switch observation.State {
	case contracts.PullRequestMissing:
		return Action{}, false, nil
	case contracts.PullRequestCompleted:
		if observation.Cursor == policy.LastCursor {
			return Action{}, false, nil
		}
		kind = ActionCompleted
	case contracts.PullRequestAbandoned:
		if observation.Cursor == policy.LastCursor {
			return Action{}, false, nil
		}
		kind = ActionAbandoned
	case contracts.PullRequestActive:
		switch {
		case observation.Cursor != policy.LastCursor && len(observation.ReviewBatches) > 0:
			// ReviewBatches is an adapter-enforced unacknowledged delta. Cursor
			// equality is the only core-level fact available because provider
			// cursor structure is intentionally opaque.
			kind = ActionReviewBatch
		case observation.Mergeability == contracts.PullRequestConflicted && observation.Cursor != policy.LastCursor:
			// The adapter's opaque cursor is the canonical conflict signature.
			kind = ActionConflict
		case freshnessDue(observation, policy):
			kind = ActionFreshness
			freshnessBucket = freshnessBucketFor(policy)
		default:
			return Action{}, false, nil
		}
	default:
		return Action{}, false, fmt.Errorf("unsupported pull request state %q", observation.State)
	}

	action := Action{Kind: kind, Cursor: observation.Cursor, FreshnessBucket: freshnessBucket, Observation: observation.Clone()}
	digest, err := actionDigest(action)
	if err != nil {
		return Action{}, false, err
	}
	action.Digest = digest
	if action.Digest == policy.ActiveActionDigest {
		return Action{}, false, nil
	}
	return action, true, nil
}

func (policy TriggerPolicy) validate() error {
	if policy.Now.IsZero() {
		return fmt.Errorf("now is required")
	}
	if policy.PollInterval <= 0 {
		return fmt.Errorf("poll interval must be positive")
	}
	if policy.FreshnessInterval <= 0 || policy.FreshnessInterval%policy.PollInterval != 0 {
		return fmt.Errorf("freshness interval must be positive and divisible by poll interval")
	}
	if err := policy.LastCursor.Validate(); err != nil {
		return fmt.Errorf("last cursor: %w", err)
	}
	if policy.LastTargetSHA != "" {
		if err := validateObjectID("last target sha", policy.LastTargetSHA); err != nil {
			return err
		}
	}
	if policy.ActiveActionDigest != "" && !isDigest(policy.ActiveActionDigest) {
		return fmt.Errorf("active action digest must be a sha256 digest")
	}
	return nil
}

func freshnessDue(observation Observation, policy TriggerPolicy) bool {
	return policy.LastTargetSHA != "" && observation.TargetSHA != policy.LastTargetSHA &&
		!policy.LastReconciledAt.IsZero() && !policy.Now.Before(policy.LastReconciledAt.Add(policy.FreshnessInterval))
}

func freshnessBucketFor(policy TriggerPolicy) int64 {
	// Use the first due deadline anchored in acknowledged binding state. Now
	// determines only whether the action is due; it cannot continuously mint a
	// new identity while source, target, cursor, and acknowledgement are
	// unchanged.
	return policy.LastReconciledAt.UTC().Add(policy.FreshnessInterval).UnixNano()
}

func actionDigest(action Action) (string, error) {
	identity := struct {
		Locator         Locator    `json:"locator"`
		SourceRef       string     `json:"source_ref"`
		SourceSHA       string     `json:"source_sha"`
		TargetRef       string     `json:"target_ref"`
		TargetSHA       string     `json:"target_sha"`
		Kind            ActionKind `json:"kind"`
		Cursor          Cursor     `json:"cursor"`
		FreshnessBucket int64      `json:"freshness_bucket,omitempty"`
	}{action.Observation.Locator, action.Observation.SourceRef, action.Observation.SourceSHA, action.Observation.TargetRef, action.Observation.TargetSHA, action.Kind, action.Cursor, action.FreshnessBucket}
	canonical, err := atc.CanonicalJSON(identity)
	if err != nil {
		return "", fmt.Errorf("canonical action identity: %w", err)
	}
	sum := sha256.Sum256(append([]byte("concourse.pull-request-action/v1\x00"), canonical...))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func isDigest(value string) bool {
	if len(value) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, character := range value[len("sha256:"):] {
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}
