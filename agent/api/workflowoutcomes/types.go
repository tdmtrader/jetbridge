// Package workflowoutcomes records the disposition and delivery result of an
// exact durable workflow-run output. It is deliberately independent from the
// legacy one-outcome-per-ticket model.
package workflowoutcomes

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/concourse/concourse/agent/snapshot"
)

var (
	ErrInvalidOutcome  = errors.New("workflow outcome: invalid")
	ErrOutcomeNotFound = errors.New("workflow outcome: not found")
)

type Disposition string

const (
	DispositionAccepted  Disposition = "accepted"
	DispositionRejected  Disposition = "rejected"
	DispositionMerged    Disposition = "merged"
	DispositionAbandoned Disposition = "abandoned"
)

func (disposition Disposition) Validate() error {
	switch disposition {
	case DispositionAccepted, DispositionRejected, DispositionMerged, DispositionAbandoned:
		return nil
	default:
		return fmt.Errorf("%w: disposition must be accepted, rejected, merged, or abandoned", ErrInvalidOutcome)
	}
}

type PublicationState string

const (
	PublicationNotRequested PublicationState = "not_requested"
	PublicationPending      PublicationState = "pending"
	PublicationPublished    PublicationState = "published"
	PublicationFailed       PublicationState = "failed"
)

func (state PublicationState) Validate() error {
	switch state {
	case PublicationNotRequested, PublicationPending, PublicationPublished, PublicationFailed:
		return nil
	default:
		return fmt.Errorf("%w: publication_state must be not_requested, pending, published, or failed", ErrInvalidOutcome)
	}
}

type Outcome struct {
	WorkflowRunID          snapshot.WorkflowRunID `json:"workflow_run_id"`
	OutputSnapshotID       snapshot.SnapshotID    `json:"output_snapshot_id"`
	Disposition            Disposition            `json:"disposition"`
	PublicationState       PublicationState       `json:"publication_state"`
	PublicationID          *snapshot.DatabaseID   `json:"publication_id,omitempty"`
	HumanModified          bool                   `json:"human_modified"`
	ModificationSnapshotID *snapshot.SnapshotID   `json:"modification_snapshot_id,omitempty"`
	InterventionCount      int                    `json:"intervention_count"`
	Labels                 []string               `json:"labels"`
	Actor                  string                 `json:"actor"`
	Revision               int64                  `json:"revision"`
	AuditedAt              time.Time              `json:"audited_at"`
}

func (outcome Outcome) Validate() error {
	if err := outcome.WorkflowRunID.Validate(); err != nil {
		return fmt.Errorf("%w: workflow_run_id: %v", ErrInvalidOutcome, err)
	}
	if err := outcome.OutputSnapshotID.Validate(); err != nil {
		return fmt.Errorf("%w: output_snapshot_id: %v", ErrInvalidOutcome, err)
	}
	if err := outcome.Disposition.Validate(); err != nil {
		return err
	}
	if err := outcome.PublicationState.Validate(); err != nil {
		return err
	}
	switch outcome.PublicationState {
	case PublicationNotRequested:
		if outcome.PublicationID != nil {
			return fmt.Errorf("%w: not_requested publication state cannot carry evidence", ErrInvalidOutcome)
		}
	case PublicationPublished:
		if outcome.PublicationID == nil {
			return fmt.Errorf("%w: published state requires exact publication evidence", ErrInvalidOutcome)
		}
	default:
		return fmt.Errorf("%w: publication state is not an authoritative terminal result", ErrInvalidOutcome)
	}
	if outcome.PublicationID != nil {
		if *outcome.PublicationID <= 0 {
			return fmt.Errorf("%w: publication_id must be positive", ErrInvalidOutcome)
		}
	}
	if outcome.ModificationSnapshotID != nil {
		if err := outcome.ModificationSnapshotID.Validate(); err != nil {
			return fmt.Errorf("%w: modification_snapshot_id: %v", ErrInvalidOutcome, err)
		}
		if !outcome.HumanModified {
			return fmt.Errorf("%w: modification_snapshot_id requires human_modified", ErrInvalidOutcome)
		}
	}
	if outcome.InterventionCount < 0 {
		return fmt.Errorf("%w: intervention_count must be nonnegative", ErrInvalidOutcome)
	}
	seenLabels := make(map[string]struct{}, len(outcome.Labels))
	for index, label := range outcome.Labels {
		if !validBoundedText(label, 64) {
			return fmt.Errorf("%w: labels[%d] is invalid", ErrInvalidOutcome, index)
		}
		if _, found := seenLabels[label]; found {
			return fmt.Errorf("%w: label %q is duplicate", ErrInvalidOutcome, label)
		}
		seenLabels[label] = struct{}{}
	}
	if !validBoundedText(outcome.Actor, 256) {
		return fmt.Errorf("%w: actor is invalid", ErrInvalidOutcome)
	}
	if outcome.Revision <= 0 {
		return fmt.Errorf("%w: revision must be positive", ErrInvalidOutcome)
	}
	if outcome.AuditedAt.IsZero() {
		return fmt.Errorf("%w: audited_at is required", ErrInvalidOutcome)
	}
	return nil
}

func (outcome Outcome) Clone() Outcome {
	outcome.PublicationID = cloneInt64(outcome.PublicationID)
	outcome.ModificationSnapshotID = cloneSnapshotID(outcome.ModificationSnapshotID)
	outcome.Labels = append([]string(nil), outcome.Labels...)
	return outcome
}

type RecordRequest struct {
	WorkflowRunID          snapshot.WorkflowRunID
	OutputSnapshotID       snapshot.SnapshotID
	Disposition            Disposition
	PublicationState       PublicationState
	PublicationID          *snapshot.DatabaseID
	HumanModified          bool
	ModificationSnapshotID *snapshot.SnapshotID
	InterventionCount      int
	Labels                 []string
	Actor                  string
}

func (request RecordRequest) clone() RecordRequest {
	request.PublicationID = cloneInt64(request.PublicationID)
	request.ModificationSnapshotID = cloneSnapshotID(request.ModificationSnapshotID)
	request.Labels = append([]string(nil), request.Labels...)
	return request
}

// ModifyRequest is the member-authored portion of an outcome. Publication
// evidence is deliberately absent: only the durable publisher store may link
// an exact succeeded agent_publications row to an outcome.
type ModifyRequest struct {
	WorkflowRunID          snapshot.WorkflowRunID
	OutputSnapshotID       snapshot.SnapshotID
	Disposition            Disposition
	HumanModified          bool
	ModificationSnapshotID *snapshot.SnapshotID
	Labels                 []string
	Actor                  string
}

func (request ModifyRequest) clone() ModifyRequest {
	request.ModificationSnapshotID = cloneSnapshotID(request.ModificationSnapshotID)
	request.Labels = append([]string(nil), request.Labels...)
	return request
}

type Store interface {
	Record(context.Context, int, RecordRequest) (Outcome, bool, error)
	Modify(context.Context, int, ModifyRequest) (Outcome, bool, error)
	Get(context.Context, int, snapshot.WorkflowRunID, snapshot.SnapshotID) (Outcome, bool, error)
	ListByRun(context.Context, int, snapshot.WorkflowRunID) ([]Outcome, error)
}

type outcomeKey struct {
	teamID     int
	runID      snapshot.WorkflowRunID
	snapshotID snapshot.SnapshotID
}

type MemoryStore struct {
	mu       sync.RWMutex
	now      func() time.Time
	outcomes map[outcomeKey]Outcome
}

func NewMemoryStore(now func() time.Time) *MemoryStore {
	if now == nil {
		now = time.Now
	}
	return &MemoryStore{now: now, outcomes: make(map[outcomeKey]Outcome)}
}

func (store *MemoryStore) Record(ctx context.Context, teamID int, request RecordRequest) (Outcome, bool, error) {
	if ctx == nil {
		return Outcome{}, false, fmt.Errorf("%w: context is required", ErrInvalidOutcome)
	}
	if err := ctx.Err(); err != nil {
		return Outcome{}, false, err
	}
	request = request.clone()
	if teamID <= 0 {
		return Outcome{}, false, fmt.Errorf("%w: team ID must be positive", ErrInvalidOutcome)
	}
	if request.PublicationState != PublicationNotRequested || request.PublicationID != nil {
		return Outcome{}, false, fmt.Errorf("%w: publication evidence is publisher-owned", ErrInvalidOutcome)
	}
	key := outcomeKey{teamID: teamID, runID: request.WorkflowRunID, snapshotID: request.OutputSnapshotID}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Outcome{}, false, err
	}
	existing, found := store.outcomes[key]
	if found && sameRecord(existing, request) {
		return existing.Clone(), false, nil
	}
	revision := int64(1)
	if found {
		revision = existing.Revision + 1
	}
	sort.Strings(request.Labels)
	outcome := Outcome{
		WorkflowRunID: request.WorkflowRunID, OutputSnapshotID: request.OutputSnapshotID,
		Disposition: request.Disposition, PublicationState: request.PublicationState,
		PublicationID: cloneInt64(request.PublicationID), HumanModified: request.HumanModified,
		ModificationSnapshotID: cloneSnapshotID(request.ModificationSnapshotID),
		InterventionCount:      request.InterventionCount, Labels: append([]string(nil), request.Labels...),
		Actor: request.Actor, Revision: revision, AuditedAt: store.now().UTC(),
	}
	if err := outcome.Validate(); err != nil {
		return Outcome{}, false, err
	}
	store.outcomes[key] = outcome.Clone()
	return outcome.Clone(), !found, nil
}

func (store *MemoryStore) Modify(ctx context.Context, teamID int, request ModifyRequest) (Outcome, bool, error) {
	if ctx == nil {
		return Outcome{}, false, fmt.Errorf("%w: context is required", ErrInvalidOutcome)
	}
	if err := ctx.Err(); err != nil {
		return Outcome{}, false, err
	}
	request = request.clone()
	sort.Strings(request.Labels)
	if request.Labels == nil {
		request.Labels = []string{}
	}
	candidate := Outcome{
		WorkflowRunID: request.WorkflowRunID, OutputSnapshotID: request.OutputSnapshotID,
		Disposition: request.Disposition, PublicationState: PublicationNotRequested,
		HumanModified: request.HumanModified, ModificationSnapshotID: request.ModificationSnapshotID,
		InterventionCount: 1, Labels: request.Labels, Actor: request.Actor,
		Revision: 1, AuditedAt: time.Unix(1, 0),
	}
	if teamID <= 0 || candidate.Validate() != nil {
		return Outcome{}, false, ErrInvalidOutcome
	}
	key := outcomeKey{teamID: teamID, runID: request.WorkflowRunID, snapshotID: request.OutputSnapshotID}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Outcome{}, false, err
	}
	existing, found := store.outcomes[key]
	if found && sameModification(existing, request) {
		return existing.Clone(), false, nil
	}
	outcome := candidate
	outcome.AuditedAt = store.now().UTC()
	if found {
		outcome.PublicationState = existing.PublicationState
		outcome.PublicationID = cloneInt64(existing.PublicationID)
		outcome.InterventionCount = existing.InterventionCount + 1
		outcome.Revision = existing.Revision + 1
	}
	store.outcomes[key] = outcome.Clone()
	return outcome.Clone(), !found, nil
}

func (store *MemoryStore) Get(
	ctx context.Context,
	teamID int,
	runID snapshot.WorkflowRunID,
	snapshotID snapshot.SnapshotID,
) (Outcome, bool, error) {
	if ctx == nil {
		return Outcome{}, false, fmt.Errorf("%w: context is required", ErrInvalidOutcome)
	}
	if err := ctx.Err(); err != nil {
		return Outcome{}, false, err
	}
	if teamID <= 0 || runID.Validate() != nil || snapshotID.Validate() != nil {
		return Outcome{}, false, fmt.Errorf("%w: invalid lookup identity", ErrInvalidOutcome)
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	outcome, found := store.outcomes[outcomeKey{teamID: teamID, runID: runID, snapshotID: snapshotID}]
	return outcome.Clone(), found, nil
}

func (store *MemoryStore) ListByRun(ctx context.Context, teamID int, runID snapshot.WorkflowRunID) ([]Outcome, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: context is required", ErrInvalidOutcome)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if teamID <= 0 || runID.Validate() != nil {
		return nil, fmt.Errorf("%w: invalid list identity", ErrInvalidOutcome)
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	outcomes := make([]Outcome, 0)
	for key, outcome := range store.outcomes {
		if key.teamID == teamID && key.runID == runID {
			outcomes = append(outcomes, outcome.Clone())
		}
	}
	sort.Slice(outcomes, func(left, right int) bool {
		return outcomes[left].OutputSnapshotID < outcomes[right].OutputSnapshotID
	})
	return outcomes, nil
}

func sameRecord(outcome Outcome, request RecordRequest) bool {
	if outcome.WorkflowRunID != request.WorkflowRunID || outcome.OutputSnapshotID != request.OutputSnapshotID ||
		outcome.Disposition != request.Disposition || outcome.PublicationState != request.PublicationState ||
		outcome.HumanModified != request.HumanModified || outcome.InterventionCount != request.InterventionCount ||
		outcome.Actor != request.Actor || !equalInt64(outcome.PublicationID, request.PublicationID) ||
		!equalSnapshotID(outcome.ModificationSnapshotID, request.ModificationSnapshotID) || len(outcome.Labels) != len(request.Labels) {
		return false
	}
	labels := append([]string(nil), request.Labels...)
	sort.Strings(labels)
	for index := range labels {
		if labels[index] != outcome.Labels[index] {
			return false
		}
	}
	return true
}

func sameModification(outcome Outcome, request ModifyRequest) bool {
	if outcome.WorkflowRunID != request.WorkflowRunID || outcome.OutputSnapshotID != request.OutputSnapshotID ||
		outcome.Disposition != request.Disposition || outcome.HumanModified != request.HumanModified ||
		outcome.Actor != request.Actor ||
		!equalSnapshotID(outcome.ModificationSnapshotID, request.ModificationSnapshotID) ||
		len(outcome.Labels) != len(request.Labels) {
		return false
	}
	labels := append([]string(nil), request.Labels...)
	sort.Strings(labels)
	for index := range labels {
		if labels[index] != outcome.Labels[index] {
			return false
		}
	}
	return true
}

func validBoundedText(value string, maximum int) bool {
	if strings.TrimSpace(value) != value || value == "" || len(value) > maximum {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func cloneInt64(value *snapshot.DatabaseID) *snapshot.DatabaseID {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneSnapshotID(value *snapshot.SnapshotID) *snapshot.SnapshotID {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func equalInt64(left, right *snapshot.DatabaseID) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func equalSnapshotID(left, right *snapshot.SnapshotID) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}
