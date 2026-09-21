package steps

import (
	"context"
	"fmt"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

type runSourceReservationRequester interface {
	RequestOutputSource(context.Context, db.Tx, int, atc.TaskPlan, int64) error
}

func RunOutputReservationDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[RunOutputFinish, RunOutputFinish]("its Run records intent to reserve the source", func(in RunOutputFinish, _ brine.Params, _ *brine.Recorder) (RunOutputFinish, error) {
			in.Err = in.requestSource()
			return in, in.Err
		}),
		brine.DefineMap[RunOutputFinish, RunOutputFinish]("its Run tries to record intent to reserve the source", func(in RunOutputFinish, _ brine.Params, _ *brine.Recorder) (RunOutputFinish, error) {
			in.Err = in.requestSource()
			return in, nil
		}),
		brine.DefineMap[RunOutputFinish, RunOutputFinish]("its Run controller tries to settle that cancellation", func(in RunOutputFinish, _ brine.Params, _ *brine.Recorder) (RunOutputFinish, error) {
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
		CheckThat[RunOutputFinish]("cancellation remains pending until the reservation attempt is resolved", func(in RunOutputFinish) error {
			if in.Err == nil {
				return fmt.Errorf("cancellation ignored a pending reservation attempt")
			}
			var closed bool
			err := in.Start.DB.Conn.QueryRow(`SELECT EXISTS(SELECT 1 FROM pipeline_run_output_finishes WHERE handoff_id=$1)`, string(in.Start.Record.HandoffID)).Scan(&closed)
			if err != nil {
				return err
			}
			if closed {
				return fmt.Errorf("cancellation closed an unresolved reservation")
			}
			return nil
		}),
		brine.DefineMap[RunOutputFinish, RunOutputFinish]("the daemon reserves the source without a database acknowledgement", func(in RunOutputFinish, _ brine.Params, _ *brine.Recorder) (RunOutputFinish, error) {
			r := in.Start.Record
			client := jetbridge.NewOutputControlClient(in.Start.Daemon.Output.URL, in.Start.Daemon.HTTP, in.Start.Daemon.Minter, executioncontrol.ActivationEpoch(hangarEpoch))
			if _, err := client.Admit(context.Background(), executioncontrol.Envelope{ProtocolVersion: executioncontrol.ProtocolVersion, Identity: r.Execution, ActivationEpoch: r.ActivationEpoch, NodeUID: hangarNodeUID, Capability: "brine-base-control"}); err != nil {
				return in, err
			}
			var err error
			in.Reserved, err = client.ReserveIncarnation(context.Background(), output.CaptureAdmission{ProtocolVersion: output.ProtocolVersion, Execution: r.Execution, ActivationEpoch: r.ActivationEpoch, HandoffID: r.HandoffID, SourceHoldID: r.SourceHoldID, Output: r.Output, CaptureDeadline: r.CaptureDeadline})
			return in, err
		}),
		brine.DefineMap[RunOutputFinish, RunOutputFinish]("another controller records the reserved source", func(in RunOutputFinish, _ brine.Params, _ *brine.Recorder) (RunOutputFinish, error) {
			in.Err = in.recordReservedSource()
			return in, in.Err
		}),
		brine.DefineMap[RunOutputFinish, RunOutputFinish]("another controller tries to record the reserved source", func(in RunOutputFinish, _ brine.Params, _ *brine.Recorder) (RunOutputFinish, error) {
			in.Err = in.recordReservedSource()
			return in, nil
		}),
		CheckThat[RunOutputFinish]("the unadmitted source reply is refused", func(in RunOutputFinish) error {
			if in.Err == nil {
				return fmt.Errorf("a source with no admitted reservation attempt was recorded")
			}
			return nil
		}),
		CheckThat[RunOutputFinish]("the reservation attempt is refused without dispatch authority", func(in RunOutputFinish) error {
			if in.Err == nil {
				return fmt.Errorf("an aborted producer dispatched source reservation")
			}
			var requested bool
			err := in.Start.DB.Conn.QueryRow(`SELECT source_requested_at IS NOT NULL FROM pipeline_run_output_starts WHERE handoff_id=$1`, string(in.Start.Record.HandoffID)).Scan(&requested)
			if err != nil {
				return err
			}
			if requested {
				return fmt.Errorf("refused source dispatch retained authority")
			}
			return nil
		}),
	}
}

func (in RunOutputFinish) requestSource() error {
	factory, ok := db.NewPipelineRunFactory(in.Start.DB.Conn, in.Start.DB.LockFactory).(runSourceReservationRequester)
	if !ok {
		return fmt.Errorf("Run source dispatch has no durable reservation intent")
	}
	tx, err := in.Start.DB.Conn.Begin()
	if err != nil {
		return err
	}
	defer db.Rollback(tx)
	if err := factory.RequestOutputSource(context.Background(), tx, in.Start.Creation.EntryBuilds[0].ID(), in.Start.Plan, int64(hangarEpoch)); err != nil {
		return err
	}
	return tx.Commit()
}
func (in RunOutputFinish) recordReservedSource() error {
	tx, err := in.Start.DB.Conn.Begin()
	if err != nil {
		return err
	}
	defer db.Rollback(tx)
	factory := db.NewPipelineRunFactory(in.Start.DB.Conn, in.Start.DB.LockFactory)
	if err := factory.RecordOutputSource(context.Background(), tx, in.Start.Creation.EntryBuilds[0].ID(), in.Start.Plan, in.Reserved, "brine-node"); err != nil {
		return err
	}
	return tx.Commit()
}
