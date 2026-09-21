package steps

import (
	"context"
	"fmt"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

type cancellationClassifier interface {
	RecordCancellationClassification(context.Context, db.Tx, db.RunCancellationLease, output.HandoffID, db.RunOutputCancellationEvidence) error
}

func CancellationClassificationDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[RunOutputFinish, CancellationLeaseResult]("cancellation retains its initial handoff classification", func(in RunOutputFinish, _ brine.Params, _ *brine.Recorder) (CancellationLeaseResult, error) {
			return CancellationLeaseResult{Err: exerciseCancellationClassification(in)}, nil
		}),
		CheckThat[CancellationLeaseResult]("the initial classification survives a replacement observer", func(in CancellationLeaseResult) error { return in.Err }),
	}
}

func exerciseCancellationClassification(in RunOutputFinish) error {
	if _, err := acceptRunCancellation(in.Start, "owner", nil, false); err != nil {
		return err
	}
	client := jetbridge.NewOutputControlClient(in.Start.Daemon.Output.URL, in.Start.Daemon.HTTP, in.Start.Daemon.Minter, executioncontrol.ActivationEpoch(hangarEpoch))
	classified, err := client.Classify(context.Background(), in.Start.Record.Execution)
	if err != nil {
		return err
	}
	evidence := db.RunOutputCancellationEvidence{NodeUID: hangarNodeUID, Execution: classified}
	if err := recordInitialCancellationClassification(in, evidence); err != nil {
		return err
	}
	var before, after string
	var proof int
	query := `SELECT row_to_json(c)::text FROM pipeline_run_output_cancellation_classifications c WHERE handoff_id=$1`
	if err := in.Start.DB.Conn.QueryRow(query, string(in.Start.Record.HandoffID)).Scan(&before); err != nil {
		return err
	}
	if err := recordInitialCancellationClassification(in, evidence); err != nil {
		return err
	}
	if err := in.Start.DB.Conn.QueryRow(query, string(in.Start.Record.HandoffID)).Scan(&after); err != nil {
		return err
	}
	if before != after {
		return fmt.Errorf("replacement observer changed the first classification")
	}
	if err := in.Start.DB.Conn.QueryRow(`SELECT count(*) FROM pipeline_run_output_cancellation_evidence WHERE handoff_id=$1`, string(in.Start.Record.HandoffID)).Scan(&proof); err != nil {
		return err
	}
	if proof != 0 {
		return fmt.Errorf("classification was mistaken for a closed execution")
	}
	return nil
}

func recordInitialCancellationClassification(in RunOutputFinish, evidence db.RunOutputCancellationEvidence) error {
	classifier, ok := any(in.repository()).(cancellationClassifier)
	if !ok {
		return fmt.Errorf("Run has no durable initial handoff classification")
	}
	ctx := context.Background()
	factory := db.NewPipelineRunFactory(in.Start.DB.Conn, in.Start.DB.LockFactory)
	tx, err := in.Start.DB.Conn.Begin()
	if err != nil {
		return err
	}
	lease, found, err := factory.ClaimRunCancellationLease(ctx, tx, "source-proof-worker", time.Minute)
	if err != nil || !found {
		db.Rollback(tx)
		return fmt.Errorf("claiming classification ownership: %v", err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	tx, err = in.Start.DB.Conn.Begin()
	if err != nil {
		return err
	}
	defer db.Rollback(tx)
	if err := classifier.RecordCancellationClassification(ctx, tx, lease, in.Start.Record.HandoffID, evidence); err != nil {
		return err
	}
	return tx.Commit()
}
