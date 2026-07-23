package workflowwait

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
)

type MemoryStore struct {
	mu      sync.Mutex
	nextID  ID
	now     func() time.Time
	byID    map[ID]Wait
	byKey   map[ExecutionKey]ID
	intents map[ID]ResolutionIntent
}

func NewMemoryStore(now func() time.Time) *MemoryStore {
	if now == nil {
		now = time.Now
	}
	return &MemoryStore{
		nextID:  1,
		now:     now,
		byID:    make(map[ID]Wait),
		byKey:   make(map[ExecutionKey]ID),
		intents: make(map[ID]ResolutionIntent),
	}
}

func (store *MemoryStore) CreateOrGet(_ context.Context, request CreateRequest) (Wait, bool, error) {
	if err := request.Validate(); err != nil {
		return Wait{}, false, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if id, found := store.byKey[request.Key]; found {
		wait := store.byID[id]
		if !sameCreate(wait, request) {
			return Wait{}, false, ErrConflict
		}
		return cloneWait(wait), false, nil
	}
	now := store.now().UTC()
	wait := Wait{
		ID: store.nextID, Key: request.Key, QuestionName: request.QuestionName, Question: request.Question,
		ExpectedType: request.ExpectedType, Deadline: request.Deadline.UTC(), TimeoutPolicy: request.TimeoutPolicy,
		Default: cloneRef(request.Default), WorkflowPort: request.WorkflowPort,
		WorkflowDefinitionID: request.WorkflowDefinitionID, Status: StatusWaiting, CreatedAt: now, UpdatedAt: now,
	}
	if err := wait.Validate(); err != nil {
		return Wait{}, false, err
	}
	store.nextID++
	store.byID[wait.ID] = wait
	store.byKey[wait.Key] = wait.ID
	return cloneWait(wait), true, nil
}

func (store *MemoryStore) Get(_ context.Context, teamID int, runID snapshot.WorkflowRunID, id ID) (Wait, bool, error) {
	if teamID <= 0 || runID.Validate() != nil || id.Validate() != nil {
		return Wait{}, false, fmt.Errorf("workflow wait: scoped identity is invalid")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	wait, found := store.byID[id]
	if !found || wait.Key.TeamID != teamID || wait.Key.WorkflowRunID != runID {
		return Wait{}, false, nil
	}
	return cloneWait(wait), true, nil
}

func (store *MemoryStore) List(_ context.Context, teamID int, runID snapshot.WorkflowRunID) ([]Wait, error) {
	if teamID <= 0 || runID.Validate() != nil {
		return nil, fmt.Errorf("workflow wait: scoped identity is invalid")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	values := make([]Wait, 0)
	for _, wait := range store.byID {
		if wait.Key.TeamID == teamID && wait.Key.WorkflowRunID == runID {
			values = append(values, cloneWait(wait))
		}
	}
	sort.Slice(values, func(i, j int) bool { return values[i].ID < values[j].ID })
	return values, nil
}

func (store *MemoryStore) ReserveResolution(
	_ context.Context,
	request ReserveResolutionRequest,
) (Wait, ResolutionIntent, bool, error) {
	if err := validateResolutionIdentity(
		request.TeamID,
		request.WorkflowRunID,
		request.WaitID,
		request.AnswerValue,
		request.Actor,
		request.DisplayName,
	); err != nil {
		return Wait{}, ResolutionIntent{}, false, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	wait, found := store.byID[request.WaitID]
	if !found || wait.Key.TeamID != request.TeamID || wait.Key.WorkflowRunID != request.WorkflowRunID {
		return Wait{}, ResolutionIntent{}, false, nil
	}
	intent, reserved := store.intents[wait.ID]
	if wait.Status == StatusResolved {
		if reserved && intent.AnswerValue == request.AnswerValue && intent.Actor == request.Actor {
			return cloneWait(wait), intent, true, nil
		}
		return Wait{}, ResolutionIntent{}, true, ErrConflict
	}
	if wait.Status != StatusWaiting {
		return Wait{}, ResolutionIntent{}, true, ErrConflict
	}
	if reserved {
		if intent.AnswerValue != request.AnswerValue || intent.Actor != request.Actor {
			return Wait{}, ResolutionIntent{}, true, ErrConflict
		}
		return cloneWait(wait), intent, true, nil
	}
	now := store.now().UTC()
	if !now.Before(wait.Deadline) {
		return Wait{}, ResolutionIntent{}, true, ErrExpired
	}
	intent = ResolutionIntent{
		AnswerValue: request.AnswerValue,
		Actor:       request.Actor,
		DisplayName: request.DisplayName,
		ReservedAt:  now,
	}
	store.intents[wait.ID] = intent
	return cloneWait(wait), intent, true, nil
}

func (store *MemoryStore) PendingResolutions(
	_ context.Context,
	teamID int,
	runID snapshot.WorkflowRunID,
	limit int,
) ([]PendingResolution, error) {
	if teamID <= 0 || runID.Validate() != nil || limit <= 0 || limit > 1000 {
		return nil, fmt.Errorf("workflow wait: pending resolution scope is invalid")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	ids := make([]ID, 0)
	for id, wait := range store.byID {
		if wait.Key.TeamID != teamID || wait.Key.WorkflowRunID != runID || wait.Status != StatusWaiting {
			continue
		}
		if _, found := store.intents[id]; found {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	if len(ids) > limit {
		ids = ids[:limit]
	}
	pending := make([]PendingResolution, 0, len(ids))
	for _, id := range ids {
		pending = append(pending, PendingResolution{
			Wait:   cloneWait(store.byID[id]),
			Intent: store.intents[id],
		})
	}
	return pending, nil
}

func (store *MemoryStore) Resolve(_ context.Context, request ResolveRequest) (Wait, bool, error) {
	if err := validateResolutionIdentity(
		request.TeamID,
		request.WorkflowRunID,
		request.WaitID,
		request.AnswerValue,
		request.Actor,
		request.DisplayName,
	); err != nil || request.Answer.Validate() != nil || request.ReservedAt.IsZero() {
		return Wait{}, false, fmt.Errorf("workflow wait: resolution is invalid")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	wait, found := store.byID[request.WaitID]
	if !found || wait.Key.TeamID != request.TeamID || wait.Key.WorkflowRunID != request.WorkflowRunID {
		return Wait{}, false, nil
	}
	if request.Answer.Type != wait.ExpectedType {
		return Wait{}, true, ErrUnavailable
	}
	intent, reserved := store.intents[wait.ID]
	if wait.Status == StatusResolved && wait.Answer != nil && *wait.Answer == request.Answer &&
		wait.ResolvedBy == request.Actor && reserved &&
		intent.AnswerValue == request.AnswerValue && intent.Actor == request.Actor &&
		intent.DisplayName == request.DisplayName && intent.ReservedAt.Equal(request.ReservedAt) {
		return cloneWait(wait), true, nil
	}
	if wait.Status != StatusWaiting {
		return Wait{}, true, ErrConflict
	}
	now := store.now().UTC()
	if !reserved || intent.AnswerValue != request.AnswerValue || intent.Actor != request.Actor ||
		intent.DisplayName != request.DisplayName || !intent.ReservedAt.Equal(request.ReservedAt) {
		return Wait{}, true, ErrConflict
	}
	wait.Status = StatusResolved
	wait.Answer = cloneRef(&request.Answer)
	wait.ResolvedBy = request.Actor
	wait.ResolvedByDisplayName = intent.DisplayName
	wait.ResolutionSource = "human"
	wait.ResolvedAt = &now
	wait.UpdatedAt = now
	store.byID[wait.ID] = wait
	return cloneWait(wait), true, nil
}

func validateResolutionIdentity(
	teamID int,
	runID snapshot.WorkflowRunID,
	waitID ID,
	answerValue string,
	actor string,
	displayName string,
) error {
	if teamID <= 0 || runID.Validate() != nil || waitID.Validate() != nil {
		return fmt.Errorf("workflow wait: resolution scope is invalid")
	}
	for label, value := range map[string]string{
		"answer":       answerValue,
		"actor":        actor,
		"display name": displayName,
	} {
		if strings.TrimSpace(value) == "" || len(value) > 16<<10 || strings.ContainsRune(value, '\x00') {
			return fmt.Errorf("workflow wait: resolution %s is invalid", label)
		}
	}
	return nil
}

func (store *MemoryStore) Expire(_ context.Context, key ExecutionKey, now time.Time) (Wait, bool, error) {
	if err := key.Validate(); err != nil || now.IsZero() {
		return Wait{}, false, fmt.Errorf("workflow wait: expiry is invalid")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	id, found := store.byKey[key]
	if !found {
		return Wait{}, false, nil
	}
	wait := store.byID[id]
	if wait.Status != StatusWaiting {
		return cloneWait(wait), true, nil
	}
	if _, reserved := store.intents[id]; reserved {
		return cloneWait(wait), true, nil
	}
	if now.Before(wait.Deadline) {
		return cloneWait(wait), true, nil
	}
	resolvedAt := now.UTC()
	wait.Status = StatusTimedOut
	wait.ResolutionSource = "timeout"
	wait.ResolvedBy = "system:timeout"
	wait.ResolvedAt = &resolvedAt
	wait.UpdatedAt = resolvedAt
	if wait.TimeoutPolicy == TimeoutDefault {
		wait.Answer = cloneRef(wait.Default)
	}
	store.byID[id] = wait
	return cloneWait(wait), true, nil
}

func (store *MemoryStore) CancelRun(_ context.Context, teamID int, runID snapshot.WorkflowRunID, actor string, now time.Time) (int, error) {
	if teamID <= 0 || runID.Validate() != nil || strings.TrimSpace(actor) == "" || now.IsZero() {
		return 0, fmt.Errorf("workflow wait: cancellation is invalid")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	count := 0
	for id, wait := range store.byID {
		if wait.Key.TeamID != teamID || wait.Key.WorkflowRunID != runID || wait.Status != StatusWaiting {
			continue
		}
		resolvedAt := now.UTC()
		wait.Status = StatusCancelled
		wait.ResolvedBy = actor
		wait.ResolutionSource = "cancel"
		wait.ResolvedAt = &resolvedAt
		wait.UpdatedAt = resolvedAt
		store.byID[id] = wait
		count++
	}
	return count, nil
}

func sameCreate(wait Wait, request CreateRequest) bool {
	return wait.Key == request.Key && wait.QuestionName == request.QuestionName && wait.Question == request.Question &&
		wait.ExpectedType == request.ExpectedType &&
		wait.TimeoutPolicy == request.TimeoutPolicy && equalRef(wait.Default, request.Default) &&
		wait.WorkflowPort == request.WorkflowPort && wait.WorkflowDefinitionID == request.WorkflowDefinitionID
}

func equalRef(left, right *snapshot.SnapshotRef) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
