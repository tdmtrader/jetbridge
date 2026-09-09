package main

// RED: the plausible-wrong execution ledger.
//
// This is the implementation the requirements are written against -- an
// in-memory map of the last thing the daemon was told, classified from whatever
// the caller last said, with no fence comparison, no durable record, and
// cleanup permitted as soon as an execution is known. It is deliberately the
// shape a reasonable person writes first, so the tests that reject it are
// rejecting something rather than nothing.

import (
	"fmt"
	"time"

	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

type ExecutionLedger struct {
	node     executioncontrol.NodeUID
	epoch    executioncontrol.ActivationEpoch
	signer   *executioncontrol.AcknowledgementSigner
	clock    func() time.Time
	sequence uint64

	known map[executioncontrol.ExecutionID]*executioncontrol.Acknowledgement
	gates map[executioncontrol.ExecutionID][]string
}

func OpenExecutionLedger(store *controlStore, node executioncontrol.NodeUID,
	epoch executioncontrol.ActivationEpoch, signer *executioncontrol.AcknowledgementSigner,
	clock func() time.Time) (*ExecutionLedger, error) {
	_ = store

	return &ExecutionLedger{
		node: node, epoch: epoch, signer: signer, clock: clock,
		known: map[executioncontrol.ExecutionID]*executioncontrol.Acknowledgement{},
		gates: map[executioncontrol.ExecutionID][]string{},
	}, nil
}

func (ledger *ExecutionLedger) Admit(envelope executioncontrol.Envelope) error {
	if err := envelope.Validate(); err != nil {
		return err
	}
	ledger.known[envelope.ExecutionID] = nil

	return nil
}

func (ledger *ExecutionLedger) sign(identity executioncontrol.Identity,
	kind executioncontrol.AcknowledgementKind, pod executioncontrol.PodUID,
	process executioncontrol.ProcessIdentity, outcome *executioncontrol.ExitOutcome) (executioncontrol.Acknowledgement, error) {
	ledger.sequence++

	return ledger.signer.Sign(executioncontrol.Acknowledgement{
		ProtocolVersion: executioncontrol.ProtocolVersion,
		Kind:            kind,
		Identity:        identity,
		ActivationEpoch: ledger.epoch,
		LedgerSequence:  executioncontrol.LedgerSequence(ledger.sequence),
		NodeUID:         ledger.node,
		PodUID:          pod,
		ProcessIdentity: process,
		ObservedAt:      executioncontrol.NewTimestamp(ledger.clock()),
		Outcome:         outcome,
	})
}

func (ledger *ExecutionLedger) RecordStart(identity executioncontrol.Identity,
	pod executioncontrol.PodUID, process executioncontrol.ProcessIdentity) (executioncontrol.Acknowledgement, error) {
	ack, err := ledger.sign(identity, executioncontrol.AcknowledgementStart, pod, process, nil)
	if err != nil {
		return executioncontrol.Acknowledgement{}, err
	}
	ledger.known[identity.ExecutionID] = &ack

	return ack, nil
}

func (ledger *ExecutionLedger) RecordOutcome(identity executioncontrol.Identity,
	kind executioncontrol.AcknowledgementKind, outcome executioncontrol.ExitOutcome) (executioncontrol.Acknowledgement, error) {
	ack, err := ledger.sign(identity, kind, testPodPlaceholder, "proc", &outcome)
	if err != nil {
		return executioncontrol.Acknowledgement{}, err
	}
	ledger.known[identity.ExecutionID] = &ack

	return ack, nil
}

const testPodPlaceholder = executioncontrol.PodUID("pod-1")

func (ledger *ExecutionLedger) Classify(identity executioncontrol.Identity) (executioncontrol.ClassifyResult, error) {
	ack, known := ledger.known[identity.ExecutionID]
	result := executioncontrol.ClassifyResult{
		ProtocolVersion: executioncontrol.ProtocolVersion,
		Identity:        identity,
		Classification:  executioncontrol.ClassificationNeverStarted,
	}
	if known && ack != nil {
		result.Classification = ack.Kind.Classification()
		result.Acknowledgement = ack
	}

	return result, nil
}

func (ledger *ExecutionLedger) Observe(identity executioncontrol.Identity) (executioncontrol.ObserveFinishOrStopResult, error) {
	classified, err := ledger.Classify(identity)
	if err != nil {
		return executioncontrol.ObserveFinishOrStopResult{}, err
	}

	return executioncontrol.ObserveFinishOrStopResult{
		ProtocolVersion: executioncontrol.ProtocolVersion,
		Identity:        identity,
		Classification:  classified.Classification,
		Acknowledgement: classified.Acknowledgement,
	}, nil
}

func (ledger *ExecutionLedger) RequestStop(identity executioncontrol.Identity) (executioncontrol.RequestSourcePreservingStopResult, error) {
	classified, err := ledger.Classify(identity)
	if err != nil {
		return executioncontrol.RequestSourcePreservingStopResult{}, err
	}

	return executioncontrol.RequestSourcePreservingStopResult{
		ProtocolVersion: executioncontrol.ProtocolVersion,
		Identity:        identity,
		Accepted:        true,
		Classification:  classified.Classification,
	}, nil
}

func (ledger *ExecutionLedger) CleanupEligible(identity executioncontrol.Identity) (executioncontrol.DestructiveCleanupEligibleResult, error) {
	classified, err := ledger.Classify(identity)
	if err != nil {
		return executioncontrol.DestructiveCleanupEligibleResult{}, err
	}
	_, known := ledger.known[identity.ExecutionID]

	return executioncontrol.DestructiveCleanupEligibleResult{
		ProtocolVersion:    executioncontrol.ProtocolVersion,
		Identity:           identity,
		Eligible:           known,
		Classification:     classified.Classification,
		OpenExtensionGates: ledger.gates[identity.ExecutionID],
	}, nil
}

func (ledger *ExecutionLedger) OpenGate(identity executioncontrol.Identity, gate string) error {
	ledger.gates[identity.ExecutionID] = append(ledger.gates[identity.ExecutionID], gate)

	return nil
}

func (ledger *ExecutionLedger) CloseGate(identity executioncontrol.Identity, gate string) error {
	remaining := ledger.gates[identity.ExecutionID][:0]
	for _, open := range ledger.gates[identity.ExecutionID] {
		if open != gate {
			remaining = append(remaining, open)
		}
	}
	ledger.gates[identity.ExecutionID] = remaining

	return nil
}

func (ledger *ExecutionLedger) MarkUnresolved(identity executioncontrol.Identity, reason string) error {
	return fmt.Errorf("%w: %s", output.ErrIncomplete, reason)
}

func (ledger *ExecutionLedger) MarkLost(identity executioncontrol.Identity, reason string) error {
	return fmt.Errorf("%w: %s", output.ErrIncomplete, reason)
}
