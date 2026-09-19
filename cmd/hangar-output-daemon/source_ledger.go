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
	// sourceReserved is the location set aside before the producing Pod
	// exists. It authorizes nothing -- no writer, no seal, no publication --
	// and it is nonetheless a state the read-only classifier answers "held"
	// for, because the ATC has already mounted the directory into a Pod that
	// is about to write into it. A reservation that did not withhold cleanup
	// would be a hostPath the sweeper could reclaim mid-write.
	sourceReserved sourceState = "reserved"

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
	SourceHoldID    output.SourceHoldID              `json:"source_hold_id"`
	Output          output.OutputName                `json:"output"`
	Incarnation     output.SourceIncarnation         `json:"incarnation"`

	// Hold is the signed statement itself, stored as signed. A replay hands
	// back these bytes.
	Hold *output.CaptureAcknowledgement `json:"hold,omitempty"`

	// Tickets is every writer ticket this source has issued, with the facts it
	// was issued for and the statements it was answered with.
	//
	// It is a list of RECORDS and not two lists of ids. Ids alone cannot say
	// which process holds a ticket -- so the same id from another Pod UID or at
	// another writer fence was the same writer -- and they cannot replay: a
	// re-presented ticket answered with the hold statement, and a re-presented
	// close minted a fresh signature. A statement this node made is a fact, and
	// a fact is stored rather than reproduced.
	//
	// Both open and closed tickets are kept. A ticket issued and closed before
	// the seal is not in the drain set; one issued and closed after it must
	// still be accounted for.
	Tickets []writerTicket `json:"writer_tickets,omitempty"`

	// DrainSet is captured at the instant admission is fenced and never
	// recomputed. Confirming against a live query instead is the defect this
	// field exists to make impossible.
	SealStarted   *output.CaptureAcknowledgement `json:"seal_started,omitempty"`
	DrainSet      []output.WriterTicketID        `json:"drain_set,omitempty"`
	SealConfirmed *output.CaptureAcknowledgement `json:"seal_confirmed,omitempty"`

	// SealDeadlineAt is the deadline the control plane composed on the DATABASE
	// clock and handed over with the seal. It is stored here, with the captured
	// drain set and for the same reason: a deadline that is recomputed on every
	// call is not a deadline. What it buys is defence in depth -- the deciding
	// clock is still the database's -- so that a control plane which lost track
	// of its own deadline cannot have a boundary confirmed hours after the
	// moment the evidence was supposed to cover.
	SealDeadlineAt output.Timestamp `json:"seal_deadline_at,omitzero"`

	// CaptureFence is the capture-ownership fence this source was last sealed
	// under, and it is the daemon's half of Req 10: "a stale owner may not
	// seal, publish, sign/register a receipt, finalize, or release".
	//
	// It is a DIFFERENT fence from the execution's, which is what made the gap:
	// `admitted` compares the base ledger's execution fence, and a capture-lease
	// takeover does not move that -- the incarnation is the same, the writer set
	// is the same, and the seal is idempotent across it by design. So every
	// stale-capture-owner refusal lived in PostgreSQL, and a superseded owner
	// that still held a valid capability could drive this node's seal
	// confirmation, canonical read, publication and attestation.
	//
	// It moves FORWARD only, and it follows the lease rather than pinning the
	// first value it saw: the control plane's lease is the authority for who
	// owns a capture, and a takeover's BeginSeal is how this node learns.
	CaptureFence output.CaptureFence `json:"capture_fence,omitempty"`

	// Release is the fenced release pair's second half, stored so a repeat of
	// the same intent returns the same statement and a different intent is a
	// conflict.
	ReleaseIntentID output.ReleaseIntentID         `json:"release_intent_id,omitempty"`
	Release         *output.ReleaseAcknowledgement `json:"release,omitempty"`

	HighWater executioncontrol.LedgerSequence `json:"high_water"`
}

// writerTicket is one ticket: who was issued it, and what this node said.
//
// PodUID and WriterFence are the ticket's IDENTITY, not decoration on it. Req
// 13 says a ticket cannot be transferred to a new process, Pod UID, handle
// generation or fence epoch; the incarnation carries the generation and the
// admission's execution carries the epoch, and these two are the rest.
type writerTicket struct {
	TicketID    output.WriterTicketID          `json:"writer_ticket_id"`
	PodUID      executioncontrol.PodUID        `json:"pod_uid"`
	WriterFence output.WriterFence             `json:"writer_fence"`
	Issued      output.CaptureAcknowledgement  `json:"issued"`
	Closed      *output.CaptureAcknowledgement `json:"closed,omitempty"`
}

// ticket finds one by id. The slice is small -- it is the writers of one
// source -- and keeping it a slice keeps the record's JSON an ordered thing a
// person can read.
func (record sourceRecord) ticket(id output.WriterTicketID) (writerTicket, bool) {
	for _, ticket := range record.Tickets {
		if ticket.TicketID == id {
			return ticket, true
		}
	}

	return writerTicket{}, false
}

// openTickets is what a seal captures: the tickets that have not been retired.
func (record sourceRecord) openTickets() []output.WriterTicketID {
	open := make([]output.WriterTicketID, 0, len(record.Tickets))
	for _, ticket := range record.Tickets {
		if ticket.Closed == nil {
			open = append(open, ticket.TicketID)
		}
	}
	sort.Slice(open, func(i, j int) bool { return open[i] < open[j] })

	return open
}

// sameWriter is the transfer refusal. The id matching is not enough: a ticket
// re-presented from another pod, or at another writer fence, is a different
// process wearing the same name.
func (ticket writerTicket) sameWriter(admission output.WriterAdmission) error {
	if ticket.PodUID != admission.PodUID {
		return fmt.Errorf("%w: writer ticket %s was issued to pod %s and this request names %s; "+
			"a ticket cannot be transferred to a new process", output.ErrConflict,
			ticket.TicketID, ticket.PodUID, admission.PodUID)
	}
	if ticket.WriterFence != admission.WriterFence {
		return fmt.Errorf("%w: writer ticket %s was issued at writer fence %d and this request "+
			"names %d; a ticket cannot be transferred to a new fence epoch", output.ErrConflict,
			ticket.TicketID, ticket.WriterFence, admission.WriterFence)
	}

	return nil
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
	// One derivation, and it lives in the contract package because the
	// read-only classifier keys on the identical string. Two spellings of this
	// is a guard that answers "unmanaged" for a directory a capture holds.
	return incarnation.Directory()
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
	//
	// A HARD link is the gap in that sentence, and what closes it is the mount
	// shape rather than this check. tarDirectory classifies by entry.Info(),
	// which is an lstat, so a hard link is an ordinary regular file and its
	// bytes are copied into the sealed tree -- measured: a file created outside
	// the capture root and hard-linked in publishes cleanly, while every
	// symlink spelling of the same move is refused. It is closed today because
	// the managed hostPath is mounted into INIT containers only (read-only for
	// the daemon's own, read-write for the cleanup one) and never into the main
	// container or a sidecar, which receive the per-output volumes instead. So
	// the producer and its sidecars can see no path to link FROM. If that mount
	// shape ever changes, the hard-link case reopens and this check does not
	// catch it.
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
// HasIncarnation reports whether this incarnation's directory is still on this
// node.
//
// It is a question about BYTES and not about the hold, and the two are separate
// questions now: a release closes the hold and leaves the bytes to the artifact
// daemon's ordinary lifecycle, so after a release this stays true and Released
// below turns true. They were one question while a release deleted, which is
// exactly the conflation that let every settlement destroy a step's output.
func (ledger *SourceLedger) HasIncarnation(incarnation output.SourceIncarnation) bool {
	_, err := ledger.ResolveIncarnation(incarnation)

	return err == nil
}

// Released reports whether this handoff's hold has been released.
//
// The hold is a RECORD, and this reads it. Nothing about the directory: the
// bytes outlive the hold by design.
func (ledger *SourceLedger) Released(handoff output.HandoffID) (bool, error) {
	record, found, err := ledger.load(handoff)
	if err != nil {
		return false, err
	}
	if !found {
		return false, fmt.Errorf("%w: handoff %s holds no source on this node",
			output.ErrNotFound, handoff)
	}

	return record.State == sourceReleased, nil
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
	current, err := ledger.base.Admission(record.Execution.ExecutionID)
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

// admitCapture is `admitted` plus the CAPTURE fence, and it is the precondition
// every capture-facet operation past the seal shares.
//
// The two fences answer different questions and neither substitutes for the
// other. The execution fence says a replacement Pod has not superseded this
// writer; the capture fence says a second coordinator has not taken ownership
// of this capture. A takeover moves only the second, so `admitted` alone would
// serve a superseded owner every operation this node has.
//
// A HIGHER fence moves the stored one forward, and that is what makes the check
// engage on the takeover it was written for. BeginSeal advances it too, but a
// takeover past the seal never calls BeginSeal -- the captured drain set is
// captured once and the new owner inherits it, which is why every crash-half
// spec asserts `begin-seal == 1` across a takeover. So the node learned a new
// owner only on the takeover that did not need teaching, and served the
// superseded fence on the one that did. The new owner's first capture-facet
// call is the lesson, whichever call that is.
//
// Monotonic, and never downward: the fence a lease has issued is a fact, and a
// node that could be talked backwards would be a node a stale owner could
// re-admit itself at. The durable refusal is Postgres's regardless -- a stale
// owner is refused at the lease before it reaches this node at all.
func (ledger *SourceLedger) admitCapture(handoff output.HandoffID,
	execution executioncontrol.Identity, epoch executioncontrol.ActivationEpoch,
	fence output.CaptureFence) (sourceRecord, error) {
	record, err := ledger.admitted(handoff, execution, epoch)
	if err != nil {
		return sourceRecord{}, err
	}
	if fence == 0 {
		return sourceRecord{}, fmt.Errorf("%w: a capture operation over handoff %s names no "+
			"capture fence", output.ErrIncomplete, handoff)
	}
	if fence < record.CaptureFence {
		return sourceRecord{}, fmt.Errorf("%w: capture fence %d over handoff %s was superseded "+
			"by %d; a stale owner may not seal, publish, sign, finalize or release",
			executioncontrol.ErrStaleFence, fence, handoff, record.CaptureFence)
	}
	if fence > record.CaptureFence {
		record.CaptureFence = fence
		if err := ledger.save(record); err != nil {
			return sourceRecord{}, err
		}
	}

	return record, nil
}

// AdmitCaptureFence is the fence check on its own, for the attestation route,
// and it answers with the WRITER fence this node admitted for that source.
//
// Signing a receipt reads no source bytes -- it is a stat against the object
// store -- so it has no other reason to reach this ledger, and Req 10 lists
// signing among the things a stale owner may not do.
//
// The writer fence comes back because a receipt claims one, and it was the only
// claim in the whole receipt taken verbatim from the caller. The control plane
// filled it with a CAST OF THE CAPTURE FENCE and then revalidated it against
// that same capture fence, so the comparison could not fail for any receipt
// this plane produced -- while Req 25 names the two as separate bound claims
// and AC 9 asks for tamper and replay across both. They are separate fences
// with separate writers: a capture-lease takeover moves the capture fence and
// leaves writer admission exactly where it was. Nothing observed the
// conflation because the writer fence is the constant 1 today.
//
// This node is the authority for it. The source ledger is where writer tickets
// are issued and where the fence they were issued at is durable, so the
// attestation takes the value from here the way it already takes the ref from a
// fresh stat and the epoch from the daemon's own namespace.
func (ledger *SourceLedger) AdmitCaptureFence(handoff output.HandoffID,
	execution executioncontrol.Identity, epoch executioncontrol.ActivationEpoch,
	fence output.CaptureFence) (output.WriterFence, error) {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()

	record, err := ledger.admitCapture(handoff, execution, epoch, fence)
	if err != nil {
		return 0, err
	}

	return record.writerFence(), nil
}

// writerFence is the writer-admission epoch this node has admitted for the
// source: the highest fence any ticket was issued at, floored at the fence of
// an incarnation nobody has been fenced out of.
//
// The floor is not a guess. A capture whose producer wrote nothing has no
// ticket and still has a writer-admission epoch, and a receipt must be able to
// claim it: ReceiptClaims.Validate refuses zero.
func (record sourceRecord) writerFence() output.WriterFence {
	fence := output.FirstWriterFence
	for _, ticket := range record.Tickets {
		if ticket.WriterFence > fence {
			fence = ticket.WriterFence
		}
	}

	return fence
}

func (ledger *SourceLedger) next() executioncontrol.LedgerSequence {
	ledger.sequence++

	return ledger.sequence
}

// ReserveIncarnation sets aside the one location this capture will hold,
// before the producing Pod exists.
//
// This is the operation that closes the Phase 4 seam. The incarnation used to
// be minted inside AcknowledgeHold, which cannot run before the Pod exists --
// the hold binds to the admitted Pod UID -- so the ATC had no location to mount
// and the producer wrote into `steps/<handle>/<output>`, a SIBLING of the
// directory the hold protected. Reserving first is what makes the bytes the
// hold protects the bytes the producer writes.
//
// It still issues rather than accepts. The caller sends the same admission a
// hold sends -- an exact execution and its fence, a handoff, a source hold, a
// declared output, an epoch -- and gets back a directory this daemon chose. Req
// 7 is intact: no API here accepts a path, and the ATC's whole part is to
// repeat the answer.
//
// It authorizes nothing. A reservation is not a hold: no writer ticket may be
// issued against it, nothing may be sealed, and the producer's main process
// still may not start until the control init's hold is durably acknowledged.
// What it does do is withhold destructive cleanup from the moment the directory
// exists, because from that moment the ATC may have mounted it.
func (ledger *SourceLedger) ReserveIncarnation(_ context.Context,
	admission output.CaptureAdmission) (output.ReservedIncarnation, error) {
	if err := admission.Validate(); err != nil {
		return output.ReservedIncarnation{}, err
	}
	if admission.ActivationEpoch != ledger.epoch {
		return output.ReservedIncarnation{}, fmt.Errorf(
			"%w: the reservation names epoch %d and this node is attested for %d",
			output.ErrConflict, admission.ActivationEpoch, ledger.epoch)
	}

	ledger.mu.Lock()
	defer ledger.mu.Unlock()

	record, found, err := ledger.load(admission.HandoffID)
	if err != nil {
		return output.ReservedIncarnation{}, err
	}
	if found {
		// Idempotent for the SAME facts, a typed conflict for any different
		// one, and the same comparison the hold makes -- because a reservation
		// and a hold are admitted for exactly the same facts, and two
		// comparisons of one set is how they come to disagree.
		if err := conflictsWithRecord(record, admission); err != nil {
			return output.ReservedIncarnation{}, err
		}

		return ledger.reservationOf(record), nil
	}

	// A NEW reservation, and only now does base admission matter. A location
	// set aside for an execution the control plane never admitted would be a
	// directory nothing ever comes back for.
	current, err := ledger.base.Admission(admission.Execution.ExecutionID)
	if err != nil {
		return output.ReservedIncarnation{}, err
	}
	if current.Fence != admission.Execution.Fence {
		return output.ReservedIncarnation{}, fmt.Errorf(
			"%w: execution %s is admitted at fence %d and the reservation names %d",
			output.ErrUnauthorized, admission.Execution.ExecutionID,
			current.Fence, admission.Execution.Fence)
	}

	incarnation := output.SourceIncarnation{
		ExecutionID:      admission.Execution.ExecutionID,
		NodeUID:          ledger.node,
		HandleGeneration: output.HandleGeneration(ledger.next()),
		Output:           admission.Output,
	}
	if err := ledger.steps.MkdirAll(incarnationDir(incarnation), 0o700); err != nil {
		return output.ReservedIncarnation{}, fmt.Errorf(
			"%w: creating the reserved source incarnation: %v", output.ErrInfrastructure, err)
	}

	record = sourceRecord{
		State:           sourceReserved,
		Execution:       admission.Execution,
		ActivationEpoch: admission.ActivationEpoch,
		HandoffID:       admission.HandoffID,
		SourceHoldID:    admission.SourceHoldID,
		Output:          admission.Output,
		Incarnation:     incarnation,
		HighWater:       ledger.sequence,
	}
	// The gate goes down BEFORE the record, exactly as it does for a hold, and
	// for the same crash: a gate with no record withholds cleanup and is closed
	// by the release; a record with no gate is a directory the ATC has mounted
	// that cleanup is told it may destroy.
	if err := ledger.base.OpenGate(admission.Execution, SourceHoldGate); err != nil {
		return output.ReservedIncarnation{}, err
	}
	if err := ledger.save(record); err != nil {
		return output.ReservedIncarnation{}, err
	}

	return ledger.reservationOf(record), nil
}

// reservationOf answers from the durable record, never from a field kept in
// memory: a reservation that a restart could not reproduce is a hostPath the
// ATC mounted into a Pod that outlives this process.
func (ledger *SourceLedger) reservationOf(record sourceRecord) output.ReservedIncarnation {
	return output.ReservedIncarnation{
		ProtocolVersion: output.ProtocolVersion,
		Execution:       record.Execution,
		ActivationEpoch: record.ActivationEpoch,
		HandoffID:       record.HandoffID,
		SourceHoldID:    record.SourceHoldID,
		NodeUID:         record.Incarnation.NodeUID,
		Incarnation:     record.Incarnation,
		Directory:       incarnationDir(record.Incarnation),
		LedgerSequence:  record.HighWater,
		ObservedAt:      output.NewTimestamp(ledger.clock()),
	}
}

// conflictsWithRecord is the one fact comparison a reservation and a hold both
// make. It is a function rather than two copies because the two operations
// admit the same facts, and the failure mode of two copies is a reservation
// that accepts what the hold then refuses -- with a Pod already built around
// the answer.
func conflictsWithRecord(record sourceRecord, admission output.CaptureAdmission) error {
	switch {
	case record.Execution != admission.Execution:
		return fmt.Errorf(
			"%w: handoff %s holds a source for execution %s at fence %d and this request names "+
				"%s at fence %d", output.ErrConflict, admission.HandoffID,
			record.Execution.ExecutionID, record.Execution.Fence,
			admission.Execution.ExecutionID, admission.Execution.Fence)
	case record.SourceHoldID != admission.SourceHoldID:
		return fmt.Errorf("%w: handoff %s holds source hold %s and this request names %s",
			output.ErrConflict, admission.HandoffID, record.SourceHoldID, admission.SourceHoldID)
	case record.Output != admission.Output:
		return fmt.Errorf("%w: handoff %s holds the source for output %q and this request names %q",
			output.ErrConflict, admission.HandoffID, record.Output, admission.Output)
	case record.State == sourceReleased:
		return fmt.Errorf("%w: handoff %s released its source; a released reservation is not "+
			"re-established", output.ErrConflict, admission.HandoffID)
	}

	return nil
}

// AcknowledgeHold establishes the pre-start, non-authorizing hold, over the
// incarnation this daemon already reserved, and binds the Pod UID.
//
// The second parameter used to be ignored, on the reading that a caller
// offering an incarnation was offering a name it chose. That reading is no
// longer available and the change is the point: the incarnation is now issued
// at ReserveIncarnation, before the Pod exists, so a control init presenting
// one is not choosing a location -- it is PROVING it received the daemon's
// answer, which is the same answer the ATC mounted into the Pod it is running
// in. A hold that named nothing, or named something else, would be a hold over
// bytes no producer is writing.
//
// The third is where the Pod UID enters this protocol, and it enters here
// because here is the first moment it exists. Admission and reservation both
// precede the Pod -- the ATC mounts the reservation into the Pod it has not
// created yet -- so neither can bind one; the capture control init reads
// `metadata.uid` off the Downward API inside the Pod and presents it. It is
// bound ONCE: the first hold writes it into the durable statement and a later
// hold on the same handoff must present the same one. A different UID at the
// same fence is a typed conflict rather than a rebinding, because a replaced
// Pod is a new incarnation that may not inherit a hold over bytes the previous
// one was writing.
func (ledger *SourceLedger) AcknowledgeHold(_ context.Context, admission output.CaptureAdmission,
	offered output.SourceIncarnation, pod executioncontrol.PodUID) (output.CaptureAcknowledgement, error) {
	if err := admission.Validate(); err != nil {
		return output.CaptureAcknowledgement{}, err
	}
	if admission.ActivationEpoch != ledger.epoch {
		return output.CaptureAcknowledgement{}, fmt.Errorf(
			"%w: the admission names epoch %d and this node is attested for %d",
			output.ErrConflict, admission.ActivationEpoch, ledger.epoch)
	}
	// A hold with no Pod is refused rather than recorded empty. An empty UID
	// compares equal to the next empty one, so a hold that skipped this would
	// bind nothing and then admit every later Pod as "the same" -- which is the
	// precise shape of the defect this parameter exists to close.
	if pod == "" {
		return output.CaptureAcknowledgement{}, fmt.Errorf(
			"%w: the hold names no pod. The capture control init reads metadata.uid from the "+
				"Downward API and presents it; a hold that carries none is not running in a Pod "+
				"this daemon can bind a writer to", output.ErrIncomplete)
	}
	// The reserving node has the last word on where a hold may be taken. The
	// reservation is a directory on ONE node's disk, so a Pod that the
	// scheduler placed elsewhere reaches its own node's daemon, which reserved
	// nothing -- and `DirectoryOrCreate` would have made it an empty unheld
	// directory. This is the typed refusal for it, ahead of the record lookup
	// so the answer names the real problem rather than "no such handoff".
	if offered.NodeUID != "" && offered.NodeUID != ledger.node {
		return output.CaptureAcknowledgement{}, fmt.Errorf(
			"%w: the hold names an incarnation reserved on node %s and this daemon is node %s. "+
				"A reservation is a directory on the reserving node's disk; a producer scheduled "+
				"elsewhere holds nothing", output.ErrUnauthorized, offered.NodeUID, ledger.node)
	}

	ledger.mu.Lock()
	defer ledger.mu.Unlock()

	record, found, err := ledger.load(admission.HandoffID)
	if err != nil {
		return output.CaptureAcknowledgement{}, err
	}
	// A hold is bound to a RESERVATION, and there is no path that mints one
	// here any more. The ATC reserves before it builds the Pod and mounts the
	// answer as the selected output's volume; a hold with no reservation behind
	// it is a control init in a Pod whose output volume is somewhere else.
	if !found {
		return output.CaptureAcknowledgement{}, fmt.Errorf(
			"%w: handoff %s has no reserved source incarnation on this node. A hold is bound to "+
				"a location this daemon issued before the Pod was built, and the producer's "+
				"output volume is that location", output.ErrNotFound, admission.HandoffID)
	}
	// The facts are compared BEFORE the base admission is consulted, and the
	// order is the answer's meaning. A repeat that names different facts is a
	// CONFLICT -- this handoff already holds a source for other facts -- and
	// saying "unauthorized" there would tell a caller its fence was wrong when
	// what is wrong is that it is reusing a handoff. Nothing is written on this
	// path, so consulting the record first warrants no authority.
	if err := conflictsWithRecord(record, admission); err != nil {
		return output.CaptureAcknowledgement{}, err
	}
	// And the incarnation itself. An empty one is refused rather than resolved
	// from the record: a caller that cannot name the location it was given has
	// not proved it is the Pod the reservation was made for, and "the daemon
	// filled it in" is how a hold over the wrong generation goes unnoticed.
	if offered != record.Incarnation {
		return output.CaptureAcknowledgement{}, fmt.Errorf(
			"%w: handoff %s reserved incarnation %s and this hold names %s. A hold binds to the "+
				"location the daemon reserved, which is the one the producer's Pod mounts",
			output.ErrConflict, admission.HandoffID,
			incarnationDir(record.Incarnation), incarnationDir(offered))
	}

	if record.Hold != nil {
		// Bound once. The stored statement names the Pod this hold is for, and
		// a second hold naming a different one is a REPLACEMENT Pod reaching
		// for a hold it did not take -- the same fence, the same handoff, a new
		// incarnation. Phase 4 already treats that as takeover-by-replacement
		// and refuses it; saying so here is what keeps the ATC's own arm
		// (`hold.PodUID != p.exact.podUID`) from being the only door.
		if record.Hold.PodUID != pod {
			return output.CaptureAcknowledgement{}, fmt.Errorf(
				"%w: handoff %s holds its source for pod %s and this hold names %s. A recreated "+
					"Pod is a new incarnation and does not inherit the hold",
				output.ErrConflict, admission.HandoffID, record.Hold.PodUID, pod)
		}
		// The gate, before the statement. A hold is two durable writes -- the
		// record and the cleanup gate -- and a crash between them leaves a
		// held record with nothing withholding cleanup. So every replay
		// re-runs the second half; it is idempotent, and it is the fail-closed
		// direction, so a replay arriving at a superseded fence may run it
		// too. Without this the repair would only ever happen on a path that
		// no longer needs it.
		if err := ledger.base.EnsureGateOpen(record.Execution.ExecutionID, SourceHoldGate); err != nil {
			return output.CaptureAcknowledgement{}, err
		}

		return *record.Hold, nil
	}

	// A NEW hold over the reserved incarnation, and only now does base
	// admission matter. A hold over an execution the control plane never
	// admitted would be a capture with no exact truth behind it, which is the
	// whole thing the base protocol is for. The base admission is asked for the
	// FENCE and nothing else: it has no Pod to give, because it was made before
	// the Pod existed.
	current, err := ledger.base.Admission(admission.Execution.ExecutionID)
	if err != nil {
		return output.CaptureAcknowledgement{}, err
	}
	if current.Fence != admission.Execution.Fence {
		return output.CaptureAcknowledgement{}, fmt.Errorf(
			"%w: execution %s is admitted at fence %d and the hold names %d",
			output.ErrUnauthorized, admission.Execution.ExecutionID,
			current.Fence, admission.Execution.Fence)
	}

	// The incarnation is the RESERVATION's. Nothing here mints one: a hold that
	// issued a fresh handle generation would name a directory the Pod running
	// this init container never mounted.
	incarnation := record.Incarnation
	// Idempotent, and the fail-closed direction: the reservation created this
	// directory, and a hold that found it gone would otherwise acknowledge a
	// source that is not there.
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
		SourceHoldID:    admission.SourceHoldID,
		Incarnation:     incarnation,
		ObservedAt:      output.NewTimestamp(ledger.clock()),
	})
	if err != nil {
		return output.CaptureAcknowledgement{}, err
	}

	record.State = sourceHeld
	record.Hold = &ack
	record.HighWater = ledger.sequence
	// The gate goes down BEFORE the record, and the order is the crash. A gate
	// with no hold is the safe orphan: it withholds cleanup, and the release --
	// or the next hold on this handoff -- closes it. A hold with no gate is the
	// other one, and it is a held source that cleanup is told it may destroy.
	//
	// The gate is an opaque name, and it is the only thing the base ledger ever
	// learns about a capture.
	if err := ledger.base.OpenGate(admission.Execution, SourceHoldGate); err != nil {
		return output.CaptureAcknowledgement{}, err
	}
	if err := ledger.save(record); err != nil {
		return output.CaptureAcknowledgement{}, err
	}

	return ack, nil
}

// InspectHold returns the statement in force, and invents nothing.
//
// It takes the identity the CAPABILITY was bound to, not just the handoff. A
// read is not harmless here: the hold statement carries the incarnation, the
// pod and the source hold, and a route that verified a token against one
// execution and then answered about another's handoff would have made the
// binding decorative on every read.
func (ledger *SourceLedger) InspectHold(handoff output.HandoffID,
	execution executioncontrol.Identity) (output.CaptureAcknowledgement, error) {
	record, found, err := ledger.load(handoff)
	if err != nil {
		return output.CaptureAcknowledgement{}, err
	}
	if !found || record.Hold == nil {
		return output.CaptureAcknowledgement{}, fmt.Errorf(
			"%w: handoff %s holds no source on this node", output.ErrNotFound, handoff)
	}
	if err := record.belongsTo(execution, handoff); err != nil {
		return output.CaptureAcknowledgement{}, err
	}

	return *record.Hold, nil
}

// belongsTo is the ownership check the three query-shaped routes share.
//
// The fence is deliberately NOT compared: a superseded controller may read
// what this node said, and `admitted` is where acting requires the current
// fence. What may never happen is a DIFFERENT execution reading it.
func (record sourceRecord) belongsTo(execution executioncontrol.Identity,
	handoff output.HandoffID) error {
	if record.Execution.ExecutionID != execution.ExecutionID {
		return fmt.Errorf("%w: handoff %s belongs to execution %s and the request is "+
			"authorized for %s", output.ErrUnauthorized, handoff,
			record.Execution.ExecutionID, execution.ExecutionID)
	}

	return nil
}

// AdmitWriter issues a ticket, or refuses because sealing won the race.
//
// The whole method runs under one lock with BeginSeal, which is what makes the
// two outcomes exhaustive. There is no window in which a ticket is issued into
// a set that was captured a moment before.
func (ledger *SourceLedger) AdmitWriter(_ context.Context, admission output.WriterAdmission) (output.CaptureAcknowledgement, error) {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()

	record, err := ledger.admittedWriter(admission)
	if err != nil {
		return output.CaptureAcknowledgement{}, err
	}

	// The replay is decided BEFORE the state, and the order is the answer's
	// meaning: a writer that was issued a ticket and lost the answer must be
	// able to ask again after the seal began, and be told what it was told.
	// Deciding the state first would answer "sealed" to a writer that is in
	// the captured drain set.
	if existing, found := record.ticket(admission.WriterTicketID); found {
		if err := existing.sameWriter(admission); err != nil {
			return output.CaptureAcknowledgement{}, err
		}
		if existing.Closed != nil {
			return output.CaptureAcknowledgement{}, fmt.Errorf(
				"%w: writer ticket %s was already retired; a ticket cannot be transferred to a "+
					"new process", output.ErrConflict, admission.WriterTicketID)
		}

		return existing.Issued, nil
	}

	if record.State != sourceHeld {
		return output.CaptureAcknowledgement{}, fmt.Errorf(
			"%w: the source for handoff %s is %s; no process and no new pod may receive a "+
				"write-capable mount for it", output.ErrSealed, admission.HandoffID, record.State)
	}

	ack, err := ledger.signTicketStatement(record, admission, output.CaptureWriterTicketIssued)
	if err != nil {
		return output.CaptureAcknowledgement{}, err
	}
	record.Tickets = append(record.Tickets, writerTicket{
		TicketID:    admission.WriterTicketID,
		PodUID:      admission.PodUID,
		WriterFence: admission.WriterFence,
		Issued:      ack,
	})
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

	record, err := ledger.admittedWriter(admission)
	if err != nil {
		return output.CaptureAcknowledgement{}, err
	}

	existing, found := record.ticket(admission.WriterTicketID)
	if !found {
		return output.CaptureAcknowledgement{}, fmt.Errorf(
			"%w: writer ticket %s was never admitted over handoff %s",
			output.ErrNotFound, admission.WriterTicketID, admission.HandoffID)
	}
	if err := existing.sameWriter(admission); err != nil {
		return output.CaptureAcknowledgement{}, err
	}
	if existing.Closed != nil {
		// The stored statement, not a fresh one. A close is answered once and
		// the answer is a signed fact; minting another would give one event two
		// sequences, and a caller holding both could not say which was the
		// close.
		return *existing.Closed, nil
	}

	ack, err := ledger.signTicketStatement(record, admission, output.CaptureWriterTicketClosed)
	if err != nil {
		return output.CaptureAcknowledgement{}, err
	}
	for index := range record.Tickets {
		if record.Tickets[index].TicketID == admission.WriterTicketID {
			closed := ack
			record.Tickets[index].Closed = &closed
		}
	}
	record.HighWater = ledger.sequence
	if err := ledger.save(record); err != nil {
		return output.CaptureAcknowledgement{}, err
	}

	return ack, nil
}

// admittedWriter is the validation the two ticket operations share, and it
// mints nothing. Callers hold the lock.
//
// Splitting the minting out is what makes a replay a replay: the old shape
// signed a statement before it knew whether the ticket already had one, so
// every repeat burned a ledger sequence on an acknowledgement it discarded.
func (ledger *SourceLedger) admittedWriter(admission output.WriterAdmission) (sourceRecord, error) {
	if err := admission.Validate(); err != nil {
		return sourceRecord{}, err
	}

	record, err := ledger.admitted(admission.HandoffID, admission.Execution, admission.ActivationEpoch)
	if err != nil {
		return sourceRecord{}, err
	}
	if admission.Incarnation != record.Incarnation {
		return sourceRecord{}, fmt.Errorf(
			"%w: the admission names an incarnation this node did not issue for handoff %s",
			output.ErrUnauthorized, admission.HandoffID)
	}
	// The incarnation has to still BE the incarnation. Admitting a writer over
	// a path that has become a symlink, or is gone, would hand write capability
	// to bytes that are not the ones this capture will seal -- and the ticket
	// would then be in the drain set, accounted for, and completely misleading.
	if _, err := ledger.ResolveIncarnation(record.Incarnation); err != nil {
		return sourceRecord{}, err
	}
	// And the Pod the HOLD bound. Every later operation on a hold presents the
	// Pod UID the hold was taken for; a writer ticket is the write-capable one,
	// so this is where a Pod that never held anything is turned away rather
	// than issued a ticket that a seal would then have to drain.
	if record.Hold != nil && admission.PodUID != record.Hold.PodUID {
		return sourceRecord{}, fmt.Errorf(
			"%w: handoff %s holds its source for pod %s and this writer admission names %s",
			output.ErrConflict, admission.HandoffID, record.Hold.PodUID, admission.PodUID)
	}

	return record, nil
}

// signTicketStatement mints one ticket statement. Callers hold the lock and
// have already decided that there is no stored statement to return instead.
func (ledger *SourceLedger) signTicketStatement(record sourceRecord,
	admission output.WriterAdmission,
	kind output.CaptureAcknowledgementKind) (output.CaptureAcknowledgement, error) {
	return ledger.signer.SignCapture(output.CaptureAcknowledgement{
		ProtocolVersion: output.ProtocolVersion,
		Kind:            kind,
		Execution:       record.Execution,
		ActivationEpoch: record.ActivationEpoch,
		LedgerSequence:  ledger.next(),
		NodeUID:         ledger.node,
		PodUID:          admission.PodUID,
		HandoffID:       record.HandoffID,
		SourceHoldID:    record.SourceHoldID,
		Incarnation:     record.Incarnation,
		WriterTicketID:  admission.WriterTicketID,
		WriterFence:     admission.WriterFence,
		ObservedAt:      output.NewTimestamp(ledger.clock()),
	})
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
	// Same rule as writer admission, and for the sharper reason: a seal is the
	// promise that what follows reads exactly these bytes, and a source that
	// cannot be resolved is not a source anybody can promise anything about.
	if _, err := ledger.ResolveIncarnation(record.Incarnation); err != nil {
		return output.SealStarted{}, err
	}
	if record.State == sourceReleased {
		return output.SealStarted{}, fmt.Errorf("%w: handoff %s released its source",
			output.ErrConflict, request.HandoffID)
	}
	if request.CaptureFence < record.CaptureFence {
		return output.SealStarted{}, fmt.Errorf(
			"%w: capture fence %d over handoff %s was superseded by %d; a stale owner may not seal",
			executioncontrol.ErrStaleFence, request.CaptureFence, request.HandoffID,
			record.CaptureFence)
	}
	if record.SealStarted != nil {
		// Idempotent: the captured set is captured once. Re-capturing it on a
		// repeat is exactly the live-query defect this field exists to avoid.
		//
		// The FENCE still moves, and that is not a contradiction: a takeover
		// inherits the seal this node already made -- the incarnation and its
		// writer set have not changed -- and how this node learns that
		// ownership moved is the new owner's own BeginSeal. Pinning the first
		// fence seen would refuse every operation the takeover then owes.
		if request.CaptureFence > record.CaptureFence {
			record.CaptureFence = request.CaptureFence
			if err := ledger.save(record); err != nil {
				return output.SealStarted{}, err
			}
		}

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
		SourceHoldID:    record.SourceHoldID,
		Incarnation:     record.Incarnation,
		ObservedAt:      output.NewTimestamp(ledger.clock()),
	})
	if err != nil {
		return output.SealStarted{}, err
	}

	record.State = sourceSealing
	record.SealStarted = &ack
	record.SealDeadlineAt = request.DeadlineAt
	record.CaptureFence = request.CaptureFence
	record.DrainSet = record.openTickets()
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
	record, err := ledger.admitCapture(started.HandoffID, started.Execution,
		started.ActivationEpoch, confirmation.CaptureFence)
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
	// The deadline the seal was begun under. Checked AFTER the idempotent
	// return, because a confirmation that already happened is a fact and not a
	// request, and refusing to hand back a statement this node made would turn
	// a lost answer into a permanent one.
	//
	// The deciding clock is the database's -- this refusal is defence in depth,
	// and it is why it is typed as an unconfirmed seal rather than as a
	// conflict: the answer it gives the control plane is exactly the one the
	// control plane then weighs against its own deadline.
	if !record.SealDeadlineAt.IsZero() && ledger.clock().After(record.SealDeadlineAt.Time) {
		return output.CaptureAcknowledgement{}, fmt.Errorf(
			"%w: the seal over handoff %s was begun with a deadline of %s and this node's clock "+
				"reads %s; evidence for a boundary is evidence for the moment it covered",
			output.ErrSealUnconfirmed, started.HandoffID,
			record.SealDeadlineAt.UTC().Format(time.RFC3339), ledger.clock().UTC().Format(time.RFC3339))
	}
	for _, ticket := range record.openTickets() {
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
		SourceHoldID:    record.SourceHoldID,
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
			// The same repair, at the other end. A release is the record, then
			// the bytes, then the gate; a crash after the record leaves a gate
			// open over a source that is gone, and an execution that is never
			// cleanup-eligible again. Both halves below are idempotent.
			if err := ledger.finishRelease(intent.Execution, record); err != nil {
				return output.ReleaseAcknowledgement{}, err
			}

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
		SourceHoldID:    record.SourceHoldID,
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
	record.Tickets = nil
	record.HighWater = ledger.sequence
	if err := ledger.save(record); err != nil {
		return output.ReleaseAcknowledgement{}, err
	}
	if err := ledger.finishRelease(intent.Execution, record); err != nil {
		return output.ReleaseAcknowledgement{}, err
	}

	return ack, nil
}

// finishRelease is everything a release does after its record is durable, and
// it is idempotent so that a replay can re-run it.
//
// IT DOES NOT REMOVE THE BYTES, and that is the whole of the rule: a release
// releases the HOLD. The incarnation is the step's own output directory --
// after the Phase 4 ruling it is reserved and created before the producing Pod
// exists, and the artifact daemon has registered a read-only alias at the
// ordinary `<handle>/<output>` path that points into it. Removing it here made
// every no_capture, cancellation and terminal failure delete a step's output at
// the moment it settled, leaving that alias pointing at a directory that was
// gone; Req 2 says a failed producer follows existing task semantics, and
// existing semantics keep a failed task's outputs for the build's lifetime.
//
// What is left is what a release actually is: the record says released, and the
// gate this hold held open is closed, so the execution becomes cleanup-eligible
// and the incarnation becomes sweepable exactly as an unselected output is.
//
// TODO(phase 7, reclamation): a settled incarnation is deleted by the reclaim
// policy that owns retention -- age, residency and the rule that every
// uncertainty KEEPS -- and never as a side effect of a release. Until that
// lands the artifact daemon's ordinary lifecycle is what reclaims it.
//
// The identity is the REQUEST's, not the record's. `admitted` has just proved
// the request names the fence this node currently holds; the record's copy was
// written when the hold was taken and a takeover since then would make closing
// the gate refuse as stale.
func (ledger *SourceLedger) finishRelease(execution executioncontrol.Identity,
	record sourceRecord) error {
	return ledger.base.CloseGate(execution, SourceHoldGate)
}

var _ output.SourceControl = (*SourceLedger)(nil)

// InspectSeal reports the captured drain set, and invents nothing.
//
// A handoff that has not begun sealing is ErrNotFound rather than an empty
// SealStarted: an empty drain set is a real and meaningful answer -- nobody was
// writing when admission was fenced -- and returning one for "no seal has
// begun" would make the two indistinguishable.
func (ledger *SourceLedger) InspectSeal(handoff output.HandoffID,
	execution executioncontrol.Identity) (output.SealStarted, error) {
	record, found, err := ledger.load(handoff)
	if err != nil {
		return output.SealStarted{}, err
	}
	if !found || record.SealStarted == nil {
		return output.SealStarted{}, fmt.Errorf(
			"%w: handoff %s has not begun sealing", output.ErrNotFound, handoff)
	}
	if err := record.belongsTo(execution, handoff); err != nil {
		return output.SealStarted{}, err
	}

	return output.SealStarted{
		Acknowledgement: *record.SealStarted,
		DrainSet:        append([]output.WriterTicketID(nil), record.DrainSet...),
		Confirmed:       record.State == sourceSealed,
	}, nil
}

// SealedIncarnation is the location of a sealed source, for the publisher.
//
// It is the ONLY way bytes reach the publish path, and it refuses anything that
// is not sealed: canonicalizing an open source would be reading bytes a writer
// may still be changing, and Req 15 is exactly the rule that no canonical read
// begins before both halves of the seal hold.
func (ledger *SourceLedger) SealedIncarnation(handoff output.HandoffID,
	execution executioncontrol.Identity, epoch executioncontrol.ActivationEpoch,
	fence output.CaptureFence) (string, sourceRecord, error) {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()

	record, err := ledger.admitCapture(handoff, execution, epoch, fence)
	if err != nil {
		return "", sourceRecord{}, err
	}
	if record.State != sourceSealed {
		return "", sourceRecord{}, fmt.Errorf(
			"%w: the source for handoff %s is %s; no canonical read begins before the seal is "+
				"confirmed", output.ErrSealUnconfirmed, handoff, record.State)
	}
	root, err := ledger.ResolveIncarnation(record.Incarnation)
	if err != nil {
		return "", sourceRecord{}, err
	}

	return root, record, nil
}
