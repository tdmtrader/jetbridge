package main

// The source ledger: this node's authority over one source incarnation.
//
// It answers three questions a database cannot: which bytes on this disk a
// capture will seal, who may write to them, and when nobody may any more. The
// first is a filesystem fact, the second is an admission decision that has to
// serialize against the third, and the third is only knowable where the writers
// are. PostgreSQL owns every other part of a capture; it cannot own these.
//
// Three rules run through the whole file.
//
// The incarnation is SERVER-issued. A caller never names a path, a handle or a
// generation; it names a handoff, and the daemon answers with an identity it
// minted. Req 7 is the reason: a handle string alone is never an identity,
// because handles are reused and a reused handle with a stale generation is
// exactly the confusion that lets one execution write into another's bytes.
// ResolveIncarnation is the only function that turns an identity into a
// location, it does so through the daemon's own os.Root handle, and it refuses
// anything it did not issue.
//
// Admission and sealing serialize on ONE durable boundary. Under one lock,
// either a ticket is issued and is therefore in whatever drain set a later seal
// captures, or the source is already sealing and issuance is refused. There is
// no third outcome, and in particular there is no window in which a ticket is
// issued into a set that was captured a moment ago.
//
// The captured drain set is the proof, not a live count. BeginSeal records
// exactly which tickets were open when admission was fenced, and ConfirmSeal is
// checked against that set. "No tickets are outstanding right now" is a
// different statement and is not accepted: a writer admitted and closed during
// the wait is still one that touched the bytes.

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// sourceState is the incarnation's lifecycle. It is a closed vocabulary stored
// in the record, so a state this binary does not know is refused at read rather
// than absorbed into "new".
type sourceState string

const (
	sourceHeld     sourceState = "held"
	sourceSealing  sourceState = "sealing"
	sourceSealed   sourceState = "sealed"
	sourceReleased sourceState = "released"
)

// sourceRecord is one capture's durable node-local state.
type sourceRecord struct {
	State           sourceState                      `json:"state"`
	Execution       executioncontrol.Identity        `json:"execution"`
	ActivationEpoch executioncontrol.ActivationEpoch `json:"activation_epoch"`
	HandoffID       output.HandoffID                 `json:"handoff_id"`
	SourceLeaseID   output.SourceLeaseID             `json:"source_lease_id"`
	Output          output.OutputName                `json:"output"`
	Incarnation     output.SourceIncarnation         `json:"incarnation"`

	// Hold is the signed statement itself, stored as signed. A replay hands
	// back these bytes.
	Hold *output.CaptureAcknowledgement `json:"hold,omitempty"`

	// Open and Closed are the writer tickets. Open is what a seal would
	// capture; Closed is what has been retired. Both are needed: a ticket that
	// was issued and closed before the seal is not in the drain set, and one
	// issued and closed after it must still be accounted for.
	Open   []output.WriterTicketID `json:"open_tickets,omitempty"`
	Closed []output.WriterTicketID `json:"closed_tickets,omitempty"`

	// DrainSet is captured at the instant admission is fenced and never
	// recomputed. Confirming against a live query instead is the defect this
	// field exists to make impossible.
	SealStarted   *output.CaptureAcknowledgement `json:"seal_started,omitempty"`
	DrainSet      []output.WriterTicketID        `json:"drain_set,omitempty"`
	SealConfirmed *output.CaptureAcknowledgement `json:"seal_confirmed,omitempty"`

	// Release is the fenced release pair's second half, stored so a repeat of
	// the same intent returns the same statement and a different intent is a
	// conflict.
	ReleaseIntentID output.ReleaseIntentID         `json:"release_intent_id,omitempty"`
	Release         *output.ReleaseAcknowledgement `json:"release,omitempty"`

	HighWater executioncontrol.LedgerSequence `json:"high_water"`
}

// SourceHoldGate is the opaque name the base execution ledger knows this
// extension by.
//
// It is a string and it is the whole vocabulary crossing that boundary: the
// base ledger will not learn that this gate is a source hold, and there is no
// argument through which it could be told.
const SourceHoldGate = "source-hold"

const sourceRecordPrefix = "source-"

// SourceLedger implements output.SourceControl over the same control store the
// execution ledger uses, and over an os.Root handle on the managed steps
// directory.
type SourceLedger struct {
	store  *controlStore
	base   *ExecutionLedger
	node   executioncontrol.NodeUID
	epoch  executioncontrol.ActivationEpoch
	signer *output.CaptureStatementSigner
	clock  func() time.Time

	// steps is a descriptor-relative handle on the managed steps directory. It
	// is a handle and not a path because the containment rule is about what
	// happens when the filesystem changes under a resolution, and only a handle
	// can say anything about that.
	steps     *os.Root
	stepsPath string

	mu       sync.Mutex
	sequence executioncontrol.LedgerSequence
}

func OpenSourceLedger(store *controlStore, base *ExecutionLedger, node executioncontrol.NodeUID,
	epoch executioncontrol.ActivationEpoch, signer *output.CaptureStatementSigner,
	clock func() time.Time, stepsDir string) (*SourceLedger, error) {
	if store == nil || base == nil {
		return nil, fmt.Errorf("%w: the source ledger has no control store or no base ledger",
			output.ErrIncomplete)
	}
	if signer == nil {
		return nil, fmt.Errorf("%w: the source ledger has no signing key", output.ErrIncomplete)
	}
	if node == "" || epoch == 0 {
		return nil, fmt.Errorf("%w: the source ledger names no node or no activation epoch",
			output.ErrIncomplete)
	}
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	if err := os.MkdirAll(stepsDir, 0o700); err != nil {
		return nil, fmt.Errorf("%w: creating the managed steps directory: %v",
			output.ErrInfrastructure, err)
	}
	root, err := os.OpenRoot(stepsDir)
	if err != nil {
		return nil, fmt.Errorf("%w: opening the managed steps directory: %v",
			output.ErrInfrastructure, err)
	}

	ledger := &SourceLedger{
		store: store, base: base, node: node, epoch: epoch, signer: signer,
		clock: clock, steps: root, stepsPath: stepsDir,
	}

	// Recreating a registry does not recreate authority, and neither does
	// restarting: what this recovers is the sequence high-water mark, so two
	// statements from one node stay orderable. Every fact is read from the
	// record at the moment it is needed.
	names, err := store.names()
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		if !strings.HasPrefix(name, sourceRecordPrefix) {
			continue
		}
		var record sourceRecord
		found, err := store.get(name, &record)
		if err != nil {
			return nil, err
		}
		if found && record.HighWater > ledger.sequence {
			ledger.sequence = record.HighWater
		}
	}

	return ledger, nil
}

func (ledger *SourceLedger) Close() error { return ledger.steps.Close() }

func sourceRecordName(handoff output.HandoffID) (string, error) {
	if err := handoff.Validate(); err != nil {
		return "", err
	}

	return sourceRecordPrefix + string(handoff) + ".json", nil
}

func (ledger *SourceLedger) load(handoff output.HandoffID) (sourceRecord, bool, error) {
	name, err := sourceRecordName(handoff)
	if err != nil {
		return sourceRecord{}, false, err
	}
	var record sourceRecord
	found, err := ledger.store.get(name, &record)

	return record, found, err
}

func (ledger *SourceLedger) save(record sourceRecord) error {
	name, err := sourceRecordName(record.HandoffID)
	if err != nil {
		return err
	}

	return ledger.store.put(name, record)
}

// incarnationDir is the ONE derivation of a location from an identity.
//
// Every component comes from the server-issued incarnation, and the handle
// generation is in the name: a reused execution id at a new generation is a
// different directory, which is Req 7 expressed as a path rather than as a
// comment. The output name is validated by output.OutputName before it can
// reach here, and os.Root refuses a climb regardless.
func incarnationDir(incarnation output.SourceIncarnation) string {
	return fmt.Sprintf("%s.%d/%s",
		incarnation.ExecutionID, incarnation.HandleGeneration, incarnation.Output)
}

// ResolveIncarnation turns a server-issued identity into a location, and
// refuses everything else.
//
// The refusals matter more than the resolution. A stale handle generation, a
// foreign node, a foreign execution, a traversing or absolute output name and a
// symlink swapped under the path are all refused here, and they are refused by
// the runtime rather than by a string check: the resolution goes through
// os.Root, which will not follow a link out of the tree and will not accept a
// component that climbs.
func (ledger *SourceLedger) ResolveIncarnation(incarnation output.SourceIncarnation) (string, error) {
	if err := incarnation.Validate(); err != nil {
		return "", err
	}
	if incarnation.NodeUID != ledger.node {
		return "", fmt.Errorf("%w: the incarnation names node %s and this is %s",
			output.ErrUnauthorized, incarnation.NodeUID, ledger.node)
	}

	relative := incarnationDir(incarnation)

	// Lstat, not Stat: a symlink at the incarnation root must be REFUSED rather
	// than followed, even to a location inside the tree. The bytes a capture
	// seals are the ones the producer wrote, and a link is somebody saying they
	// are somewhere else.
	info, err := ledger.steps.Lstat(relative)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%w: no source incarnation %s is held on this node",
				output.ErrNotFound, relative)
		}

		return "", fmt.Errorf("%w: containment check on %s: %v",
			output.ErrUnauthorized, relative, err)
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return "", fmt.Errorf("%w: containment: the source path for %s is a symbolic link. The "+
			"bytes a capture seals are the ones the producer wrote, and a link is somebody "+
			"saying they are somewhere else", output.ErrUnauthorized, relative)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: containment: the source path for %s is not a directory",
			output.ErrUnauthorized, relative)
	}

	return path.Join(ledger.stepsPath, relative), nil
}

// Holds reports whether the incarnation's bytes are still on this node. It is
// an outcome, not a call count: "the daemon was not asked to delete it" is not
// something this file can say and not something a caller should want.
func (ledger *SourceLedger) Holds(incarnation output.SourceIncarnation) bool {
	_, err := ledger.ResolveIncarnation(incarnation)

	return err == nil
}

// admitted is the precondition every acting operation shares: this handoff is
// known here, under this epoch, at the base execution's current fence.
func (ledger *SourceLedger) admitted(handoff output.HandoffID,
	execution executioncontrol.Identity, epoch executioncontrol.ActivationEpoch) (sourceRecord, error) {
	record, found, err := ledger.load(handoff)
	if err != nil {
		return sourceRecord{}, err
	}
	if !found {
		return sourceRecord{}, fmt.Errorf("%w: handoff %s holds no source on this node",
			output.ErrNotFound, handoff)
	}
	if epoch != ledger.epoch {
		return sourceRecord{}, fmt.Errorf("%w: the request names epoch %d and this node is "+
			"attested for %d", output.ErrConflict, epoch, ledger.epoch)
	}
	if execution.ExecutionID != record.Execution.ExecutionID {
		return sourceRecord{}, fmt.Errorf("%w: handoff %s belongs to execution %s and the request "+
			"names %s", output.ErrInvalidIdentity, handoff,
			record.Execution.ExecutionID, execution.ExecutionID)
	}

	// The fence is the BASE ledger's, read fresh. The extension does not keep a
	// second copy of the execution's fence, because two copies is how a
	// takeover ends up honoured on one side and not the other.
	current, _, err := ledger.base.Admission(record.Execution.ExecutionID)
	if err != nil {
		return sourceRecord{}, err
	}
	if execution.Fence < current.Fence {
		return sourceRecord{}, fmt.Errorf("%w: fence %d was superseded by %d",
			executioncontrol.ErrStaleFence, execution.Fence, current.Fence)
	}
	if execution.Fence > current.Fence {
		return sourceRecord{}, fmt.Errorf("%w: fence %d has not been admitted; this node holds %d",
			output.ErrUnauthorized, execution.Fence, current.Fence)
	}

	return record, nil
}

func (ledger *SourceLedger) next() executioncontrol.LedgerSequence {
	ledger.sequence++

	return ledger.sequence
}

// AcknowledgeHold establishes the pre-start, non-authorizing hold.
//
// The second parameter is the SourceControl interface's, and it is deliberately
// ignored: a caller offering an incarnation is offering a name it chose. What
// comes back is the one this daemon minted.
func (ledger *SourceLedger) AcknowledgeHold(_ context.Context, admission output.CaptureAdmission,
	_ output.SourceIncarnation) (output.CaptureAcknowledgement, error) {
	if err := admission.Validate(); err != nil {
		return output.CaptureAcknowledgement{}, err
	}
	if admission.ActivationEpoch != ledger.epoch {
		return output.CaptureAcknowledgement{}, fmt.Errorf(
			"%w: the admission names epoch %d and this node is attested for %d",
			output.ErrConflict, admission.ActivationEpoch, ledger.epoch)
	}

	ledger.mu.Lock()
	defer ledger.mu.Unlock()

	record, found, err := ledger.load(admission.HandoffID)
	if err != nil {
		return output.CaptureAcknowledgement{}, err
	}
	// An existing hold is compared BEFORE the base admission is consulted, and
	// the order is the answer's meaning. A repeat that names different facts is
	// a CONFLICT -- this handoff already holds a source for other facts -- and
	// saying "unauthorized" there would tell a caller its fence was wrong when
	// what is wrong is that it is reusing a handoff. Nothing is written on this
	// path, so consulting the record first grants no authority.
	if found && record.Hold != nil {
		// Idempotent replay for the SAME facts, and a typed conflict for any
		// different one. "Repeating the handoff returns the same state" passes
		// for a daemon that ignores the identity entirely; comparing every fact
		// is what makes the replay mean something.
		switch {
		case record.Execution != admission.Execution:
			return output.CaptureAcknowledgement{}, fmt.Errorf(
				"%w: handoff %s holds a source for execution %s at fence %d and this hold names "+
					"%s at fence %d", output.ErrConflict, admission.HandoffID,
				record.Execution.ExecutionID, record.Execution.Fence,
				admission.Execution.ExecutionID, admission.Execution.Fence)
		case record.SourceLeaseID != admission.SourceLeaseID:
			return output.CaptureAcknowledgement{}, fmt.Errorf(
				"%w: handoff %s holds source lease %s and this hold names %s",
				output.ErrConflict, admission.HandoffID, record.SourceLeaseID, admission.SourceLeaseID)
		case record.Output != admission.Output:
			return output.CaptureAcknowledgement{}, fmt.Errorf(
				"%w: handoff %s holds the source for output %q and this hold names %q",
				output.ErrConflict, admission.HandoffID, record.Output, admission.Output)
		case record.State == sourceReleased:
			return output.CaptureAcknowledgement{}, fmt.Errorf(
				"%w: handoff %s released its source; a released hold is not re-established",
				output.ErrConflict, admission.HandoffID)
		}

		return *record.Hold, nil
	}

	// A NEW hold, and only now does base admission matter. A hold over an
	// execution the control plane never admitted would be a capture with no
	// exact truth behind it, which is the whole thing the base protocol is for.
	current, pod, err := ledger.base.Admission(admission.Execution.ExecutionID)
	if err != nil {
		return output.CaptureAcknowledgement{}, err
	}
	if current.Fence != admission.Execution.Fence {
		return output.CaptureAcknowledgement{}, fmt.Errorf(
			"%w: execution %s is admitted at fence %d and the hold names %d",
			output.ErrUnauthorized, admission.Execution.ExecutionID,
			current.Fence, admission.Execution.Fence)
	}

	// A new handle generation for every hold on this node, taken from the same
	// monotonic sequence as everything else. A reused generation is precisely
	// the confusion SourceIncarnation exists to prevent.
	incarnation := output.SourceIncarnation{
		ExecutionID:      admission.Execution.ExecutionID,
		NodeUID:          ledger.node,
		HandleGeneration: output.HandleGeneration(ledger.next()),
		Output:           admission.Output,
	}
	if err := ledger.steps.MkdirAll(incarnationDir(incarnation), 0o700); err != nil {
		return output.CaptureAcknowledgement{}, fmt.Errorf(
			"%w: creating the source incarnation: %v", output.ErrInfrastructure, err)
	}

	ack, err := ledger.signer.SignCapture(output.CaptureAcknowledgement{
		ProtocolVersion: output.ProtocolVersion,
		Kind:            output.CaptureHoldAcknowledged,
		Execution:       admission.Execution,
		ActivationEpoch: admission.ActivationEpoch,
		LedgerSequence:  ledger.next(),
		NodeUID:         ledger.node,
		PodUID:          pod,
		HandoffID:       admission.HandoffID,
		SourceLeaseID:   admission.SourceLeaseID,
		Incarnation:     incarnation,
		ObservedAt:      output.NewTimestamp(ledger.clock()),
	})
	if err != nil {
		return output.CaptureAcknowledgement{}, err
	}

	record = sourceRecord{
		State:           sourceHeld,
		Execution:       admission.Execution,
		ActivationEpoch: admission.ActivationEpoch,
		HandoffID:       admission.HandoffID,
		SourceLeaseID:   admission.SourceLeaseID,
		Output:          admission.Output,
		Incarnation:     incarnation,
		Hold:            &ack,
		HighWater:       ledger.sequence,
	}
	if err := ledger.save(record); err != nil {
		return output.CaptureAcknowledgement{}, err
	}

	// The hold is a cleanup gate on the base execution, by opaque name. This is
	// the only thing the base ledger ever learns about a capture.
	if err := ledger.base.OpenGate(admission.Execution, SourceHoldGate); err != nil {
		return output.CaptureAcknowledgement{}, err
	}

	return ack, nil
}

// InspectHold returns the statement in force, and invents nothing.
func (ledger *SourceLedger) InspectHold(handoff output.HandoffID) (output.CaptureAcknowledgement, error) {
	record, found, err := ledger.load(handoff)
	if err != nil {
		return output.CaptureAcknowledgement{}, err
	}
	if !found || record.Hold == nil {
		return output.CaptureAcknowledgement{}, fmt.Errorf(
			"%w: handoff %s holds no source on this node", output.ErrNotFound, handoff)
	}

	return *record.Hold, nil
}

// AdmitWriter issues a ticket, or refuses because sealing won the race.
//
// The whole method runs under one lock with BeginSeal, which is what makes the
// two outcomes exhaustive. There is no window in which a ticket is issued into
// a set that was captured a moment before.
func (ledger *SourceLedger) AdmitWriter(_ context.Context, admission output.WriterAdmission) (output.CaptureAcknowledgement, error) {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()

	record, ack, err := ledger.ticketStatement(admission, output.CaptureWriterTicketIssued)
	if err != nil {
		return output.CaptureAcknowledgement{}, err
	}
	if record.State != sourceHeld {
		return output.CaptureAcknowledgement{}, fmt.Errorf(
			"%w: the source for handoff %s is %s; no process and no new pod may receive a "+
				"write-capable mount for it", output.ErrSealed, admission.HandoffID, record.State)
	}
	for _, open := range record.Open {
		if open == admission.WriterTicketID {
			return *record.Hold, nil
		}
	}
	for _, closed := range record.Closed {
		if closed == admission.WriterTicketID {
			return output.CaptureAcknowledgement{}, fmt.Errorf(
				"%w: writer ticket %s was already retired; a ticket cannot be transferred to a "+
					"new process", output.ErrConflict, admission.WriterTicketID)
		}
	}

	record.Open = append(record.Open, admission.WriterTicketID)
	sort.Slice(record.Open, func(i, j int) bool { return record.Open[i] < record.Open[j] })
	record.HighWater = ledger.sequence
	if err := ledger.save(record); err != nil {
		return output.CaptureAcknowledgement{}, err
	}

	return ack, nil
}

// RetireWriter closes a ticket. It is idempotent: a writer that closed and
// whose caller lost the answer closes again to the same statement.
func (ledger *SourceLedger) RetireWriter(_ context.Context, admission output.WriterAdmission) (output.CaptureAcknowledgement, error) {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()

	record, ack, err := ledger.ticketStatement(admission, output.CaptureWriterTicketClosed)
	if err != nil {
		return output.CaptureAcknowledgement{}, err
	}

	remaining := make([]output.WriterTicketID, 0, len(record.Open))
	held := false
	for _, open := range record.Open {
		if open == admission.WriterTicketID {
			held = true

			continue
		}
		remaining = append(remaining, open)
	}
	if !held {
		for _, closed := range record.Closed {
			if closed == admission.WriterTicketID {
				return ack, nil
			}
		}

		return output.CaptureAcknowledgement{}, fmt.Errorf(
			"%w: writer ticket %s was never admitted over handoff %s",
			output.ErrNotFound, admission.WriterTicketID, admission.HandoffID)
	}
	record.Open = remaining
	record.Closed = append(record.Closed, admission.WriterTicketID)
	sort.Slice(record.Closed, func(i, j int) bool { return record.Closed[i] < record.Closed[j] })
	record.HighWater = ledger.sequence
	if err := ledger.save(record); err != nil {
		return output.CaptureAcknowledgement{}, err
	}

	return ack, nil
}

// ticketStatement is the shared validation and minting for the two ticket
// operations. Callers hold the lock.
func (ledger *SourceLedger) ticketStatement(admission output.WriterAdmission,
	kind output.CaptureAcknowledgementKind) (sourceRecord, output.CaptureAcknowledgement, error) {
	if err := admission.Validate(); err != nil {
		return sourceRecord{}, output.CaptureAcknowledgement{}, err
	}

	record, err := ledger.admitted(admission.HandoffID, admission.Execution, admission.ActivationEpoch)
	if err != nil {
		return sourceRecord{}, output.CaptureAcknowledgement{}, err
	}
	if admission.Incarnation != record.Incarnation {
		return sourceRecord{}, output.CaptureAcknowledgement{}, fmt.Errorf(
			"%w: the admission names an incarnation this node did not issue for handoff %s",
			output.ErrUnauthorized, admission.HandoffID)
	}

	ack, err := ledger.signer.SignCapture(output.CaptureAcknowledgement{
		ProtocolVersion: output.ProtocolVersion,
		Kind:            kind,
		Execution:       record.Execution,
		ActivationEpoch: record.ActivationEpoch,
		LedgerSequence:  ledger.next(),
		NodeUID:         ledger.node,
		PodUID:          admission.PodUID,
		HandoffID:       record.HandoffID,
		SourceLeaseID:   record.SourceLeaseID,
		Incarnation:     record.Incarnation,
		WriterTicketID:  admission.WriterTicketID,
		WriterFence:     admission.WriterFence,
		ObservedAt:      output.NewTimestamp(ledger.clock()),
	})

	return record, ack, err
}

// BeginSeal fences future admission and captures the drain set.
//
// It does not wait, and the reason is a deadlock rather than a preference: the
// ATC cannot terminate the pod writers it has not been told about, so a single
// blocking call would be each half waiting for the other.
func (ledger *SourceLedger) BeginSeal(_ context.Context, request output.SealRequest) (output.SealStarted, error) {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()

	record, err := ledger.admitted(request.HandoffID, request.Execution, request.ActivationEpoch)
	if err != nil {
		return output.SealStarted{}, err
	}
	if request.Incarnation != record.Incarnation {
		return output.SealStarted{}, fmt.Errorf(
			"%w: the seal names an incarnation this node did not issue for handoff %s",
			output.ErrUnauthorized, request.HandoffID)
	}
	if request.CaptureFence == 0 {
		return output.SealStarted{}, fmt.Errorf("%w: a seal names no capture fence",
			output.ErrIncomplete)
	}
	if record.State == sourceReleased {
		return output.SealStarted{}, fmt.Errorf("%w: handoff %s released its source",
			output.ErrConflict, request.HandoffID)
	}
	if record.SealStarted != nil {
		// Idempotent: the captured set is captured once. Re-capturing it on a
		// repeat is exactly the live-query defect this field exists to avoid.
		return output.SealStarted{
			Acknowledgement: *record.SealStarted,
			DrainSet:        append([]output.WriterTicketID(nil), record.DrainSet...),
		}, nil
	}

	ack, err := ledger.signer.SignCapture(output.CaptureAcknowledgement{
		ProtocolVersion: output.ProtocolVersion,
		Kind:            output.CaptureSealStarted,
		Execution:       record.Execution,
		ActivationEpoch: record.ActivationEpoch,
		LedgerSequence:  ledger.next(),
		NodeUID:         ledger.node,
		PodUID:          record.Hold.PodUID,
		HandoffID:       record.HandoffID,
		SourceLeaseID:   record.SourceLeaseID,
		Incarnation:     record.Incarnation,
		ObservedAt:      output.NewTimestamp(ledger.clock()),
	})
	if err != nil {
		return output.SealStarted{}, err
	}

	record.State = sourceSealing
	record.SealStarted = &ack
	record.DrainSet = append([]output.WriterTicketID(nil), record.Open...)
	record.HighWater = ledger.sequence
	if err := ledger.save(record); err != nil {
		return output.SealStarted{}, err
	}

	started := output.SealStarted{
		Acknowledgement: ack,
		DrainSet:        append([]output.WriterTicketID(nil), record.DrainSet...),
	}

	return started, started.Validate()
}

// ConfirmSeal takes the evidence for the captured set and nothing else.
//
// SealConfirmation.Validate already refuses evidence that does not cover the
// captured set exactly. What this adds is that the set it is checked against is
// the one THIS node captured, read from the record -- a caller cannot present
// its own SealStarted and have it believed.
func (ledger *SourceLedger) ConfirmSeal(_ context.Context, confirmation output.SealConfirmation) (output.CaptureAcknowledgement, error) {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()

	started := confirmation.Started.Acknowledgement
	record, err := ledger.admitted(started.HandoffID, started.Execution, started.ActivationEpoch)
	if err != nil {
		return output.CaptureAcknowledgement{}, err
	}
	if record.SealStarted == nil {
		return output.CaptureAcknowledgement{}, fmt.Errorf(
			"%w: handoff %s has not begun sealing; there is no captured drain set to confirm "+
				"against", output.ErrSealUnconfirmed, started.HandoffID)
	}
	if record.SealStarted.Signature != started.Signature {
		return output.CaptureAcknowledgement{}, fmt.Errorf(
			"%w: the confirmation carries a seal statement this node did not make for handoff %s",
			output.ErrUnauthorized, started.HandoffID)
	}
	// The captured set is this node's, not the caller's. Replacing the offered
	// one closes the gap where a caller presents a shorter set.
	confirmation.Started.DrainSet = append([]output.WriterTicketID(nil), record.DrainSet...)
	if err := confirmation.Validate(); err != nil {
		return output.CaptureAcknowledgement{}, err
	}
	if record.SealConfirmed != nil {
		return *record.SealConfirmed, nil
	}
	for _, ticket := range record.Open {
		return output.CaptureAcknowledgement{}, fmt.Errorf(
			"%w: writer ticket %s is still open over handoff %s", output.ErrSealUnconfirmed,
			ticket, started.HandoffID)
	}

	ack, err := ledger.signer.SignCapture(output.CaptureAcknowledgement{
		ProtocolVersion: output.ProtocolVersion,
		Kind:            output.CaptureSealConfirmed,
		Execution:       record.Execution,
		ActivationEpoch: record.ActivationEpoch,
		LedgerSequence:  ledger.next(),
		NodeUID:         ledger.node,
		PodUID:          record.Hold.PodUID,
		HandoffID:       record.HandoffID,
		SourceLeaseID:   record.SourceLeaseID,
		Incarnation:     record.Incarnation,
		ObservedAt:      output.NewTimestamp(ledger.clock()),
	})
	if err != nil {
		return output.CaptureAcknowledgement{}, err
	}
	record.State = sourceSealed
	record.SealConfirmed = &ack
	record.HighWater = ledger.sequence
	if err := ledger.save(record); err != nil {
		return output.CaptureAcknowledgement{}, err
	}

	return ack, nil
}

// AcknowledgeRelease is the daemon half of an exact fenced source release.
//
// All three dispositions can reach here. The capture branch's case is narrow
// and it is the branch review's F7: a capture that terminally cancels or fails
// before the irreversible publish point has released nothing, and the source
// stays held until this statement exists.
//
// It is idempotent for the same intent and a conflict for a different one, and
// that distinction is what makes the pair crash-recoverable: a caller that lost
// the answer repeats the same intent id and gets the same statement, while a
// second intent for a source already released is a caller working from stale
// state.
func (ledger *SourceLedger) AcknowledgeRelease(_ context.Context, intent output.ReleaseIntent) (output.ReleaseAcknowledgement, error) {
	if err := intent.Validate(); err != nil {
		return output.ReleaseAcknowledgement{}, err
	}

	ledger.mu.Lock()
	defer ledger.mu.Unlock()

	record, err := ledger.admitted(intent.HandoffID, intent.Execution, intent.ActivationEpoch)
	if err != nil {
		return output.ReleaseAcknowledgement{}, err
	}
	if intent.Incarnation != record.Incarnation {
		return output.ReleaseAcknowledgement{}, fmt.Errorf(
			"%w: the release names an incarnation this node did not issue for handoff %s",
			output.ErrUnauthorized, intent.HandoffID)
	}
	if record.Release != nil {
		if record.ReleaseIntentID == intent.ReleaseIntentID {
			return *record.Release, nil
		}

		return output.ReleaseAcknowledgement{}, fmt.Errorf(
			"%w: handoff %s was released under intent %s and this release names %s; the source "+
				"is released once", output.ErrConflict, intent.HandoffID,
			record.ReleaseIntentID, intent.ReleaseIntentID)
	}

	ack, err := ledger.signer.SignRelease(output.ReleaseAcknowledgement{
		ProtocolVersion: output.ProtocolVersion,
		Disposition:     intent.Disposition,
		Execution:       record.Execution,
		ActivationEpoch: record.ActivationEpoch,
		HandoffID:       record.HandoffID,
		SourceLeaseID:   record.SourceLeaseID,
		ReleaseIntentID: intent.ReleaseIntentID,
		Incarnation:     record.Incarnation,
		LedgerSequence:  ledger.next(),
		ObservedAt:      output.NewTimestamp(ledger.clock()),
	})
	if err != nil {
		return output.ReleaseAcknowledgement{}, err
	}

	// The record is written BEFORE the bytes go. A crash between the two leaves
	// a released record over bytes that are still there, which a sweep can
	// finish; the other order leaves bytes gone with the plane still believing
	// they are held, which nothing can.
	record.State = sourceReleased
	record.ReleaseIntentID = intent.ReleaseIntentID
	record.Release = &ack
	record.Open, record.Closed = nil, nil
	record.HighWater = ledger.sequence
	if err := ledger.save(record); err != nil {
		return output.ReleaseAcknowledgement{}, err
	}
	if err := ledger.steps.RemoveAll(incarnationDir(record.Incarnation)); err != nil {
		return output.ReleaseAcknowledgement{}, fmt.Errorf(
			"%w: removing the released source incarnation: %v", output.ErrInfrastructure, err)
	}
	if err := ledger.base.CloseGate(record.Execution, SourceHoldGate); err != nil {
		return output.ReleaseAcknowledgement{}, err
	}

	return ack, nil
}

var _ output.SourceControl = (*SourceLedger)(nil)
