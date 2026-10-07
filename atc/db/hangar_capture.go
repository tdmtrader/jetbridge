package db

// The capture row's repository: one table, one row per (execution, output),
// and every write a compare-and-set on its state.
//
// Every method takes the caller's transaction and makes no network call. The
// coordinator reads, closes the transaction, asks a node, and commits the
// answer as one of these in a second transaction -- so a lost answer is
// resolved by reading the row again, never by remembering.
//
// A CAS that finds the row already where it was asked to go, with the same
// facts, succeeds: that is a replay of a commit whose answer was lost. Finding
// it anywhere else is output.ErrConflict naming the state it is in.

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// PendingCapture is step 1's database half: what the control plane knows when
// the producing step is admitted, before its Pod exists.
type PendingCapture struct {
	Key     output.CaptureKey
	Node    string
	NodeUID executioncontrol.NodeUID
	// PodUID is empty until the node names the Pod; the move to publishing
	// writes it from the node's own finish statement.
	PodUID executioncontrol.PodUID
	// Term is the capture deadline, measured from now() on the database clock.
	Term time.Duration
}

const hangarCaptureColumns = `execution_id, output_name, state, node, node_uid, coalesce(pod_uid, ''),
	coalesce(scope, ''), coalesce(digest, ''), coalesce(generation, 0), coalesce(error, ''),
	capture_deadline_at, created_at, finished_at, released_at`

func scanHangarCapture(scan func(...any) error) (output.Capture, error) {
	var capture output.Capture
	var execution, outputName, state, node, nodeUID, podUID, scope, digest string
	var finished, released sql.NullTime
	if err := scan(&execution, &outputName, &state, &node, &nodeUID, &podUID, &scope, &digest,
		&capture.Generation, &capture.Error, &capture.CaptureDeadline, &capture.CreatedAt,
		&finished, &released); err != nil {
		return output.Capture{}, err
	}
	capture.Key = output.CaptureKey{ExecutionID: executioncontrol.ExecutionID(execution), Output: output.OutputName(outputName)}
	capture.State = output.CaptureState(state)
	capture.Node, capture.NodeUID, capture.PodUID = node, executioncontrol.NodeUID(nodeUID), executioncontrol.PodUID(podUID)
	capture.Scope, capture.Digest = hangar.Scope(scope), hangar.Digest(digest)
	capture.CaptureDeadline = capture.CaptureDeadline.UTC()
	capture.CreatedAt = capture.CreatedAt.UTC()
	if finished.Valid {
		at := finished.Time.UTC()
		capture.FinishedAt = &at
	}
	if released.Valid {
		at := released.Time.UTC()
		capture.ReleasedAt = &at
	}

	return capture, capture.State.Validate()
}

// GetCapture reads one row, or output.ErrNotFound.
func (repository *HangarOutputRepository) GetCapture(ctx context.Context, tx output.Tx, key output.CaptureKey) (output.Capture, error) {
	if err := key.Validate(); err != nil {
		return output.Capture{}, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+hangarCaptureColumns+`
		FROM hangar_captures WHERE execution_id = $1 AND output_name = $2`,
		string(key.ExecutionID), string(key.Output))
	if err != nil {
		return output.Capture{}, hangarConflict(err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return output.Capture{}, hangarConflict(err)
		}

		return output.Capture{}, fmt.Errorf("%w: no capture %s", output.ErrNotFound, key)
	}

	return scanHangarCapture(rows.Scan)
}

// InsertPending is step 1. A repeat with the same facts returns the existing
// row; the same key with different facts is a conflict.
func (repository *HangarOutputRepository) InsertPending(ctx context.Context, tx output.Tx, pending PendingCapture) (output.Capture, error) {
	if err := pending.Key.Validate(); err != nil {
		return output.Capture{}, err
	}
	if pending.Node == "" || pending.NodeUID == "" {
		return output.Capture{}, fmt.Errorf("%w: a capture names no node", output.ErrIncomplete)
	}
	if pending.Term < time.Second {
		return output.Capture{}, fmt.Errorf("%w: capture deadline term %s", output.ErrIncomplete, pending.Term)
	}
	interval := hangarInterval(pending.Term)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO hangar_captures (execution_id, output_name, node, node_uid, pod_uid, capture_deadline_at)
		VALUES ($1, $2, $3, $4, nullif($5, ''), now() + $6::interval)
		ON CONFLICT (execution_id, output_name) DO NOTHING`,
		string(pending.Key.ExecutionID), string(pending.Key.Output), pending.Node,
		string(pending.NodeUID), string(pending.PodUID), interval); err != nil {
		return output.Capture{}, hangarConflict(err)
	}
	capture, err := repository.GetCapture(ctx, tx, pending.Key)
	if err != nil {
		return output.Capture{}, err
	}
	if capture.Node != pending.Node || capture.NodeUID != pending.NodeUID ||
		(pending.PodUID != "" && capture.PodUID != "" && capture.PodUID != pending.PodUID) {
		return output.Capture{}, fmt.Errorf("%w: capture %s already names node %s (%s)",
			output.ErrConflict, pending.Key, capture.Node, capture.NodeUID)
	}

	return capture, nil
}

// casCapture runs one guarded UPDATE and, when it touched nothing, decides
// between a replay (same destination, same facts) and a conflict.
func (repository *HangarOutputRepository) casCapture(ctx context.Context, tx output.Tx, key output.CaptureKey,
	update string, args []any, replay func(output.Capture) bool) (output.Capture, error) {
	if err := key.Validate(); err != nil {
		return output.Capture{}, err
	}
	if _, err := LockHangarSuffix(ctx, tx, repository.prefix, HangarLockRequest{
		CaptureRows: []output.CaptureKey{key},
	}); err != nil {
		return output.Capture{}, err
	}
	result, err := tx.ExecContext(ctx, update, append([]any{string(key.ExecutionID), string(key.Output)}, args...)...)
	if err != nil {
		return output.Capture{}, hangarConflict(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return output.Capture{}, err
	}
	capture, err := repository.GetCapture(ctx, tx, key)
	if err != nil {
		return output.Capture{}, err
	}
	if changed == 1 || replay(capture) {
		return capture, nil
	}

	return output.Capture{}, fmt.Errorf("%w: capture %s is %s", output.ErrConflict, key, capture.State)
}

// CASPendingToPublishing is step 3: the digest and the move to publishing are
// written BEFORE any object can exist, so recovery always knows which object a
// publishing row may have created. The Pod UID is the node's own statement of
// which Pod produced the sealed tree.
func (repository *HangarOutputRepository) CASPendingToPublishing(ctx context.Context, tx output.Tx, key output.CaptureKey, podUID executioncontrol.PodUID, scope hangar.Scope, digest hangar.Digest) (output.Capture, error) {
	if err := scope.Validate(); err != nil {
		return output.Capture{}, err
	}
	if err := digest.Validate(); err != nil {
		return output.Capture{}, err
	}
	if podUID == "" {
		return output.Capture{}, fmt.Errorf("%w: a sealed capture names no Pod", output.ErrIncomplete)
	}

	return repository.casCapture(ctx, tx, key, `
		UPDATE hangar_captures SET state = 'publishing', pod_uid = $3, scope = $4, digest = $5
		WHERE execution_id = $1 AND output_name = $2 AND state = 'pending'
		  AND (pod_uid IS NULL OR pod_uid = $3)`,
		[]any{string(podUID), string(scope), string(digest)},
		func(current output.Capture) bool {
			return current.State == output.CapturePublishing && current.Digest == digest &&
				current.Scope == scope && current.PodUID == podUID
		})
}

// CASPendingToDiscarded is the cancelled / no-output branch of step 3.
func (repository *HangarOutputRepository) CASPendingToDiscarded(ctx context.Context, tx output.Tx, key output.CaptureKey, reason string) (output.Capture, error) {
	if reason == "" {
		return output.Capture{}, fmt.Errorf("%w: a discard names no reason", output.ErrIncomplete)
	}

	return repository.casCapture(ctx, tx, key, `
		UPDATE hangar_captures SET state = 'discarded', error = $3, finished_at = now()
		WHERE execution_id = $1 AND output_name = $2 AND state = 'pending'`,
		[]any{reason},
		func(current output.Capture) bool { return current.State == output.CaptureDiscarded })
}

// MarkFailed ends a capture that cannot publish: a pending row past its
// deadline, or a publishing row whose object conflicts. It is never reached
// from published.
func (repository *HangarOutputRepository) MarkFailed(ctx context.Context, tx output.Tx, key output.CaptureKey, reason string) (output.Capture, error) {
	if reason == "" {
		return output.Capture{}, fmt.Errorf("%w: a failure names no reason", output.ErrIncomplete)
	}
	if len(reason) > 1024 {
		reason = reason[:1024]
	}

	return repository.casCapture(ctx, tx, key, `
		UPDATE hangar_captures SET state = 'failed', error = $3, finished_at = now()
		WHERE execution_id = $1 AND output_name = $2 AND state IN ('pending', 'publishing')`,
		[]any{reason},
		func(current output.Capture) bool { return current.State == output.CaptureFailed })
}

// PublishedCapture is what step 5 commits: the generation the store assigned
// and the facts the lifecycle row and the capture's claim need.
type PublishedCapture struct {
	Key            output.CaptureKey
	Generation     int64
	Metageneration int64
	// ActivationEpoch is the epoch the lifecycle and claim rows are recorded
	// under while epochs exist.
	ActivationEpoch int64
}

// CASPublishingToPublished is step 5. In one transaction it moves the row,
// registers the generation's lifecycle and takes the capture's own claim on it
// (output.CaptureKey.ClaimID), so a published tree is protected from reclaim
// from the instant it is published until a consumer releases that claim.
//
// Lock order is the suffix's: the logical class (every capture row of this
// scope and digest, this one included), then the exact lifecycle, then the
// claim.
func (repository *HangarOutputRepository) CASPublishingToPublished(ctx context.Context, tx output.Tx, published PublishedCapture) (output.Capture, error) {
	if published.Generation <= 0 || published.Metageneration <= 0 || published.ActivationEpoch <= 0 {
		return output.Capture{}, fmt.Errorf("%w: a publication names no generation, metageneration or epoch",
			output.ErrIncomplete)
	}
	current, err := repository.GetCapture(ctx, tx, published.Key)
	if err != nil {
		return output.Capture{}, err
	}
	if current.Digest == "" {
		return output.Capture{}, fmt.Errorf("%w: capture %s is %s and has no digest",
			output.ErrConflict, published.Key, current.State)
	}
	ref := hangar.TreeRef{Scope: current.Scope, Digest: current.Digest, Generation: published.Generation}
	if err := ref.Validate(); err != nil {
		return output.Capture{}, err
	}
	if _, err := LockHangarSuffix(ctx, tx, repository.prefix, HangarLockRequest{
		Logical: []HangarLogicalKey{{Scope: ref.Scope, Digest: ref.Digest}},
	}); err != nil {
		return output.Capture{}, err
	}

	capture, err := repository.casCapture(ctx, tx, published.Key, `
		UPDATE hangar_captures SET state = 'published', generation = $3, finished_at = now()
		WHERE execution_id = $1 AND output_name = $2 AND state = 'publishing'`,
		[]any{published.Generation},
		func(current output.Capture) bool {
			return current.State == output.CapturePublished && current.Generation == published.Generation
		})
	if err != nil {
		return output.Capture{}, err
	}
	if _, err := repository.upsertLifecycle(ctx, tx, ref, published.Metageneration,
		published.ActivationEpoch, "registered"); err != nil {
		return output.Capture{}, err
	}
	var now time.Time
	if err := hangarQueryRow(ctx, tx, `SELECT now()`, nil, &now); err != nil {
		return output.Capture{}, err
	}
	if err := repository.AcquireClaim(ctx, tx, output.ClaimAcquisition{
		ProtocolVersion:   output.ProtocolVersion,
		ClaimID:           published.Key.ClaimID(),
		Ref:               ref,
		ConsumerBindingID: output.OpaqueID("capture:" + published.Key.String()),
		RequestedAt:       output.NewTimestamp(now.UTC()),
	}); err != nil {
		return output.Capture{}, err
	}

	return capture, nil
}

// SetReleased is step 6's commit, after the node acknowledged clearing the
// marker. Only a terminal row is released, and only once.
func (repository *HangarOutputRepository) SetReleased(ctx context.Context, tx output.Tx, key output.CaptureKey) (output.Capture, error) {
	return repository.casCapture(ctx, tx, key, `
		UPDATE hangar_captures SET released_at = now()
		WHERE execution_id = $1 AND output_name = $2
		  AND state IN ('published', 'discarded', 'failed') AND released_at IS NULL`,
		nil,
		func(current output.Capture) bool { return current.Released() })
}

func (repository *HangarOutputRepository) listCaptures(ctx context.Context, tx output.Tx, where string, order string, limit int) ([]output.Capture, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("%w: a capture pass is bounded; %d is not a batch", output.ErrIncomplete, limit)
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+hangarCaptureColumns+`
		FROM hangar_captures WHERE `+where+` ORDER BY `+order+` LIMIT $1`, limit)
	if err != nil {
		return nil, hangarConflict(err)
	}
	defer Close(rows)
	var captures []output.Capture
	for rows.Next() {
		capture, err := scanHangarCapture(rows.Scan)
		if err != nil {
			return nil, err
		}
		captures = append(captures, capture)
	}
	if err := rows.Err(); err != nil {
		return nil, hangarConflict(err)
	}

	return captures, nil
}

// ListPending is the advancement pass: pending rows still inside their
// capture deadline, oldest first.
func (repository *HangarOutputRepository) ListPending(ctx context.Context, tx output.Tx, limit int) ([]output.Capture, error) {
	return repository.listCaptures(ctx, tx, `state = 'pending' AND capture_deadline_at > now()`,
		`created_at, execution_id, output_name`, limit)
}

// ListPendingPastDeadline is recovery's half for pending rows: a capture that
// never reached publishing inside its deadline fails.
func (repository *HangarOutputRepository) ListPendingPastDeadline(ctx context.Context, tx output.Tx, limit int) ([]output.Capture, error) {
	return repository.listCaptures(ctx, tx, `state = 'pending' AND capture_deadline_at <= now()`,
		`capture_deadline_at, execution_id, output_name`, limit)
}

// ListPublishingForRecovery is every row whose object may or may not exist.
func (repository *HangarOutputRepository) ListPublishingForRecovery(ctx context.Context, tx output.Tx, limit int) ([]output.Capture, error) {
	return repository.listCaptures(ctx, tx, `state = 'publishing'`,
		`created_at, execution_id, output_name`, limit)
}

// ListUnreleased is the release pass: terminal rows whose node marker has not
// been cleared. It is retried forever; the sweeper refuses the directory
// until it lands.
func (repository *HangarOutputRepository) ListUnreleased(ctx context.Context, tx output.Tx, limit int) ([]output.Capture, error) {
	return repository.listCaptures(ctx, tx,
		`released_at IS NULL AND state IN ('published', 'discarded', 'failed')`,
		`finished_at, execution_id, output_name`, limit)
}
