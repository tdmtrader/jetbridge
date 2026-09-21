package steps

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/hangar/output"
)

type runExecutionStore interface {
	AdmitRunExecution(context.Context, db.Tx, db.RunExecutionRequest) (db.RunExecutionAdmission, bool, error)
	RunExecution(context.Context, db.Tx, int, atc.PlanID) (db.RunExecutionAdmission, bool, error)
}

func RunExecutionAdmissionDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[RunOutputStart, CancellationLeaseResult]("Run execution admission exercises {string}", func(in RunOutputStart, p brine.Params, _ *brine.Recorder) (CancellationLeaseResult, error) {
			mode, _ := p.GetString(0)
			return CancellationLeaseResult{Err: exerciseRunExecutionAdmission(in, mode)}, nil
		}),
		CheckThat[CancellationLeaseResult]("its execution ownership is durable and fenced", func(in CancellationLeaseResult) error { return in.Err }),
	}
}

func exerciseRunExecutionAdmission(in RunOutputStart, mode string) error {
	factory := db.NewPipelineRunFactory(in.DB.Conn, in.DB.LockFactory)
	store, ok := any(factory).(runExecutionStore)
	if !ok {
		return fmt.Errorf("Run factory cannot retain exact executor ownership for job and check builds")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	build := in.Creation.EntryBuilds[0]
	if mode == "check ownership" {
		check, err := requestRunCheck(in, "persisted")
		if err != nil {
			return err
		}
		if check.Err != nil {
			return check.Err
		}
		if check.Build == nil {
			return fmt.Errorf("check was not created")
		}
		build = check.Build
	}
	kind := db.ContainerTypeTask
	if mode == "check ownership" {
		kind = db.ContainerTypeCheck
	}
	req := db.RunExecutionRequest{BuildID: build.ID(), PlanID: "step-1", Kind: kind, Epoch: int64(hangarEpoch), NodeName: "brine-node", NodeUID: hangarNodeUID}
	transaction := func(rollback bool, f func(db.Tx) error) error {
		tx, err := in.DB.Conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer db.Rollback(tx)
		if err = f(tx); err != nil {
			return err
		}
		if rollback {
			return nil
		}
		return tx.Commit()
	}
	admit := func(req db.RunExecutionRequest, rollback bool) (db.RunExecutionAdmission, error) {
		var result db.RunExecutionAdmission
		err := transaction(rollback, func(tx db.Tx) error {
			var found bool
			var err error
			result, found, err = store.AdmitRunExecution(ctx, tx, req)
			if err == nil && !found {
				return fmt.Errorf("Run-owned execution was treated as ordinary work")
			}
			return err
		})
		return result, err
	}
	if mode == "capture identity" {
		record, err := in.start(in.Plan, req.Epoch, req.NodeName, req.NodeUID, false)
		if err != nil {
			return err
		}
		in.Record = record
		req.HandoffID = record.HandoffID
	}
	if mode == "cancellation first" {
		if _, err := acceptRunCancellation(in, "owner", nil, false); err != nil {
			return err
		}
		_, err := admit(req, false)
		if !errors.Is(err, db.ErrPipelineRunCancelling) {
			return fmt.Errorf("cancelled Run admitted execution: %v", err)
		}
		return nil
	}
	var first db.RunExecutionAdmission
	var err error
	if mode == "concurrent admission" {
		in.DB.Conn.SetMaxOpenConns(4)
		var results [2]db.RunExecutionAdmission
		var errs [2]error
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := range results {
			wg.Add(1)
			go func(i int) { defer wg.Done(); <-start; results[i], errs[i] = admit(req, false) }(i)
		}
		close(start)
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				return err
			}
		}
		if results[0] != results[1] {
			return fmt.Errorf("concurrent controllers created different executions")
		}
		first = results[0]
	} else {
		first, err = admit(req, mode == "rollback")
		if err != nil {
			return err
		}
	}
	if first.RunID != in.Creation.Run.ID() || first.BuildID != build.ID() || first.Identity.Validate() != nil {
		return fmt.Errorf("execution has no exact Run/build identity")
	}
	if mode == "capture identity" && first.Identity != in.Record.Execution {
		return fmt.Errorf("capture got a second execution identity")
	}
	if mode == "cancelled replay" || mode == "cancellation discovery" {
		if _, err = acceptRunCancellation(in, "owner", nil, false); err != nil {
			return err
		}
	}
	if mode == "cancelled replay" {
		_, err = admit(req, false)
		if !errors.Is(err, db.ErrPipelineRunCancelling) {
			return fmt.Errorf("replay bypassed cancellation: %v", err)
		}
	} else if mode == "immutable node" {
		req.NodeUID = "replacement-node"
		_, err = admit(req, false)
		if !errors.Is(err, output.ErrConflict) {
			return fmt.Errorf("execution moved to another node: %v", err)
		}
	} else if mode == "cancellation discovery" {
		var lease db.RunCancellationLease
		if err = transaction(false, func(tx db.Tx) error {
			var err error
			lease, _, err = factory.ClaimRunCancellationLease(ctx, tx, "execution-owner", time.Minute)
			return err
		}); err != nil {
			return err
		}
		for i := 0; i < 8; i++ {
			if err = transaction(false, func(tx db.Tx) error {
				_, err := factory.DiscoverRunCancellation(ctx, tx, lease, first.RunID, 100)
				return err
			}); err != nil {
				return err
			}
		}
		var found bool
		if err = in.DB.Conn.QueryRow(`SELECT EXISTS(SELECT 1 FROM pipeline_run_cancellation_operations WHERE run_id=$1 AND kind=$2 AND subject=$3)`, first.RunID, string(db.CancelExecution), fmt.Sprintf("%s/%d", first.Identity.ExecutionID, first.Identity.Fence)).Scan(&found); err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("cancellation cannot discover an execution without an output handoff")
		}
	}
	// A fresh factory reads the original row even when admission is now closed.
	store = any(db.NewPipelineRunFactory(in.DB.Conn, in.DB.LockFactory)).(runExecutionStore)
	return transaction(false, func(tx db.Tx) error {
		read, found, err := store.RunExecution(ctx, tx, build.ID(), req.PlanID)
		if err != nil {
			return err
		}
		if mode == "rollback" {
			if found {
				return fmt.Errorf("rollback retained an execution")
			}
			return nil
		}
		if !found || read != first {
			return fmt.Errorf("fresh controller lost the original execution")
		}
		return nil
	})
}
