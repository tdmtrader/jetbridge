package steps

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

func CancellationSourceProofDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[RunOutputFinish, CancellationLeaseResult]("cancellation evidence exercises {string}", func(in RunOutputFinish, p brine.Params, _ *brine.Recorder) (CancellationLeaseResult, error) {
			mode, _ := p.GetString(0)
			return CancellationLeaseResult{Err: exerciseCancellationEvidence(in, mode)}, nil
		}),
		CheckThat[CancellationLeaseResult]("source proof follows its exact execution and transaction", func(in CancellationLeaseResult) error { return in.Err }),
		brine.DefineMap[RunOutputFinish, RunOutputFinish]("cancellation tries to select source release without finish evidence", func(in RunOutputFinish, _ brine.Params, _ *brine.Recorder) (RunOutputFinish, error) {
			if _, err := acceptRunCancellation(in.Start, "owner", nil, false); err != nil {
				return in, err
			}
			tx, err := in.Start.DB.Conn.Begin()
			if err != nil {
				return in, err
			}
			defer db.Rollback(tx)
			_, in.Err = in.repository().CancelOrSettle(context.Background(), tx, in.Start.Record.HandoffID)
			if in.Err == nil {
				in.Err = tx.Commit()
			}
			return in, nil
		}),
		CheckThat[RunOutputFinish]("the Run keeps its source and handoff open", func(in RunOutputFinish) error {
			if !errors.Is(in.Err, atc.ErrRunOutputPending) {
				return fmt.Errorf("source release was selected without exact finish evidence: %v", in.Err)
			}
			if err := checkNoRunCheckpoint(in); err != nil {
				return err
			}
			r := in.Start.Replay
			ack, err := decodeControl[output.CaptureAcknowledgement](in.Start.Daemon.capture("inspect-hold", "/capture/v1/hold/inspect", r.Execution, holdInspection{Execution: r.Execution, HandoffID: r.HandoffID}))
			if err != nil {
				return err
			}
			if ack.HandoffID != in.Hold.HandoffID || ack.Incarnation != in.Hold.Incarnation {
				return fmt.Errorf("unproven cancellation changed the held source")
			}
			return nil
		}),
	}
}

func exerciseCancellationEvidence(in RunOutputFinish, mode string) error {
	if _, err := acceptRunCancellation(in.Start, "owner", nil, false); err != nil {
		return err
	}
	evidence, found, err := cancellationEvidence(in)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("source fixture has no reservation")
	}
	accepted := false
	switch mode {
	case "verified finish", "fenced never started", "replay":
		accepted = true
	case "rollback", "executing":
	case "signature":
		evidence.Execution.Acknowledgement.Signature = base64.StdEncoding.EncodeToString(make([]byte, 64))
	case "node":
		evidence.NodeUID = "replacement-node"
	case "pod":
		evidence.Execution.Acknowledgement.PodUID = "replacement-pod"
	case "execution":
		evidence.Execution.ExecutionID = executioncontrol.ExecutionID(freshUUID())
	case "missing start closure":
		evidence.StartClosure = nil
	case "refused start closure":
		evidence.StartClosure.Accepted = false
	case "stale owner":
		return staleCancellationEvidence(in, evidence)
	default:
		return fmt.Errorf("unknown evidence case %q", mode)
	}
	err = recordCancellationEvidence(in, evidence, mode == "rollback")
	if accepted && err != nil {
		return err
	}
	if !accepted && mode != "rollback" && err == nil {
		return fmt.Errorf("invalid source evidence accepted: %s", mode)
	}
	if mode == "replay" {
		if err := recordCancellationEvidence(in, evidence, false); err != nil {
			return err
		}
	}
	var count int
	if err := in.Start.DB.Conn.QueryRow(`SELECT count(*) FROM pipeline_run_output_cancellation_evidence WHERE handoff_id=$1`, string(in.Start.Record.HandoffID)).Scan(&count); err != nil {
		return err
	}
	if (count == 1) != accepted {
		return fmt.Errorf("evidence transaction left %d proofs for accepted=%t", count, accepted)
	}
	tx, err := in.Start.DB.Conn.Begin()
	if err != nil {
		return err
	}
	defer db.Rollback(tx)
	_, err = in.repository().CancelOrSettle(context.Background(), tx, in.Start.Record.HandoffID)
	if !accepted {
		if !errors.Is(err, atc.ErrRunOutputPending) {
			return fmt.Errorf("unproven source became releasable: %v", err)
		}
		return nil
	}
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	ack, err := in.daemonRelease()
	if err != nil {
		return err
	}
	if err := in.recordRelease(ack, false); err != nil {
		return err
	}
	// The daemon must still fence a never-started producer after its hold has
	// been released. Cleanup cannot reopen that execution's first start.
	if mode == "fenced never started" {
		r := in.Start.Replay
		client := jetbridge.NewOutputControlClient(in.Start.Daemon.Output.URL, in.Start.Daemon.HTTP, in.Start.Daemon.Minter, executioncontrol.ActivationEpoch(hangarEpoch))
		if _, err := client.RecordStart(context.Background(), r.Execution, in.Hold.PodUID, "delayed-producer"); err == nil {
			return fmt.Errorf("released never-started source could execute later")
		}
	}
	return nil
}

func staleCancellationEvidence(in RunOutputFinish, evidence db.RunOutputCancellationEvidence) error {
	ctx := context.Background()
	factory := db.NewPipelineRunFactory(in.Start.DB.Conn, in.Start.DB.LockFactory)
	tx, err := in.Start.DB.Conn.Begin()
	if err != nil {
		return err
	}
	defer db.Rollback(tx)
	lease, found, err := factory.ClaimRunCancellationLease(ctx, tx, "source-proof-worker", time.Minute)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("missing first owner")
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if _, err := in.Start.DB.Conn.Exec(`UPDATE pipeline_run_cancellation_worker SET renewed_at=clock_timestamp()-interval '2 seconds',expires_at=clock_timestamp()-interval '1 second'`); err != nil {
		return err
	}
	tx, err = in.Start.DB.Conn.Begin()
	if err != nil {
		return err
	}
	defer db.Rollback(tx)
	_, found, err = factory.ClaimRunCancellationLease(ctx, tx, "new-source-worker", time.Minute)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("missing replacement owner")
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	tx, err = in.Start.DB.Conn.Begin()
	if err != nil {
		return err
	}
	defer db.Rollback(tx)
	if err := in.repository().RecordCancellationEvidence(ctx, tx, lease, in.Start.Record.HandoffID, evidence); !errors.Is(err, db.ErrRunCancellationLeaseLost) {
		return fmt.Errorf("stale source worker recorded evidence: %v", err)
	}
	return nil
}

// Collect from the real node before entering the evidence transaction. A
// never-started observation alone is insufficient: close start first, then
// classify again. Existing finish scenarios retain the real signed witness.
func cancellationEvidence(in RunOutputFinish) (db.RunOutputCancellationEvidence, bool, error) {
	ctx := context.Background()
	tx, err := in.Start.DB.Conn.Begin()
	if err != nil {
		return db.RunOutputCancellationEvidence{}, false, err
	}
	record, err := in.repository().LoadHandoffRecord(ctx, tx, in.Start.Record.HandoffID)
	db.Rollback(tx)
	if err != nil {
		return db.RunOutputCancellationEvidence{}, false, err
	}
	if !record.Source.Reserved() {
		return db.RunOutputCancellationEvidence{}, false, nil
	}
	client := jetbridge.NewOutputControlClient(in.Start.Daemon.Output.URL, in.Start.Daemon.HTTP, in.Start.Daemon.Minter, executioncontrol.ActivationEpoch(hangarEpoch))
	classified, err := client.Classify(ctx, record.Execution)
	if err != nil {
		return db.RunOutputCancellationEvidence{}, false, err
	}
	evidence := db.RunOutputCancellationEvidence{NodeUID: hangarNodeUID, Execution: classified}
	if err := recordInitialCancellationClassification(in, evidence); err != nil {
		return evidence, false, err
	}
	if classified.Classification == executioncontrol.ClassificationNeverStarted {
		closed, err := client.RequestStop(ctx, record.Execution)
		if err != nil {
			return evidence, false, err
		}
		evidence.StartClosure = &closed
		evidence.Execution, err = client.Classify(ctx, record.Execution)
		if err != nil {
			return evidence, false, err
		}
	}
	return evidence, true, nil
}

func recordCancellationEvidence(in RunOutputFinish, evidence db.RunOutputCancellationEvidence, rollback bool) error {
	ctx := context.Background()
	factory := db.NewPipelineRunFactory(in.Start.DB.Conn, in.Start.DB.LockFactory)
	tx, err := in.Start.DB.Conn.Begin()
	if err != nil {
		return err
	}
	lease, found, err := factory.ClaimRunCancellationLease(ctx, tx, "source-proof-worker", time.Minute)
	if err != nil || !found {
		db.Rollback(tx)
		return fmt.Errorf("claiming source proof ownership: %v", err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	tx, err = in.Start.DB.Conn.Begin()
	if err != nil {
		return err
	}
	defer db.Rollback(tx)
	if err := in.repository().RecordCancellationEvidence(ctx, tx, lease, in.Start.Record.HandoffID, evidence); err != nil {
		return err
	}
	if rollback {
		return nil
	}
	return tx.Commit()
}

func retainCancellationEvidence(in RunOutputFinish) error {
	evidence, found, err := cancellationEvidence(in)
	if err != nil || !found {
		return err
	}
	return recordCancellationEvidence(in, evidence, false)
}
