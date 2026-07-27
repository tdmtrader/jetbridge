package workflowwait

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

const pendingResolutionBatchLimit = 100

// AnswerSnapshotCreator is the narrow server-owned upload boundary used to
// materialize an accepted answer intent. snapshot.SnapshotCreator satisfies it.
type AnswerSnapshotCreator interface {
	Upload(context.Context, snapshot.UploadRequest) (snapshot.Snapshot, error)
}

// MaterializeAnswer deterministically turns a durable resolution intent into
// the canonical human-answer snapshot for one wait. The idempotency key and
// document timestamp are both derived from durable state, so replay after a
// process crash cannot mint a different semantic value.
func MaterializeAnswer(
	ctx context.Context,
	creator AnswerSnapshotCreator,
	teamID int,
	teamName string,
	runID snapshot.WorkflowRunID,
	waitID ID,
	intent ResolutionIntent,
) (snapshot.Snapshot, error) {
	if workflowWaitDependencyNil(creator) || strings.TrimSpace(teamName) == "" ||
		ValidateResolutionIdentity(
			teamID,
			runID,
			waitID,
			intent.AnswerValue,
			intent.Actor,
			intent.DisplayName,
		) != nil ||
		intent.ReservedAt.IsZero() {
		return snapshot.Snapshot{}, fmt.Errorf("workflow wait: answer materialization is invalid")
	}
	document := contracts.HumanAnswerDocument{
		SchemaVersion: "1.0.0",
		Answer:        intent.AnswerValue,
		AnsweredBy:    intent.Actor,
		AnsweredAt:    intent.ReservedAt.UTC().Format(time.RFC3339Nano),
	}
	if err := document.Validate(); err != nil {
		return snapshot.Snapshot{}, err
	}
	contents, err := json.Marshal(document)
	if err != nil {
		return snapshot.Snapshot{}, err
	}
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	if err := writer.WriteHeader(&tar.Header{
		Name:    "human-answer.json",
		Mode:    0600,
		Size:    int64(len(contents)),
		ModTime: time.Unix(0, 0).UTC(),
	}); err != nil {
		return snapshot.Snapshot{}, err
	}
	if _, err := writer.Write(contents); err != nil {
		return snapshot.Snapshot{}, err
	}
	if err := writer.Close(); err != nil {
		return snapshot.Snapshot{}, err
	}
	archiveBytes := append([]byte(nil), archive.Bytes()...)
	sourceMetadata, err := json.Marshal(map[string]string{
		"source":           "workflow-wait-answer",
		"workflow_run_id":  runID.String(),
		"workflow_wait_id": waitID.String(),
	})
	if err != nil {
		return snapshot.Snapshot{}, err
	}
	manifest, err := creator.Upload(ctx, snapshot.UploadRequest{
		TeamID:     teamID,
		TeamName:   teamName,
		UploadedBy: intent.DisplayName,
		Actor:      intent.Actor,
		IdempotencyKey: fmt.Sprintf(
			"workflow-wait-answer:%d:%s:%s",
			teamID,
			runID.String(),
			waitID.String(),
		),
		Type: snapshot.TypeRef("human-answer/v1"),
		OpenTar: func(context.Context) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(archiveBytes)), nil
		},
		SourceMetadata: sourceMetadata,
	})
	if err != nil {
		return snapshot.Snapshot{}, err
	}
	if manifest.Validate() != nil || manifest.Type != snapshot.TypeRef("human-answer/v1") ||
		manifest.ContentState != snapshot.ContentStateAvailable {
		return snapshot.Snapshot{}, fmt.Errorf("workflow wait: creator returned an invalid answer snapshot")
	}
	return manifest, nil
}

// ResolutionCompleter drains the durable resolution-intent outbox for a
// workflow run. It deliberately has no lease: concurrent workers replay the
// same deterministic upload and Resolve is an exact CAS over the full intent.
type ResolutionCompleter struct {
	store   Store
	creator AnswerSnapshotCreator
}

func NewResolutionCompleter(store Store, creator AnswerSnapshotCreator) (*ResolutionCompleter, error) {
	if workflowWaitDependencyNil(store) || workflowWaitDependencyNil(creator) {
		return nil, fmt.Errorf("workflow wait: resolution completer requires store and creator")
	}
	return &ResolutionCompleter{store: store, creator: creator}, nil
}

func (completer *ResolutionCompleter) ReconcilePending(
	ctx context.Context,
	teamID int,
	teamName string,
	runID snapshot.WorkflowRunID,
) error {
	pending, err := completer.store.PendingResolutions(
		ctx,
		teamID,
		runID,
		pendingResolutionBatchLimit,
	)
	if err != nil {
		return fmt.Errorf("workflow wait: list pending resolutions: %w", err)
	}
	for _, resolution := range pending {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		wait := resolution.Wait
		if wait.Key.TeamID != teamID || wait.Key.WorkflowRunID != runID ||
			wait.Status != StatusWaiting || wait.ExpectedType != snapshot.TypeRef("human-answer/v1") {
			return fmt.Errorf("workflow wait: pending resolution identity is invalid")
		}
		manifest, err := MaterializeAnswer(
			ctx,
			completer.creator,
			teamID,
			teamName,
			runID,
			wait.ID,
			resolution.Intent,
		)
		if err != nil {
			return fmt.Errorf("workflow wait: materialize pending answer: %w", err)
		}
		ref := snapshot.SnapshotRef{ID: manifest.ID, Type: manifest.Type, Digest: manifest.Digest}
		_, found, err := completer.store.Resolve(ctx, ResolveRequest{
			TeamID:        teamID,
			WorkflowRunID: runID,
			WaitID:        wait.ID,
			Answer:        ref,
			AnswerValue:   resolution.Intent.AnswerValue,
			Actor:         resolution.Intent.Actor,
			DisplayName:   resolution.Intent.DisplayName,
			ReservedAt:    resolution.Intent.ReservedAt,
		})
		if err == nil && found {
			continue
		}
		if errors.Is(err, ErrConflict) {
			current, currentFound, getErr := completer.store.Get(ctx, teamID, runID, wait.ID)
			if getErr == nil && currentFound {
				switch current.Status {
				case StatusCancelled, StatusTimedOut:
					continue
				case StatusResolved:
					if current.Answer != nil && *current.Answer == ref &&
						current.ResolvedBy == resolution.Intent.Actor &&
						current.ResolvedByDisplayName == resolution.Intent.DisplayName {
						continue
					}
				}
			}
			err = errors.Join(err, getErr)
		}
		if err == nil && !found {
			err = ErrUnavailable
		}
		return fmt.Errorf("workflow wait: complete pending answer: %w", err)
	}
	return nil
}

func workflowWaitDependencyNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
