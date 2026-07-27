// Package workflowwaittest provides the in-memory human-wait store the
// workflowwait tests and the workflowwaits API tests run against. It lives
// outside the production package so no test double is compiled into the web
// binary.
package workflowwaittest

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflowwait"
)

type MemoryStore struct {
	mu      sync.Mutex
	nextID  workflowwait.ID
	now     func() time.Time
	byID    map[workflowwait.ID]workflowwait.Wait
	byKey   map[workflowwait.ExecutionKey]workflowwait.ID
	intents map[workflowwait.ID]workflowwait.ResolutionIntent
}

func NewMemoryStore(now func() time.Time) *MemoryStore {
	if now == nil {
		now = time.Now
	}
	return &MemoryStore{
		nextID:  1,
		now:     now,
		byID:    make(map[workflowwait.ID]workflowwait.Wait),
		byKey:   make(map[workflowwait.ExecutionKey]workflowwait.ID),
		intents: make(map[workflowwait.ID]workflowwait.ResolutionIntent),
	}
}

func (store *MemoryStore) CreateOrGet(_ context.Context, request workflowwait.CreateRequest) (workflowwait.Wait, bool, error) {
	if err := request.Validate(); err != nil {
		return workflowwait.Wait{}, false, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if id, found := store.byKey[request.Key]; found {
		wait := store.byID[id]
		if !sameCreate(wait, request) {
			return workflowwait.Wait{}, false, workflowwait.ErrConflict
		}
		return cloneWait(wait), false, nil
	}
	now := store.now().UTC()
	wait := workflowwait.Wait{
		ID: store.nextID, Key: request.Key, QuestionName: request.QuestionName, Question: request.Question,
		ExpectedType: request.ExpectedType, Deadline: request.Deadline.UTC(), TimeoutPolicy: request.TimeoutPolicy,
		Default: cloneRef(request.Default), WorkflowPort: request.WorkflowPort,
		WorkflowDefinitionID: request.WorkflowDefinitionID, Status: workflowwait.StatusWaiting, CreatedAt: now, UpdatedAt: now,
	}
	if err := wait.Validate(); err != nil {
		return workflowwait.Wait{}, false, err
	}
	store.nextID++
	store.byID[wait.ID] = wait
	store.byKey[wait.Key] = wait.ID
	return cloneWait(wait), true, nil
}

func (store *MemoryStore) Get(_ context.Context, teamID int, runID snapshot.WorkflowRunID, id workflowwait.ID) (workflowwait.Wait, bool, error) {
	if teamID <= 0 || runID.Validate() != nil || id.Validate() != nil {
		return workflowwait.Wait{}, false, fmt.Errorf("workflow wait: scoped identity is invalid")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	wait, found := store.byID[id]
	if !found || wait.Key.TeamID != teamID || wait.Key.WorkflowRunID != runID {
		return workflowwait.Wait{}, false, nil
	}
	return cloneWait(wait), true, nil
}

func (store *MemoryStore) List(_ context.Context, teamID int, runID snapshot.WorkflowRunID) ([]workflowwait.Wait, error) {
	if teamID <= 0 || runID.Validate() != nil {
		return nil, fmt.Errorf("workflow wait: scoped identity is invalid")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	values := make([]workflowwait.Wait, 0)
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
	request workflowwait.ReserveResolutionRequest,
) (workflowwait.Wait, workflowwait.ResolutionIntent, bool, error) {
	if err := workflowwait.ValidateResolutionIdentity(
		request.TeamID,
		request.WorkflowRunID,
		request.WaitID,
		request.AnswerValue,
		request.Actor,
		request.DisplayName,
	); err != nil {
		return workflowwait.Wait{}, workflowwait.ResolutionIntent{}, false, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	wait, found := store.byID[request.WaitID]
	if !found || wait.Key.TeamID != request.TeamID || wait.Key.WorkflowRunID != request.WorkflowRunID {
		return workflowwait.Wait{}, workflowwait.ResolutionIntent{}, false, nil
	}
	intent, reserved := store.intents[wait.ID]
	if wait.Status == workflowwait.StatusResolved {
		if reserved && intent.AnswerValue == request.AnswerValue && intent.Actor == request.Actor {
			return cloneWait(wait), intent, true, nil
		}
		return workflowwait.Wait{}, workflowwait.ResolutionIntent{}, true, workflowwait.ErrConflict
	}
	if wait.Status != workflowwait.StatusWaiting {
		return workflowwait.Wait{}, workflowwait.ResolutionIntent{}, true, workflowwait.ErrConflict
	}
	if reserved {
		if intent.AnswerValue != request.AnswerValue || intent.Actor != request.Actor {
			return workflowwait.Wait{}, workflowwait.ResolutionIntent{}, true, workflowwait.ErrConflict
		}
		return cloneWait(wait), intent, true, nil
	}
	now := store.now().UTC()
	if !now.Before(wait.Deadline) {
		return workflowwait.Wait{}, workflowwait.ResolutionIntent{}, true, workflowwait.ErrExpired
	}
	intent = workflowwait.ResolutionIntent{
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
) ([]workflowwait.PendingResolution, error) {
	if teamID <= 0 || runID.Validate() != nil || limit <= 0 || limit > 1000 {
		return nil, fmt.Errorf("workflow wait: pending resolution scope is invalid")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	ids := make([]workflowwait.ID, 0)
	for id, wait := range store.byID {
		if wait.Key.TeamID != teamID || wait.Key.WorkflowRunID != runID || wait.Status != workflowwait.StatusWaiting {
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
	pending := make([]workflowwait.PendingResolution, 0, len(ids))
	for _, id := range ids {
		pending = append(pending, workflowwait.PendingResolution{
			Wait:   cloneWait(store.byID[id]),
			Intent: store.intents[id],
		})
	}
	return pending, nil
}

func (store *MemoryStore) Resolve(_ context.Context, request workflowwait.ResolveRequest) (workflowwait.Wait, bool, error) {
	if err := workflowwait.ValidateResolutionIdentity(
		request.TeamID,
		request.WorkflowRunID,
		request.WaitID,
		request.AnswerValue,
		request.Actor,
		request.DisplayName,
	); err != nil || request.Answer.Validate() != nil || request.ReservedAt.IsZero() {
		return workflowwait.Wait{}, false, fmt.Errorf("workflow wait: resolution is invalid")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	wait, found := store.byID[request.WaitID]
	if !found || wait.Key.TeamID != request.TeamID || wait.Key.WorkflowRunID != request.WorkflowRunID {
		return workflowwait.Wait{}, false, nil
	}
	if request.Answer.Type != wait.ExpectedType {
		return workflowwait.Wait{}, true, workflowwait.ErrUnavailable
	}
	intent, reserved := store.intents[wait.ID]
	if wait.Status == workflowwait.StatusResolved && wait.Answer != nil && *wait.Answer == request.Answer &&
		wait.ResolvedBy == request.Actor && reserved &&
		intent.AnswerValue == request.AnswerValue && intent.Actor == request.Actor &&
		intent.DisplayName == request.DisplayName && intent.ReservedAt.Equal(request.ReservedAt) {
		return cloneWait(wait), true, nil
	}
	if wait.Status != workflowwait.StatusWaiting {
		return workflowwait.Wait{}, true, workflowwait.ErrConflict
	}
	now := store.now().UTC()
	if !reserved || intent.AnswerValue != request.AnswerValue || intent.Actor != request.Actor ||
		intent.DisplayName != request.DisplayName || !intent.ReservedAt.Equal(request.ReservedAt) {
		return workflowwait.Wait{}, true, workflowwait.ErrConflict
	}
	wait.Status = workflowwait.StatusResolved
	wait.Answer = cloneRef(&request.Answer)
	wait.ResolvedBy = request.Actor
	wait.ResolvedByDisplayName = intent.DisplayName
	wait.ResolutionSource = "human"
	wait.ResolvedAt = &now
	wait.UpdatedAt = now
	store.byID[wait.ID] = wait
	return cloneWait(wait), true, nil
}

func (store *MemoryStore) Expire(_ context.Context, key workflowwait.ExecutionKey, now time.Time) (workflowwait.Wait, bool, error) {
	if err := key.Validate(); err != nil || now.IsZero() {
		return workflowwait.Wait{}, false, fmt.Errorf("workflow wait: expiry is invalid")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	id, found := store.byKey[key]
	if !found {
		return workflowwait.Wait{}, false, nil
	}
	wait := store.byID[id]
	if wait.Status != workflowwait.StatusWaiting {
		return cloneWait(wait), true, nil
	}
	if _, reserved := store.intents[id]; reserved {
		return cloneWait(wait), true, nil
	}
	if now.Before(wait.Deadline) {
		return cloneWait(wait), true, nil
	}
	resolvedAt := now.UTC()
	wait.Status = workflowwait.StatusTimedOut
	wait.ResolutionSource = "timeout"
	wait.ResolvedBy = "system:timeout"
	wait.ResolvedAt = &resolvedAt
	wait.UpdatedAt = resolvedAt
	if wait.TimeoutPolicy == workflowwait.TimeoutDefault {
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
		if wait.Key.TeamID != teamID || wait.Key.WorkflowRunID != runID || wait.Status != workflowwait.StatusWaiting {
			continue
		}
		resolvedAt := now.UTC()
		wait.Status = workflowwait.StatusCancelled
		wait.ResolvedBy = actor
		wait.ResolutionSource = "cancel"
		wait.ResolvedAt = &resolvedAt
		wait.UpdatedAt = resolvedAt
		store.byID[id] = wait
		count++
	}
	return count, nil
}

func sameCreate(wait workflowwait.Wait, request workflowwait.CreateRequest) bool {
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

// cloneRef/cloneTime/cloneWait keep the double's stored values immutable
// across reads, mirroring what row scanning gives the durable store for free.
// They moved here with the double: nothing in the production package copies a
// Wait.
func cloneRef(ref *snapshot.SnapshotRef) *snapshot.SnapshotRef {
	if ref == nil {
		return nil
	}
	copy := *ref
	return &copy
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneWait(wait workflowwait.Wait) workflowwait.Wait {
	wait.Default = cloneRef(wait.Default)
	wait.Answer = cloneRef(wait.Answer)
	wait.ResolvedAt = cloneTime(wait.ResolvedAt)
	return wait
}
