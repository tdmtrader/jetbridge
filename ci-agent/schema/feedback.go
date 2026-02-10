package schema

import (
	"encoding/json"
	"fmt"
)

// Verdict represents a human's assessment of an agent finding.
type Verdict string

const (
	VerdictAccurate         Verdict = "accurate"
	VerdictFalsePositive    Verdict = "false_positive"
	VerdictNoisy            Verdict = "noisy"
	VerdictOverlyStrict     Verdict = "overly_strict"
	VerdictPartiallyCorrect Verdict = "partially_correct"
	VerdictMissedContext    Verdict = "missed_context"
)

var validVerdicts = map[Verdict]bool{
	VerdictAccurate:         true,
	VerdictFalsePositive:    true,
	VerdictNoisy:            true,
	VerdictOverlyStrict:     true,
	VerdictPartiallyCorrect: true,
	VerdictMissedContext:    true,
}

// FeedbackSource identifies how feedback was collected.
type FeedbackSource string

const (
	SourceInteractive          FeedbackSource = "interactive"
	SourceInferredConversation FeedbackSource = "inferred_conversation"
	SourceInferredOutcome      FeedbackSource = "inferred_outcome"
)

var validSources = map[FeedbackSource]bool{
	SourceInteractive:          true,
	SourceInferredConversation: true,
	SourceInferredOutcome:      true,
}

// ReviewRef identifies a specific review session.
type ReviewRef struct {
	Repo     string `json:"repo"`
	Commit   string `json:"commit"`
	ReviewTS string `json:"review_timestamp"`
}

// ConversationMessage captures one turn of the feedback conversation.
type ConversationMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// FeedbackRecord captures a human's verdict on a single agent finding.
type FeedbackRecord struct {
	ID              string                `json:"id,omitempty"`
	ReviewRef       ReviewRef             `json:"review_ref"`
	FindingID       string                `json:"finding_id"`
	FindingType     string                `json:"finding_type"`
	FindingSnapshot json.RawMessage       `json:"finding_snapshot"`
	Verdict         Verdict               `json:"verdict"`
	Confidence      float64               `json:"confidence"`
	Notes           string                `json:"notes"`
	Conversation    []ConversationMessage  `json:"conversation"`
	Reviewer        string                `json:"reviewer"`
	Source          FeedbackSource         `json:"source"`
	Timestamp       string                `json:"timestamp,omitempty"`
}

// Validate checks that all required FeedbackRecord fields are present and valid.
func (r *FeedbackRecord) Validate() error {
	if r.ReviewRef.Repo == "" {
		return fmt.Errorf("review_ref.repo is required")
	}
	if r.ReviewRef.Commit == "" {
		return fmt.Errorf("review_ref.commit is required")
	}
	if r.FindingID == "" {
		return fmt.Errorf("finding_id is required")
	}
	if r.Verdict == "" {
		return fmt.Errorf("verdict is required")
	}
	if len(r.FindingSnapshot) == 0 {
		return fmt.Errorf("finding_snapshot is required")
	}
	if !validVerdicts[r.Verdict] {
		return fmt.Errorf("invalid verdict %q", r.Verdict)
	}
	if r.Source != "" && !validSources[r.Source] {
		return fmt.Errorf("invalid source %q", r.Source)
	}
	return nil
}

// VerdictSummary aggregates feedback stats.
type VerdictSummary struct {
	Total         int            `json:"total"`
	AccuracyRate  float64        `json:"accuracy_rate"`
	FPRate        float64        `json:"false_positive_rate"`
	ByVerdict     map[string]int `json:"by_verdict"`
	ByCategory    map[string]int `json:"by_category"`
	BySeverity    map[string]int `json:"by_severity"`
}
