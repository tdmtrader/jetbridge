package outputplane

// The capture ledger: this node's half of a capture, which is one marker file
// per step directory and nothing else.
//
// The control plane owns the capture row and every decision; this file answers
// three questions only the node can: is this step directory held, may it be
// sealed, and what are its bytes. The marker is written by fsync and rename in
// the private control directory, keyed by the step directory it protects:
//
//	held    written before the step's first container may start
//	sealed  the producer exited; nobody writes the tree again
//	(gone)  released; the directory is the artifact daemon's ordinary business
//
// Three rules run through it.
//
// NO MARKER NEVER MEANS "CAPTURE WHAT IS THERE". A seal, a publish or a stat of
// a directory with no held marker is refused: a producer scheduled on another
// node, or a hold that never happened, would otherwise publish an empty tree
// that looks exactly like a successful capture.
//
// The base execution's cleanup gate goes down BEFORE the marker is written and
// comes up AFTER it is removed. A crash between leaves the gate open, which
// withholds cleanup -- the fail-closed direction.
//
// A seal waits for every container of the exact Pod the marker names to be
// terminated, and never deletes anything to get there. A sidecar still writing
// when the main process exits is part of the tree, and the tree is what the
// directory holds once nobody can write to it.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// SourceHoldGate is the opaque name the base execution ledger knows a held
// step directory by. It is the whole vocabulary crossing that boundary.
const SourceHoldGate = "source-hold"

// stepMarkerPrefix names marker records in the control directory. The
// read-only classifier in hangar/output/ledger keys on the same prefix.
const stepMarkerPrefix = "capture-"

// PodTerminations answers whether every container of one Pod on this node has
// terminated. It never deletes, evicts or signals anything; it only looks.
type PodTerminations interface {
	Terminated(ctx context.Context, pod executioncontrol.PodUID) (bool, error)
}

// CaptureLedger is the node's marker store and the seal/publish path over it.
type CaptureLedger struct {
	store        *controlStore
	base         *ExecutionLedger
	node         executioncontrol.NodeUID
	daemon       *Daemon
	terminations PodTerminations

	// steps is a descriptor-relative handle on the managed steps directory: a
	// symlink swapped under a step directory cannot redirect a read.
	steps     *os.Root
	stepsPath string
	staging   string

	// sealWait bounds one seal call's wait for termination. A Pod still
	// running past it answers ErrUnresolved and the control plane asks again.
	sealWait time.Duration
	poll     time.Duration

	mu sync.Mutex
}

// CaptureLedgerConfig is everything the ledger needs beyond the store.
type CaptureLedgerConfig struct {
	Node         executioncontrol.NodeUID
	StepsDir     string
	ScratchDir   string
	Terminations PodTerminations
	SealWait     time.Duration
}

func OpenCaptureLedger(store *controlStore, base *ExecutionLedger, daemon *Daemon, config CaptureLedgerConfig) (*CaptureLedger, error) {
	if store == nil || base == nil || daemon == nil {
		return nil, fmt.Errorf("%w: the capture ledger has no control store, base ledger or daemon",
			output.ErrIncomplete)
	}
	if config.Node == "" {
		return nil, fmt.Errorf("%w: the capture ledger names no node", output.ErrIncomplete)
	}
	if config.Terminations == nil {
		return nil, fmt.Errorf("%w: the capture ledger cannot observe Pod termination, and a seal "+
			"that cannot wait for writers to stop is a torn tree", output.ErrIncomplete)
	}
	if config.SealWait <= 0 {
		config.SealWait = 30 * time.Second
	}
	if err := os.MkdirAll(config.StepsDir, 0o700); err != nil {
		return nil, fmt.Errorf("%w: creating the managed steps directory: %v", output.ErrInfrastructure, err)
	}
	steps, err := os.OpenRoot(config.StepsDir)
	if err != nil {
		return nil, fmt.Errorf("%w: opening the managed steps directory: %v", output.ErrInfrastructure, err)
	}
	staging := filepath.Join(config.ScratchDir, "staged")
	if err := os.MkdirAll(staging, 0o700); err != nil {
		_ = steps.Close()

		return nil, fmt.Errorf("%w: creating the capture staging directory: %v", output.ErrInfrastructure, err)
	}

	return &CaptureLedger{
		store: store, base: base, node: config.Node, daemon: daemon,
		terminations: config.Terminations, steps: steps, stepsPath: config.StepsDir,
		staging: staging, sealWait: config.SealWait, poll: 250 * time.Millisecond,
	}, nil
}

func (ledger *CaptureLedger) Close() error { return ledger.steps.Close() }

func markerName(key output.CaptureKey) (string, error) {
	if err := key.Validate(); err != nil {
		return "", err
	}

	return stepMarkerPrefix + string(key.ExecutionID) + "." + string(key.Output) + ".json", nil
}

// load reads the marker. Absent is (false, nil); every other problem --
// unreadable, torn, a version this binary does not know -- is an error, and
// the caller refuses rather than reading it as absent.
func (ledger *CaptureLedger) load(key output.CaptureKey) (output.StepMarker, bool, error) {
	name, err := markerName(key)
	if err != nil {
		return output.StepMarker{}, false, err
	}
	var marker output.StepMarker
	found, err := ledger.store.get(name, &marker)
	if err != nil {
		return output.StepMarker{}, false, fmt.Errorf("%w: the step marker for %s is unreadable, so "+
			"whether it is held is unknown: %v", output.ErrInfrastructure, key, err)
	}
	if !found {
		return output.StepMarker{}, false, nil
	}
	if err := marker.Validate(); err != nil {
		return output.StepMarker{}, false, fmt.Errorf("%w: the step marker for %s does not validate: %v",
			output.ErrInfrastructure, key, err)
	}
	if marker.Key() != key {
		return output.StepMarker{}, false, fmt.Errorf("%w: the step marker filed under %s names %s",
			output.ErrInfrastructure, key, marker.Key())
	}

	return marker, true, nil
}

func (ledger *CaptureLedger) save(marker output.StepMarker) error {
	name, err := markerName(marker.Key())
	if err != nil {
		return err
	}

	return ledger.store.put(name, marker)
}

// admitted is the precondition every route shares: the base execution is
// admitted on this node at exactly this fence.
func (ledger *CaptureLedger) admitted(identity executioncontrol.Identity) error {
	current, err := ledger.base.Admission(identity.ExecutionID)
	if err != nil {
		return err
	}
	if identity.Fence < current.Fence {
		return fmt.Errorf("%w: fence %d was superseded by %d", executioncontrol.ErrStaleFence,
			identity.Fence, current.Fence)
	}
	if identity.Fence > current.Fence {
		return fmt.Errorf("%w: fence %d has not been admitted; this node holds %d",
			output.ErrUnauthorized, identity.Fence, current.Fence)
	}

	return nil
}

// Hold is step 1's node half: write the held marker. It is idempotent for the
// same Pod and refuses a different one -- a recreated Pod is a new writer and
// does not inherit a hold.
func (ledger *CaptureLedger) Hold(_ context.Context, request output.CaptureHoldRequest) (output.CaptureHoldAcknowledgement, error) {
	if err := request.Validate(); err != nil {
		return output.CaptureHoldAcknowledgement{}, err
	}
	key := output.CaptureKey{ExecutionID: request.Execution.ExecutionID, Output: request.Output}

	ledger.mu.Lock()
	defer ledger.mu.Unlock()

	if err := ledger.admitted(request.Execution); err != nil {
		return output.CaptureHoldAcknowledgement{}, err
	}
	marker, found, err := ledger.load(key)
	if err != nil {
		return output.CaptureHoldAcknowledgement{}, err
	}
	if found && marker.PodUID != request.PodUID {
		return output.CaptureHoldAcknowledgement{}, fmt.Errorf("%w: %s is held for pod %s and this "+
			"hold names %s; a recreated Pod does not inherit a hold", output.ErrConflict, key,
			marker.PodUID, request.PodUID)
	}
	// Gate before marker, on the first hold and on every replay: a crash
	// between the two leaves a gate with no marker, which withholds cleanup.
	if err := ledger.base.OpenGate(request.Execution, SourceHoldGate); err != nil {
		return output.CaptureHoldAcknowledgement{}, err
	}
	if !found {
		marker = output.StepMarker{
			State: output.StepHeld, ExecutionID: key.ExecutionID, Output: key.Output,
			Node: ledger.node, PodUID: request.PodUID,
		}
		if err := ledger.save(marker); err != nil {
			return output.CaptureHoldAcknowledgement{}, err
		}
	}
	// The directory, after the marker: a directory that exists before anything
	// protects it is one the sweeper may take.
	if err := ledger.steps.MkdirAll(key.Directory(), 0o755); err != nil {
		return output.CaptureHoldAcknowledgement{}, fmt.Errorf("%w: creating the step directory: %v",
			output.ErrInfrastructure, err)
	}

	return output.CaptureHoldAcknowledgement{
		ProtocolVersion: output.ProtocolVersion, Kind: output.HoldAcknowledged, Marker: marker,
	}, nil
}

// matching reads the marker and refuses unless it exists and names exactly
// this execution, output, node and (when given) Pod.
func (ledger *CaptureLedger) matching(key output.CaptureKey, pod executioncontrol.PodUID) (output.StepMarker, error) {
	marker, found, err := ledger.load(key)
	if err != nil {
		return output.StepMarker{}, err
	}
	if !found {
		return output.StepMarker{}, fmt.Errorf("%w: no held marker for %s on this node; a step "+
			"directory nothing held is never captured", output.ErrNotFound, key)
	}
	if marker.Node != ledger.node {
		return output.StepMarker{}, fmt.Errorf("%w: the marker for %s was written by node %s and "+
			"this is %s", output.ErrConflict, key, marker.Node, ledger.node)
	}
	if pod != "" && marker.PodUID != pod {
		return output.StepMarker{}, fmt.Errorf("%w: %s is held for pod %s and the request names %s",
			output.ErrConflict, key, marker.PodUID, pod)
	}

	return marker, nil
}

// resolve turns a capture into its step directory, refusing a symlink at the
// root: the bytes a capture seals are the ones the producer wrote there.
func (ledger *CaptureLedger) resolve(key output.CaptureKey) (string, error) {
	relative := key.Directory()
	info, err := ledger.steps.Lstat(relative)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("%w: the step directory for %s is gone", output.ErrNotFound, key)
		}

		return "", fmt.Errorf("%w: containment check on %s: %v", output.ErrUnauthorized, relative, err)
	}
	if info.Mode()&fs.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("%w: the step directory for %s is not a directory", output.ErrUnauthorized, key)
	}

	return path.Join(ledger.stepsPath, relative), nil
}

func (ledger *CaptureLedger) stagedPath(key output.CaptureKey, digest hangar.Digest) string {
	return filepath.Join(ledger.staging, string(key.ExecutionID)+"."+string(key.Output)+"."+
		strings.TrimPrefix(string(digest), "sha256:")+".tar")
}

// Seal is step 2: flip the marker to sealed, wait until every container of
// the marker's Pod has terminated, canonicalize, and answer the digest.
//
// It is idempotent: a seal of a sealed marker waits and canonicalizes again,
// which is how a daemon killed mid-seal finishes after its restart.
//
// slot, when given, is taken after the wait and held for the
// canonicalization: scratch is bounded, and a Pod that takes minutes to stop
// must not hold a slot every other capture needs.
func (ledger *CaptureLedger) Seal(ctx context.Context, request output.CaptureSealRequest,
	slot func(context.Context) (func(), error)) (output.CaptureSealResult, error) {
	if err := request.Validate(); err != nil {
		return output.CaptureSealResult{}, err
	}
	key := output.CaptureKey{ExecutionID: request.Execution.ExecutionID, Output: request.Output}

	ledger.mu.Lock()
	if err := ledger.admitted(request.Execution); err != nil {
		ledger.mu.Unlock()

		return output.CaptureSealResult{}, err
	}
	marker, err := ledger.matching(key, request.PodUID)
	if err == nil && marker.State == output.StepHeld {
		marker.State = output.StepSealed
		err = ledger.save(marker)
	}
	ledger.mu.Unlock()
	if err != nil {
		return output.CaptureSealResult{}, err
	}

	if err := ledger.awaitTermination(ctx, marker.PodUID); err != nil {
		return output.CaptureSealResult{}, err
	}
	if slot != nil {
		release, err := slot(ctx)
		if err != nil {
			return output.CaptureSealResult{}, err
		}
		defer release()
	}
	root, err := ledger.resolve(key)
	if err != nil {
		return output.CaptureSealResult{}, err
	}
	captured, err := ledger.daemon.CanonicalizeDirectory(ctx, root)
	if err != nil {
		return output.CaptureSealResult{}, err
	}
	defer captured.Close()

	staged := ledger.stagedPath(key, captured.Digest)
	if err := os.Rename(captured.ArchivePath, staged); err != nil {
		// Staging is an optimisation; the publish canonicalizes again.
		staged = ""
	}

	result := output.CaptureSealResult{
		ProtocolVersion: output.ProtocolVersion,
		Marker:          marker,
		Scope:           ledger.daemon.Namespace().Scope(),
		Digest:          captured.Digest,
		LogicalBytes:    captured.ByteSize,
	}
	if staged != "" {
		result.Staged = filepath.Base(staged)
	}

	return result, result.Validate()
}

func (ledger *CaptureLedger) awaitTermination(ctx context.Context, pod executioncontrol.PodUID) error {
	wait, cancel := context.WithTimeout(ctx, ledger.sealWait)
	defer cancel()
	for {
		done, err := ledger.terminations.Terminated(wait, pod)
		if err == nil && done {
			return nil
		}
		select {
		case <-wait.Done():
			if err != nil {
				return fmt.Errorf("%w: observing pod %s: %v", output.ErrUnresolved, pod, err)
			}

			return fmt.Errorf("%w: pod %s still has a container that has not terminated; the "+
				"seal waits for every writer to stop and never deletes one", output.ErrUnresolved, pod)
		case <-time.After(ledger.poll):
		}
	}
}

// Publish is step 4: create the object for the sealed tree, which must be
// the digest the control plane already recorded.
func (ledger *CaptureLedger) Publish(ctx context.Context, request output.CapturePublishRequest) (output.CapturePublishResult, error) {
	if err := request.Validate(); err != nil {
		return output.CapturePublishResult{}, err
	}
	key := output.CaptureKey{ExecutionID: request.Execution.ExecutionID, Output: request.Output}

	ledger.mu.Lock()
	err := ledger.admitted(request.Execution)
	var marker output.StepMarker
	if err == nil {
		marker, err = ledger.matching(key, "")
	}
	ledger.mu.Unlock()
	if err != nil {
		return output.CapturePublishResult{}, err
	}
	if marker.State != output.StepSealed {
		return output.CapturePublishResult{}, fmt.Errorf("%w: %s is %s; nothing is published before "+
			"its seal", output.ErrSealUnconfirmed, key, marker.State)
	}

	archive, size, cleanup, err := ledger.canonical(ctx, key, request.Digest)
	if err != nil {
		return output.CapturePublishResult{}, err
	}
	defer cleanup()

	namespace := ledger.daemon.Namespace()
	object, err := ledger.daemon.Publisher().EnsurePublication(ctx,
		namespace.MarkerFor(key.MarkerID(), request.Digest, output.NewTimestamp(nowUTC())),
		archive, size)
	if err != nil {
		return output.CapturePublishResult{}, err
	}

	result := output.CapturePublishResult{
		ProtocolVersion: output.ProtocolVersion,
		Ref:             object.Attributes.Ref,
		Metageneration:  object.Metageneration,
		MarkerVersion:   object.Marker.Version,
		Deduplicated:    object.Deduplicated,
	}

	return result, result.Validate()
}

// canonical opens the staged archive for digest, or canonicalizes the sealed
// directory again when the stage is gone (a restart, a full scratch). Either
// way the bytes must BE the digest; a tree that changed is a conflict.
func (ledger *CaptureLedger) canonical(ctx context.Context, key output.CaptureKey, digest hangar.Digest) (*os.File, int64, func(), error) {
	staged := ledger.stagedPath(key, digest)
	if file, err := os.Open(staged); err == nil {
		info, err := file.Stat()
		if err == nil {
			return file, info.Size(), func() { _ = file.Close() }, nil
		}
		_ = file.Close()
	}

	root, err := ledger.resolve(key)
	if err != nil {
		return nil, 0, nil, err
	}
	captured, err := ledger.daemon.CanonicalizeDirectory(ctx, root)
	if err != nil {
		return nil, 0, nil, err
	}
	if captured.Digest != digest {
		_ = captured.Close()

		return nil, 0, nil, fmt.Errorf("%w: %s canonicalizes to %s and the capture recorded %s",
			output.ErrConflict, key, captured.Digest, digest)
	}
	file, err := os.Open(captured.ArchivePath)
	if err != nil {
		_ = captured.Close()

		return nil, 0, nil, fmt.Errorf("%w: opening the canonical archive: %v", output.ErrInfrastructure, err)
	}

	return file, captured.ByteSize, func() { _ = file.Close(); _ = captured.Close() }, nil
}

// Release is step 6: clear the marker and anything staged for it, then close
// the base gate. Idempotent; a capture with no marker is already released.
func (ledger *CaptureLedger) Release(_ context.Context, request output.CaptureReleaseRequest) (output.CaptureReleaseAcknowledgement, error) {
	if err := request.Validate(); err != nil {
		return output.CaptureReleaseAcknowledgement{}, err
	}
	key := output.CaptureKey{ExecutionID: request.Execution.ExecutionID, Output: request.Output}

	ledger.mu.Lock()
	defer ledger.mu.Unlock()

	marker, found, err := ledger.load(key)
	if err != nil {
		return output.CaptureReleaseAcknowledgement{}, err
	}
	if found && marker.Node != ledger.node {
		return output.CaptureReleaseAcknowledgement{}, fmt.Errorf("%w: the marker for %s was written "+
			"by node %s", output.ErrConflict, key, marker.Node)
	}
	if found {
		name, err := markerName(key)
		if err != nil {
			return output.CaptureReleaseAcknowledgement{}, err
		}
		if err := ledger.store.remove(name); err != nil {
			return output.CaptureReleaseAcknowledgement{}, err
		}
	}
	if stale, err := filepath.Glob(filepath.Join(ledger.staging,
		string(key.ExecutionID)+"."+string(key.Output)+".*.tar")); err == nil {
		for _, file := range stale {
			_ = os.Remove(file)
		}
	}
	// The gate last: the marker is gone, so the execution's cleanup is
	// withheld by nothing this capture owns.
	// A capture this node never held -- the producer ran elsewhere, or the
	// hold never happened -- has no gate to close and is released already.
	if err := ledger.base.CloseGate(request.Execution, SourceHoldGate); err != nil &&
		(found || !errors.Is(err, output.ErrUnauthorized)) {
		return output.CaptureReleaseAcknowledgement{}, err
	}

	return output.CaptureReleaseAcknowledgement{
		ProtocolVersion: output.ProtocolVersion, Kind: output.ReleaseAcknowledged, Key: key,
	}, nil
}

// Stat is recovery's question: what does the store hold for this digest now?
// Absent is output.ErrNotFound. A present object must be marked by this plane
// with this digest, or it is a conflict and is never adopted.
func (ledger *CaptureLedger) Stat(ctx context.Context, request output.CaptureStatRequest) (output.CapturePublishResult, error) {
	if err := request.Validate(); err != nil {
		return output.CapturePublishResult{}, err
	}
	object, err := ledger.daemon.Publisher().StatCurrentObject(ctx, request.Digest)
	if err != nil {
		return output.CapturePublishResult{}, err
	}
	result := output.CapturePublishResult{
		ProtocolVersion: output.ProtocolVersion,
		Ref:             object.Attributes.Ref,
		Metageneration:  object.Metageneration,
		MarkerVersion:   object.Marker.Version,
		Deduplicated:    true,
	}

	return result, result.Validate()
}
