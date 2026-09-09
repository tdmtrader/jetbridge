package main

// RED: the plausible-wrong source ledger.
//
// It keeps the hold in memory, derives the incarnation directory by joining the
// output name onto a path, admits any writer that asks, captures whatever
// tickets happen to be open at the moment somebody looks, and releases without
// comparing the intent. It is the shape a reasonable person writes first, and
// the point of committing it is that the tests below are rejecting something.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

type SourceLedger struct {
	store  *controlStore
	base   *ExecutionLedger
	node   executioncontrol.NodeUID
	epoch  executioncontrol.ActivationEpoch
	signer *output.CaptureStatementSigner
	clock  func() time.Time
	steps  string

	holds   map[output.HandoffID]output.CaptureAcknowledgement
	tickets map[output.HandoffID][]output.WriterTicketID
	sealed  map[output.HandoffID]bool
}

func OpenSourceLedger(store *controlStore, base *ExecutionLedger, node executioncontrol.NodeUID,
	epoch executioncontrol.ActivationEpoch, signer *output.CaptureStatementSigner,
	clock func() time.Time, steps string) (*SourceLedger, error) {
	if err := os.MkdirAll(steps, 0o700); err != nil {
		return nil, err
	}

	return &SourceLedger{
		store: store, base: base, node: node, epoch: epoch, signer: signer,
		clock: clock, steps: steps,
		holds:   map[output.HandoffID]output.CaptureAcknowledgement{},
		tickets: map[output.HandoffID][]output.WriterTicketID{},
		sealed:  map[output.HandoffID]bool{},
	}, nil
}

func (ledger *SourceLedger) AcknowledgeHold(ctx context.Context, admission output.CaptureAdmission,
	_ output.SourceIncarnation) (output.CaptureAcknowledgement, error) {
	if existing, found := ledger.holds[admission.HandoffID]; found {
		return existing, nil
	}
	incarnation := output.SourceIncarnation{
		ExecutionID:      admission.Execution.ExecutionID,
		NodeUID:          ledger.node,
		HandleGeneration: 1,
		Output:           admission.Output,
	}
	ack, err := ledger.signer.SignCapture(output.CaptureAcknowledgement{
		ProtocolVersion: output.ProtocolVersion,
		Kind:            output.CaptureHoldAcknowledged,
		Execution:       admission.Execution,
		ActivationEpoch: admission.ActivationEpoch,
		LedgerSequence:  1,
		NodeUID:         ledger.node,
		PodUID:          "pod-1",
		HandoffID:       admission.HandoffID,
		SourceLeaseID:   admission.SourceLeaseID,
		Incarnation:     incarnation,
		ObservedAt:      output.NewTimestamp(ledger.clock()),
	})
	if err != nil {
		return output.CaptureAcknowledgement{}, err
	}
	if err := os.MkdirAll(filepath.Join(ledger.steps, string(admission.Output)), 0o700); err != nil {
		return output.CaptureAcknowledgement{}, err
	}
	ledger.holds[admission.HandoffID] = ack

	return ack, nil
}

func (ledger *SourceLedger) InspectHold(handoff output.HandoffID) (output.CaptureAcknowledgement, error) {
	return ledger.holds[handoff], nil
}

func (ledger *SourceLedger) Holds(incarnation output.SourceIncarnation) bool {
	_, err := os.Lstat(filepath.Join(ledger.steps, string(incarnation.Output)))

	return err == nil
}

func (ledger *SourceLedger) ResolveIncarnation(incarnation output.SourceIncarnation) (string, error) {
	return filepath.Join(ledger.steps, string(incarnation.Output)), nil
}

func (ledger *SourceLedger) AdmitWriter(ctx context.Context, admission output.WriterAdmission) (output.CaptureAcknowledgement, error) {
	ledger.tickets[admission.HandoffID] = append(ledger.tickets[admission.HandoffID], admission.WriterTicketID)

	return ledger.signer.SignCapture(output.CaptureAcknowledgement{
		ProtocolVersion: output.ProtocolVersion,
		Kind:            output.CaptureWriterTicketIssued,
		Execution:       admission.Execution,
		ActivationEpoch: admission.ActivationEpoch,
		LedgerSequence:  2,
		NodeUID:         ledger.node,
		PodUID:          admission.PodUID,
		HandoffID:       admission.HandoffID,
		SourceLeaseID:   ledger.holds[admission.HandoffID].SourceLeaseID,
		Incarnation:     admission.Incarnation,
		WriterTicketID:  admission.WriterTicketID,
		WriterFence:     admission.WriterFence,
		ObservedAt:      output.NewTimestamp(ledger.clock()),
	})
}

func (ledger *SourceLedger) RetireWriter(ctx context.Context, admission output.WriterAdmission) (output.CaptureAcknowledgement, error) {
	return ledger.signer.SignCapture(output.CaptureAcknowledgement{
		ProtocolVersion: output.ProtocolVersion,
		Kind:            output.CaptureWriterTicketClosed,
		Execution:       admission.Execution,
		ActivationEpoch: admission.ActivationEpoch,
		LedgerSequence:  3,
		NodeUID:         ledger.node,
		PodUID:          admission.PodUID,
		HandoffID:       admission.HandoffID,
		SourceLeaseID:   ledger.holds[admission.HandoffID].SourceLeaseID,
		Incarnation:     admission.Incarnation,
		WriterTicketID:  admission.WriterTicketID,
		WriterFence:     admission.WriterFence,
		ObservedAt:      output.NewTimestamp(ledger.clock()),
	})
}

func (ledger *SourceLedger) BeginSeal(ctx context.Context, request output.SealRequest) (output.SealStarted, error) {
	ledger.sealed[request.HandoffID] = true
	ack, err := ledger.signer.SignCapture(output.CaptureAcknowledgement{
		ProtocolVersion: output.ProtocolVersion,
		Kind:            output.CaptureSealStarted,
		Execution:       request.Execution,
		ActivationEpoch: request.ActivationEpoch,
		LedgerSequence:  4,
		NodeUID:         ledger.node,
		PodUID:          "pod-1",
		HandoffID:       request.HandoffID,
		SourceLeaseID:   ledger.holds[request.HandoffID].SourceLeaseID,
		Incarnation:     request.Incarnation,
		ObservedAt:      output.NewTimestamp(ledger.clock()),
	})
	if err != nil {
		return output.SealStarted{}, err
	}

	return output.SealStarted{Acknowledgement: ack}, nil
}

func (ledger *SourceLedger) ConfirmSeal(ctx context.Context, confirmation output.SealConfirmation) (output.CaptureAcknowledgement, error) {
	started := confirmation.Started.Acknowledgement

	return ledger.signer.SignCapture(output.CaptureAcknowledgement{
		ProtocolVersion: output.ProtocolVersion,
		Kind:            output.CaptureSealConfirmed,
		Execution:       started.Execution,
		ActivationEpoch: started.ActivationEpoch,
		LedgerSequence:  5,
		NodeUID:         ledger.node,
		PodUID:          started.PodUID,
		HandoffID:       started.HandoffID,
		SourceLeaseID:   started.SourceLeaseID,
		Incarnation:     started.Incarnation,
		ObservedAt:      output.NewTimestamp(ledger.clock()),
	})
}

func (ledger *SourceLedger) AcknowledgeRelease(ctx context.Context, intent output.ReleaseIntent) (output.ReleaseAcknowledgement, error) {
	if err := os.RemoveAll(filepath.Join(ledger.steps, string(intent.Incarnation.Output))); err != nil {
		return output.ReleaseAcknowledgement{}, fmt.Errorf("%w: %v", output.ErrInfrastructure, err)
	}

	return ledger.signer.SignRelease(output.ReleaseAcknowledgement{
		ProtocolVersion: output.ProtocolVersion,
		Disposition:     intent.Disposition,
		Execution:       intent.Execution,
		ActivationEpoch: intent.ActivationEpoch,
		HandoffID:       intent.HandoffID,
		SourceLeaseID:   intent.SourceLeaseID,
		ReleaseIntentID: intent.ReleaseIntentID,
		Incarnation:     intent.Incarnation,
		LedgerSequence:  6,
		ObservedAt:      output.NewTimestamp(ledger.clock()),
	})
}

var _ output.SourceControl = (*SourceLedger)(nil)
