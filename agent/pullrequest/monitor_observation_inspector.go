package pullrequest

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

// snapshotMonitorObservationInspector reopens a team-authorized immutable
// pull-request observation, re-canonicalizes its bytes, and validates the
// sealed record before exposing provider lifecycle facts to the run
// classifier.
type snapshotMonitorObservationInspector struct {
	metadata      snapshot.MetadataStore
	content       snapshot.ContentStore
	canonicalizer snapshot.Canonicalizer
}

func NewSnapshotMonitorObservationInspector(
	metadata snapshot.MetadataStore,
	content snapshot.ContentStore,
	canonicalizer snapshot.Canonicalizer,
) (MonitorObservationInspector, error) {
	if nilMonitorObservationDependency(metadata) ||
		nilMonitorObservationDependency(content) {
		return nil, fmt.Errorf(
			"pullrequest: monitor observation metadata and content stores are required",
		)
	}
	return &snapshotMonitorObservationInspector{
		metadata: metadata, content: content,
		canonicalizer: canonicalizer,
	}, nil
}

func (inspector *snapshotMonitorObservationInspector) InspectMonitorObservation(
	ctx context.Context,
	teamID int,
	reference snapshot.SnapshotRef,
) (contracts.PullRequestBody, error) {
	if ctx == nil || teamID <= 0 {
		return contracts.PullRequestBody{}, fmt.Errorf(
			"pullrequest: monitor observation inspection requires context and team",
		)
	}
	if err := ctx.Err(); err != nil {
		return contracts.PullRequestBody{}, err
	}
	if err := reference.Validate(); err != nil ||
		reference.Type != snapshot.TypeRef("pull-request/v1") {
		return contracts.PullRequestBody{}, fmt.Errorf(
			"pullrequest: monitor observation reference is invalid",
		)
	}
	manifest, found, err := inspector.metadata.GetAuthorized(
		ctx, teamID, reference.ID,
	)
	if err != nil {
		return contracts.PullRequestBody{}, fmt.Errorf(
			"pullrequest: authorize monitor observation: %w", err,
		)
	}
	if !found {
		return contracts.PullRequestBody{}, fmt.Errorf(
			"%w: monitor observation", snapshot.ErrNotFound,
		)
	}
	if err := manifest.Validate(); err != nil ||
		manifest.ID != reference.ID ||
		manifest.Type != reference.Type ||
		manifest.Digest != reference.Digest {
		return contracts.PullRequestBody{}, fmt.Errorf(
			"pullrequest: authorized monitor observation is not the exact sealed reference",
		)
	}
	if manifest.ContentState != snapshot.ContentStateAvailable {
		return contracts.PullRequestBody{}, fmt.Errorf(
			"%w: monitor observation is %s",
			snapshot.ErrContentUnavailable, manifest.ContentState,
		)
	}
	reader, err := inspector.content.Open(ctx, manifest)
	if err != nil {
		var closeErr error
		if reader != nil {
			closeErr = reader.Close()
		}
		return contracts.PullRequestBody{}, fmt.Errorf(
			"pullrequest: open monitor observation: %w",
			errors.Join(snapshot.ErrContentUnavailable, err, closeErr),
		)
	}
	if reader == nil {
		return contracts.PullRequestBody{}, fmt.Errorf(
			"%w: open monitor observation",
			snapshot.ErrContentUnavailable,
		)
	}
	tree, captureErr := inspector.canonicalizer.Capture(ctx, reader)
	closeErr := reader.Close()
	if err := errors.Join(captureErr, closeErr); err != nil {
		if tree != nil {
			_ = tree.Close()
		}
		return contracts.PullRequestBody{}, fmt.Errorf(
			"pullrequest: capture monitor observation: %w", err,
		)
	}
	if tree == nil {
		return contracts.PullRequestBody{}, fmt.Errorf(
			"pullrequest: monitor observation canonicalizer returned no value",
		)
	}
	if tree.Digest != manifest.Digest ||
		tree.ByteSize != manifest.ByteSize ||
		tree.FileCount != manifest.FileCount {
		_ = tree.Close()
		return contracts.PullRequestBody{}, fmt.Errorf(
			"pullrequest: monitor observation content does not match its sealed manifest",
		)
	}
	root, err := tree.OpenRoot()
	if err != nil {
		_ = tree.Close()
		return contracts.PullRequestBody{}, fmt.Errorf(
			"pullrequest: open sealed monitor observation: %w", err,
		)
	}
	record, recordErr := contracts.ReadSealedPullRequestRecord(ctx, root)
	rootCloseErr := root.Close()
	treeCloseErr := tree.Close()
	if err := errors.Join(
		recordErr, rootCloseErr, treeCloseErr,
	); err != nil {
		return contracts.PullRequestBody{}, fmt.Errorf(
			"pullrequest: read sealed monitor observation: %w", err,
		)
	}
	return record.Body, nil
}

func nilMonitorObservationDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map,
		reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

var _ MonitorObservationInspector = (*snapshotMonitorObservationInspector)(nil)
