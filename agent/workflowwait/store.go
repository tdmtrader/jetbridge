package workflowwait

import (
	"context"
	"errors"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
)

var (
	ErrConflict    = errors.New("workflow wait: immutable state conflicts")
	ErrUnavailable = errors.New("workflow wait: unavailable or unauthorized")
	ErrExpired     = errors.New("workflow wait: deadline has passed")
)

type ResolveRequest struct {
	TeamID        int
	WorkflowRunID snapshot.WorkflowRunID
	WaitID        ID
	Answer        snapshot.SnapshotRef
	AnswerValue   string
	Actor         string
	DisplayName   string
	ReservedAt    time.Time
}

type ReserveResolutionRequest struct {
	TeamID        int
	WorkflowRunID snapshot.WorkflowRunID
	WaitID        ID
	AnswerValue   string
	Actor         string
	DisplayName   string
}

type ResolutionIntent struct {
	AnswerValue string
	Actor       string
	DisplayName string
	ReservedAt  time.Time
}

// PendingResolution is a durable, server-accepted answer that still needs its
// canonical snapshot materialized and atomically attached to the wait. The
// intent is immutable after reservation, so any worker may safely replay it.
type PendingResolution struct {
	Wait   Wait
	Intent ResolutionIntent
}

type Store interface {
	CreateOrGet(context.Context, CreateRequest) (Wait, bool, error)
	Get(context.Context, int, snapshot.WorkflowRunID, ID) (Wait, bool, error)
	List(context.Context, int, snapshot.WorkflowRunID) ([]Wait, error)
	ReserveResolution(context.Context, ReserveResolutionRequest) (Wait, ResolutionIntent, bool, error)
	PendingResolutions(context.Context, int, snapshot.WorkflowRunID, int) ([]PendingResolution, error)
	Resolve(context.Context, ResolveRequest) (Wait, bool, error)
	Expire(context.Context, ExecutionKey, time.Time) (Wait, bool, error)
	CancelRun(context.Context, int, snapshot.WorkflowRunID, string, time.Time) (int, error)
}
