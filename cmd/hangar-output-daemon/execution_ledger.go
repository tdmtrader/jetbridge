package main

// The execution ledger: this node's sole runtime owner of exact execution truth.
//
// It answers four questions about one controlled execution -- what is durably
// known, what the durable outcome was, may the command be interrupted, and may
// its remains be destroyed -- and it answers them from records on this node's
// disk. Not from a Pod phase, not from a container status, not from an
// in-memory map that a restart empties. The whole reason this component exists
// is that those three can all be wrong at once and none of them can say so.
//
// What it does NOT contain is as deliberate as what it does. There is no Run,
// build kind, job, check, cancellation reason, output, hold, capture or receipt
// anywhere in this file. An optional capture extension references an execution
// by identity and may open a named cleanup gate on it; it cannot replace the
// outcome and it cannot start a second state machine. That is decision F13's
// contract obligation, and TestABaseExecutionNeedsNoExtensionToBeComplete is
// what keeps it true: a base execution with no extension at all is a complete,
// verifiable, cleanup-eligible thing.
//
// Ordering is the substance here, so it is stated once:
//
//	Admit    before any start is admissible, and it names the fence
//	Start    written and made durable BEFORE the child is launched, so that a
//	         crash between the two is unresolved and never never_started
//	Outcome  written and made durable BEFORE the result is exposed to a caller,
//	         so that a lost response is recovered rather than re-executed
//
// Every acknowledgement is signed on the way out and stored as signed. Replay
// returns the stored bytes rather than signing again: a second signature over
// the same facts is a second statement, and a caller that received two could
// not tell which one the control plane's records agree with.

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// executionRecord is one execution's durable state.
//
// It is a single record replaced atomically rather than an append log, because
// every question this ledger answers is about the current state and a log would
// need its own compaction, its own torn-tail rule and its own recovery. The
// monotonic sequence that a log would give for free is carried explicitly
// instead, and recovered at open as the maximum over every record.
type executionRecord struct {
	Identity        executioncontrol.Identity        `json:"identity"`
	ActivationEpoch executioncontrol.ActivationEpoch `json:"activation_epoch"`
	NodeUID         executioncontrol.NodeUID         `json:"node_uid"`
	PodUID          executioncontrol.PodUID          `json:"pod_uid"`
	AdmittedAt      executioncontrol.Timestamp       `json:"admitted_at"`

	// Start and Outcome are the signed statements themselves, stored exactly as
	// they were returned. A replay hands back these bytes.
	Start   *executioncontrol.Acknowledgement `json:"start,omitempty"`
	Outcome *executioncontrol.Acknowledgement `json:"outcome,omitempty"`

	// StopRequested records that an interruption was admitted. It is not an
	// outcome and it never becomes one: the supervisor's own record is.
	StopRequested bool `json:"stop_requested"`

	// Unproved is set when the truth cannot be established. Reason is for an
	// operator; neither may ever be read as an outcome.
	Unproved       executioncontrol.Classification `json:"unproved,omitempty"`
	UnprovedReason string                          `json:"unproved_reason,omitempty"`

	// OpenGates are opaque names an optional extension asked to keep open. This
	// ledger does not know what any of them mean, which is exactly why an
	// extension can add one without this file learning what a capture is.
	OpenGates []string `json:"open_gates,omitempty"`

	// HighWater is the ledger sequence at the time this record was last
	// written. The maximum over every record is what the sequence resumes from
	// after a restart.
	HighWater executioncontrol.LedgerSequence `json:"high_water"`
}

// ExecutionLedger is the durable state machine. One mutex serializes the whole
// thing: these operations are per-execution and rare, and a lock per identity
// would buy contention nobody has and a lock order nobody needs.
type ExecutionLedger struct {
	store  *controlStore
	node   executioncontrol.NodeUID
	epoch  executioncontrol.ActivationEpoch
	signer *executioncontrol.AcknowledgementSigner
	clock  func() time.Time

	mu       sync.Mutex
	sequence executioncontrol.LedgerSequence
}

// executionRecordPrefix keeps the two ledgers' records apart in one directory,
// so that neither can read the other's by name.
const executionRecordPrefix = "execution-"

func OpenExecutionLedger(store *controlStore, node executioncontrol.NodeUID,
	epoch executioncontrol.ActivationEpoch, signer *executioncontrol.AcknowledgementSigner,
	clock func() time.Time) (*ExecutionLedger, error) {
	if store == nil {
		return nil, fmt.Errorf("%w: the execution ledger has no control store", output.ErrIncomplete)
	}
	if signer == nil {
		return nil, fmt.Errorf("%w: the execution ledger has no signing key; an unsigned "+
			"acknowledgement is not proof", output.ErrIncomplete)
	}
	if node == "" {
		return nil, fmt.Errorf("%w: the execution ledger names no node", output.ErrIncomplete)
	}
	if epoch == 0 {
		return nil, fmt.Errorf("%w: the execution ledger names no activation epoch", output.ErrIncomplete)
	}
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}

	ledger := &ExecutionLedger{store: store, node: node, epoch: epoch, signer: signer, clock: clock}

	// The sequence resumes above every sequence this node ever issued. Two
	// statements from one node must always be orderable, and a sequence that
	// restarted at one after a crash would make two different statements claim
	// the same position.
	names, err := store.names()
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		if !strings.HasPrefix(name, executionRecordPrefix) {
			continue
		}
		var record executionRecord
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

func executionRecordName(id executioncontrol.ExecutionID) (string, error) {
	if err := id.Validate(); err != nil {
		return "", err
	}

	return executionRecordPrefix + string(id) + ".json", nil
}

// load reads one execution's record. A missing record is not an error: an
// execution this node never admitted is never_started, which is an answer.
func (ledger *ExecutionLedger) load(id executioncontrol.ExecutionID) (executionRecord, bool, error) {
	name, err := executionRecordName(id)
	if err != nil {
		return executionRecord{}, false, err
	}
	var record executionRecord
	found, err := ledger.store.get(name, &record)

	return record, found, err
}

func (ledger *ExecutionLedger) save(record executionRecord) error {
	name, err := executionRecordName(record.Identity.ExecutionID)
	if err != nil {
		return err
	}

	return ledger.store.put(name, record)
}

// admittedRecord is the precondition every acting operation shares: the
// execution is known here, at this fence, under this epoch.
//
// The fence rule is the one worth stating. A holder of an older fence may
// OBSERVE -- Classify and Observe take no fence check, because reading the
// truth is never dangerous -- but it may not act. Takeover advances the fence,
// and the previous owner's next write is refused rather than merged.
func (ledger *ExecutionLedger) admittedRecord(identity executioncontrol.Identity) (executionRecord, error) {
	if err := identity.Validate(); err != nil {
		return executionRecord{}, err
	}
	if identity.Fence == 0 {
		return executionRecord{}, fmt.Errorf("%w: fence zero is not a fence", executioncontrol.ErrStaleFence)
	}

	record, found, err := ledger.load(identity.ExecutionID)
	if err != nil {
		return executionRecord{}, err
	}
	if !found {
		return executionRecord{}, fmt.Errorf("%w: execution %s was never admitted on this node; "+
			"a start the control plane did not admit is a command nobody authorized",
			output.ErrUnauthorized, identity.ExecutionID)
	}
	if identity.Fence < record.Identity.Fence {
		return executionRecord{}, fmt.Errorf("%w: fence %d was superseded by %d",
			executioncontrol.ErrStaleFence, identity.Fence, record.Identity.Fence)
	}
	if identity.Fence > record.Identity.Fence {
		return executionRecord{}, fmt.Errorf("%w: fence %d has not been admitted; this node holds "+
			"%d", output.ErrUnauthorized, identity.Fence, record.Identity.Fence)
	}

	return record, nil
}

// Admit records an execution before anything may start, and it is also how a
// takeover advances the fence.
func (ledger *ExecutionLedger) Admit(envelope executioncontrol.Envelope) error {
	if err := envelope.Validate(); err != nil {
		return err
	}
	if envelope.ActivationEpoch != ledger.epoch {
		return fmt.Errorf("%w: the envelope names epoch %d and this node is attested for %d",
			output.ErrConflict, envelope.ActivationEpoch, ledger.epoch)
	}
	if envelope.NodeUID != ledger.node {
		return fmt.Errorf("%w: the envelope names node %s and this is %s",
			output.ErrUnauthorized, envelope.NodeUID, ledger.node)
	}

	ledger.mu.Lock()
	defer ledger.mu.Unlock()

	record, found, err := ledger.load(envelope.ExecutionID)
	if err != nil {
		return err
	}
	if found {
		if envelope.Fence < record.Identity.Fence {
			return fmt.Errorf("%w: admission at fence %d, this node holds %d",
				executioncontrol.ErrStaleFence, envelope.Fence, record.Identity.Fence)
		}
		if envelope.Fence == record.Identity.Fence {
			// Idempotent: the same admission twice is one admission.
			return nil
		}
		// A takeover advances the fence and keeps every durable statement. The
		// outcome of a process is a fact about that process; a new controller
		// does not get to un-know it.
		record.Identity.Fence = envelope.Fence
		record.PodUID = envelope.PodUID
		ledger.sequence++
		record.HighWater = ledger.sequence

		return ledger.save(record)
	}

	ledger.sequence++

	return ledger.save(executionRecord{
		Identity:        envelope.Identity,
		ActivationEpoch: envelope.ActivationEpoch,
		NodeUID:         envelope.NodeUID,
		PodUID:          envelope.PodUID,
		AdmittedAt:      executioncontrol.NewTimestamp(ledger.clock()),
		HighWater:       ledger.sequence,
	})
}

// sign mints one statement. The sequence is taken under the ledger's lock and
// is never reused.
func (ledger *ExecutionLedger) sign(record *executionRecord,
	kind executioncontrol.AcknowledgementKind, pod executioncontrol.PodUID,
	process executioncontrol.ProcessIdentity,
	outcome *executioncontrol.ExitOutcome) (executioncontrol.Acknowledgement, error) {
	ledger.sequence++
	record.HighWater = ledger.sequence

	return ledger.signer.Sign(executioncontrol.Acknowledgement{
		ProtocolVersion: executioncontrol.ProtocolVersion,
		Kind:            kind,
		Identity:        record.Identity,
		ActivationEpoch: record.ActivationEpoch,
		LedgerSequence:  ledger.sequence,
		NodeUID:         record.NodeUID,
		PodUID:          pod,
		ProcessIdentity: process,
		ObservedAt:      executioncontrol.NewTimestamp(ledger.clock()),
		Outcome:         outcome,
	})
}

// RecordStart is written and made durable before the child process is launched.
//
// That order is the whole reason a crash between the two is `unresolved` rather
// than `never_started`: a caller that saw this return knows a process may exist,
// and a caller that did not can retry the call and find out.
func (ledger *ExecutionLedger) RecordStart(identity executioncontrol.Identity,
	pod executioncontrol.PodUID, process executioncontrol.ProcessIdentity) (executioncontrol.Acknowledgement, error) {
	if process == "" {
		return executioncontrol.Acknowledgement{}, fmt.Errorf(
			"%w: a start names no process identity", output.ErrIncomplete)
	}

	ledger.mu.Lock()
	defer ledger.mu.Unlock()

	record, err := ledger.admittedRecord(identity)
	if err != nil {
		return executioncontrol.Acknowledgement{}, err
	}
	if record.Outcome != nil {
		return executioncontrol.Acknowledgement{}, fmt.Errorf(
			"%w: execution %s already has a durable %s outcome; a recorded execution is never "+
				"run again", output.ErrConflict, identity.ExecutionID, record.Outcome.Kind)
	}
	if record.Start != nil {
		// Idempotent replay. The same start, offered again by a caller that
		// never saw the answer, returns the stored statement -- not a second
		// signature over the same facts.
		if record.Start.PodUID != pod || record.Start.ProcessIdentity != process {
			return executioncontrol.Acknowledgement{}, fmt.Errorf(
				"%w: execution %s already started as %s/%s and this start names %s/%s",
				output.ErrConflict, identity.ExecutionID,
				record.Start.PodUID, record.Start.ProcessIdentity, pod, process)
		}

		return *record.Start, nil
	}

	ack, err := ledger.sign(&record, executioncontrol.AcknowledgementStart, pod, process, nil)
	if err != nil {
		return executioncontrol.Acknowledgement{}, err
	}
	record.Start = &ack
	if err := ledger.save(record); err != nil {
		return executioncontrol.Acknowledgement{}, err
	}

	return ack, nil
}

// RecordOutcome is written and made durable before the result is exposed.
//
// A conflicting outcome is refused rather than merged. Two different answers
// about one process means one of them is wrong, and a ledger that took the
// second would be a ledger whose answer depends on who asked last.
func (ledger *ExecutionLedger) RecordOutcome(identity executioncontrol.Identity,
	kind executioncontrol.AcknowledgementKind, outcome executioncontrol.ExitOutcome) (executioncontrol.Acknowledgement, error) {
	if err := kind.Validate(); err != nil {
		return executioncontrol.Acknowledgement{}, err
	}
	if kind == executioncontrol.AcknowledgementStart {
		return executioncontrol.Acknowledgement{}, fmt.Errorf(
			"%w: a start is not an outcome", output.ErrIncomplete)
	}
	if err := outcome.Validate(); err != nil {
		return executioncontrol.Acknowledgement{}, err
	}

	ledger.mu.Lock()
	defer ledger.mu.Unlock()

	record, err := ledger.admittedRecord(identity)
	if err != nil {
		return executioncontrol.Acknowledgement{}, err
	}
	if record.Start == nil {
		return executioncontrol.Acknowledgement{}, fmt.Errorf(
			"%w: execution %s has no durable start record; an outcome for a command that was "+
				"never recorded as started is an inference", output.ErrUnauthorized, identity.ExecutionID)
	}
	if existing := record.Outcome; existing != nil {
		if existing.Kind == kind && existing.Outcome != nil && *existing.Outcome == outcome {
			return *existing, nil
		}

		return executioncontrol.Acknowledgement{}, fmt.Errorf(
			"%w: execution %s durably %s with %+v and a %s with %+v was offered",
			output.ErrConflict, identity.ExecutionID, existing.Kind, *existing.Outcome, kind, outcome)
	}

	ack, err := ledger.sign(&record, kind, record.Start.PodUID, record.Start.ProcessIdentity, &outcome)
	if err != nil {
		return executioncontrol.Acknowledgement{}, err
	}
	record.Outcome = &ack
	// A durable outcome retires an unproved marking: the answer arrived.
	record.Unproved, record.UnprovedReason = "", ""
	if err := ledger.save(record); err != nil {
		return executioncontrol.Acknowledgement{}, err
	}

	return ack, nil
}

// classifyExecution is the pure rule, over a record. It never consults a Pod, a
// process table or a caller's opinion, and it has no argument through which one
// could be offered.
func classifyExecution(record executionRecord, found bool) (executioncontrol.Classification, *executioncontrol.Acknowledgement) {
	if !found {
		return executioncontrol.ClassificationNeverStarted, nil
	}
	if record.Outcome != nil {
		return record.Outcome.Kind.Classification(), record.Outcome
	}
	if record.Unproved != "" {
		return record.Unproved, nil
	}
	if record.Start != nil {
		return executioncontrol.ClassificationExecuting, nil
	}

	return executioncontrol.ClassificationNeverStarted, nil
}

func (ledger *ExecutionLedger) Classify(identity executioncontrol.Identity) (executioncontrol.ClassifyResult, error) {
	if err := identity.Validate(); err != nil {
		return executioncontrol.ClassifyResult{}, err
	}

	ledger.mu.Lock()
	defer ledger.mu.Unlock()

	record, found, err := ledger.load(identity.ExecutionID)
	if err != nil {
		return executioncontrol.ClassifyResult{}, err
	}
	classification, ack := classifyExecution(record, found)

	result := executioncontrol.ClassifyResult{
		ProtocolVersion: executioncontrol.ProtocolVersion,
		Identity:        identity,
		Classification:  classification,
		Acknowledgement: ack,
	}
	if ack != nil {
		// The evidence rule: an acknowledgement is attached only when it is for
		// this exact identity. A statement about a superseded fence is not
		// proof about the current one.
		result.Identity = ack.Identity
	}

	return result, result.Validate()
}

// Observe returns a durable acknowledgement or says there is not one. It polls;
// the caller owns its own deadline, which is why the wait is on the wire and
// not in this method.
func (ledger *ExecutionLedger) Observe(identity executioncontrol.Identity) (executioncontrol.ObserveFinishOrStopResult, error) {
	classified, err := ledger.Classify(identity)
	if err != nil {
		return executioncontrol.ObserveFinishOrStopResult{}, err
	}

	result := executioncontrol.ObserveFinishOrStopResult{
		ProtocolVersion: executioncontrol.ProtocolVersion,
		Identity:        classified.Identity,
		Classification:  classified.Classification,
		Acknowledgement: classified.Acknowledgement,
	}

	return result, result.Validate()
}

// RequestStop interrupts the command and destroys nothing.
//
// It deletes no Pod, removes no artifact path and releases no gate. It records
// that an interruption was admitted; the outcome arrives through the
// supervisor's own record like every other outcome.
func (ledger *ExecutionLedger) RequestStop(identity executioncontrol.Identity) (executioncontrol.RequestSourcePreservingStopResult, error) {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()

	record, err := ledger.admittedRecord(identity)
	if err != nil {
		return executioncontrol.RequestSourcePreservingStopResult{}, err
	}
	classification, _ := classifyExecution(record, true)

	result := executioncontrol.RequestSourcePreservingStopResult{
		ProtocolVersion: executioncontrol.ProtocolVersion,
		Identity:        identity,
		Classification:  classification,
	}
	if classification.Terminal() {
		// Nothing to interrupt. Reporting this as accepted would be a caller
		// waiting for a stop that will never be acknowledged.
		return result, result.Validate()
	}

	result.Accepted = true
	if !record.StopRequested {
		record.StopRequested = true
		ledger.sequence++
		record.HighWater = ledger.sequence
		if err := ledger.save(record); err != nil {
			return executioncontrol.RequestSourcePreservingStopResult{}, err
		}
	}

	return result, result.Validate()
}

// CleanupEligible fails closed. Unknown means no, unproved means no, and an
// open gate means no.
func (ledger *ExecutionLedger) CleanupEligible(identity executioncontrol.Identity) (executioncontrol.DestructiveCleanupEligibleResult, error) {
	if err := identity.Validate(); err != nil {
		return executioncontrol.DestructiveCleanupEligibleResult{}, err
	}

	ledger.mu.Lock()
	defer ledger.mu.Unlock()

	record, found, err := ledger.load(identity.ExecutionID)
	if err != nil {
		return executioncontrol.DestructiveCleanupEligibleResult{}, err
	}
	classification, _ := classifyExecution(record, found)

	result := executioncontrol.DestructiveCleanupEligibleResult{
		ProtocolVersion:    executioncontrol.ProtocolVersion,
		Identity:           identity,
		Classification:     classification,
		OpenExtensionGates: append([]string(nil), record.OpenGates...),
	}
	switch {
	case !classification.Authoritative():
		result.WithheldReason = fmt.Sprintf("execution %s is %s; only a durable finish or stop "+
			"may authorize destroying anything", identity.ExecutionID, classification)
	case len(record.OpenGates) != 0:
		result.WithheldReason = fmt.Sprintf("execution %s finished and %d extension gate(s) are "+
			"still open: %s", identity.ExecutionID, len(record.OpenGates),
			strings.Join(record.OpenGates, ", "))
	default:
		result.Eligible = true
	}

	return result, result.Validate()
}

// OpenGate and CloseGate are the extension's whole vocabulary here.
//
// A gate is an opaque string. This ledger will not learn what one means, and
// there is no parameter through which an extension could tell it: that is what
// stops the optional half from becoming a second state machine over the same
// process.
func (ledger *ExecutionLedger) OpenGate(identity executioncontrol.Identity, gate string) error {
	if strings.TrimSpace(gate) == "" {
		return fmt.Errorf("%w: an extension gate needs a name", output.ErrIncomplete)
	}

	ledger.mu.Lock()
	defer ledger.mu.Unlock()

	record, err := ledger.admittedRecord(identity)
	if err != nil {
		return err
	}
	for _, open := range record.OpenGates {
		if open == gate {
			return nil
		}
	}
	record.OpenGates = append(record.OpenGates, gate)
	sort.Strings(record.OpenGates)
	ledger.sequence++
	record.HighWater = ledger.sequence

	return ledger.save(record)
}

// EnsureGateOpen re-opens a gate this node already committed to, without a
// fence.
//
// It exists for one thing: the second half of a two-write durable step that a
// crash interrupted. The extension writes its own record and then opens the
// gate, and a replay of that step has to be able to finish it -- including a
// replay reaching this node from a caller whose fence has since been
// superseded, which is a READ and must stay one.
//
// Opening a gate is the fail-closed direction. It can only withhold cleanup,
// never authorize it, so a repair that takes no fence grants nobody anything.
// CLOSING one still takes admission at the current fence, because that is the
// direction that lets bytes be destroyed.
func (ledger *ExecutionLedger) EnsureGateOpen(id executioncontrol.ExecutionID, gate string) error {
	if strings.TrimSpace(gate) == "" {
		return fmt.Errorf("%w: an extension gate needs a name", output.ErrIncomplete)
	}

	ledger.mu.Lock()
	defer ledger.mu.Unlock()

	record, found, err := ledger.load(id)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%w: execution %s was never admitted on this node", output.ErrUnauthorized, id)
	}
	for _, open := range record.OpenGates {
		if open == gate {
			return nil
		}
	}
	record.OpenGates = append(record.OpenGates, gate)
	sort.Strings(record.OpenGates)
	ledger.sequence++
	record.HighWater = ledger.sequence

	return ledger.save(record)
}

func (ledger *ExecutionLedger) CloseGate(identity executioncontrol.Identity, gate string) error {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()

	record, err := ledger.admittedRecord(identity)
	if err != nil {
		return err
	}
	remaining := make([]string, 0, len(record.OpenGates))
	for _, open := range record.OpenGates {
		if open != gate {
			remaining = append(remaining, open)
		}
	}
	if len(remaining) == len(record.OpenGates) {
		return nil
	}
	record.OpenGates = remaining
	ledger.sequence++
	record.HighWater = ledger.sequence

	return ledger.save(record)
}

// MarkUnresolved and MarkLost are the two honest failures.
//
// Neither produces an acknowledgement, and neither may be attached to one:
// evidence beside an unproved answer is how an inference starts looking like
// proof. Neither authorizes cleanup and neither authorizes re-running the
// command.
func (ledger *ExecutionLedger) MarkUnresolved(identity executioncontrol.Identity, reason string) error {
	return ledger.markUnproved(identity, executioncontrol.ClassificationUnresolved, reason)
}

func (ledger *ExecutionLedger) MarkLost(identity executioncontrol.Identity, reason string) error {
	return ledger.markUnproved(identity, executioncontrol.ClassificationLost, reason)
}

func (ledger *ExecutionLedger) markUnproved(identity executioncontrol.Identity,
	classification executioncontrol.Classification, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("%w: %s with no reason is a stuck execution nobody can diagnose",
			output.ErrIncomplete, classification)
	}

	ledger.mu.Lock()
	defer ledger.mu.Unlock()

	record, err := ledger.admittedRecord(identity)
	if err != nil {
		return err
	}
	if record.Outcome != nil {
		return fmt.Errorf("%w: execution %s durably %s; a proved outcome is not un-proved",
			output.ErrConflict, identity.ExecutionID, record.Outcome.Kind)
	}
	if record.Unproved == executioncontrol.ClassificationLost && classification != executioncontrol.ClassificationLost {
		return fmt.Errorf("%w: execution %s is lost, which is terminal", output.ErrConflict,
			identity.ExecutionID)
	}
	record.Unproved, record.UnprovedReason = classification, reason
	ledger.sequence++
	record.HighWater = ledger.sequence

	return ledger.save(record)
}

// Sequence is the ledger's current high-water mark, for the handshake.
func (ledger *ExecutionLedger) Sequence() executioncontrol.LedgerSequence {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()

	return ledger.sequence
}

// Admission is the exact identity an execution is currently admitted at, and
// the Pod it was admitted for.
//
// The capture extension needs both before the execution has started -- a source
// is held before the producer's main process runs -- so they come from the
// admission record rather than from a start acknowledgement that may not exist
// yet. The FENCE is the important half: an extension that read the fence out of
// its own record instead would honour a takeover on one side and not the other,
// which is two truths about one execution.
//
// It is the extension's only read of base state that is not a classification,
// and it is read-only. Nothing here lets the extension change what the base
// ledger says.
func (ledger *ExecutionLedger) Admission(id executioncontrol.ExecutionID) (executioncontrol.Identity, executioncontrol.PodUID, error) {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()

	record, found, err := ledger.load(id)
	if err != nil {
		return executioncontrol.Identity{}, "", err
	}
	if !found {
		return executioncontrol.Identity{}, "", fmt.Errorf(
			"%w: execution %s was never admitted on this node", output.ErrUnauthorized, id)
	}

	return record.Identity, record.PodUID, nil
}
