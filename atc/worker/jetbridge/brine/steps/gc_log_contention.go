package steps

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
)

// Cursor initialization has already finished when this is called. The entire
// factory uses a real bounded session; only the first pipeline's write is locked.
func (in LogReady) holdLogWrite(rec *brine.Recorder) (db.DbConn, error) {
	if len(in.jobs) < 2 || in.jobs[0].pipeline.ID() == in.jobs[1].pipeline.ID() {
		return nil, fmt.Errorf("log contention requires distinct affected and healthy pipelines")
	}
	first := in.jobs[0]
	collector, err := ownGCConnection(in.DB, rec)
	if err != nil {
		return nil, err
	}
	holder, err := ownGCConnection(in.DB, rec)
	if err != nil {
		return nil, err
	}
	tx, err := holder.Begin()
	if err != nil {
		return nil, err
	}
	var blockerPID, collectorPID int
	kind := "events"
	if in.fault == faultCursorRefused {
		kind = "cursor"
	}
	TrackDisposer(rec, "the owned log-GC "+kind+" lock", func() error {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			return err
		}
		var locked bool
		if err := in.DB.Conn.QueryRow("SELECT EXISTS (SELECT 1 FROM pg_locks WHERE pid=$1 AND locktype IN ('relation','transactionid') AND granted)", blockerPID).Scan(&locked); err != nil {
			return err
		}
		if locked {
			return fmt.Errorf("log-GC blocker %d still holds a lock", blockerPID)
		}
		fmt.Printf("released owned log-GC %s lock pipeline %d job %d blocker %d\n", kind, first.pipeline.ID(), first.job.ID(), blockerPID)
		return nil
	})
	if err := tx.QueryRow("SELECT pg_backend_pid()").Scan(&blockerPID); err != nil {
		return nil, err
	}
	if err := collector.QueryRow("SELECT pg_backend_pid()").Scan(&collectorPID); err != nil {
		return nil, err
	}
	if blockerPID == collectorPID {
		return nil, fmt.Errorf("log contention needs separate PostgreSQL sessions")
	}
	var probeErr error
	switch in.fault {
	case faultDeleteRefused:
		// The identifier comes only from the real positive database ID.
		if first.pipeline.ID() <= 0 {
			return nil, fmt.Errorf("log contention requires a persisted pipeline")
		}
		table := fmt.Sprintf("pipeline_build_events_%d", first.pipeline.ID())
		if _, err := tx.Exec("LOCK TABLE " + table + " IN SHARE MODE"); err != nil {
			return nil, err
		}
		_, probeErr = collector.Exec("DELETE FROM " + table + " WHERE false")
	case faultCursorRefused:
		var observedID int
		// NO KEY UPDATE still blocks the cursor update but permits unrelated FK checks.
		if err := tx.QueryRow("SELECT id FROM jobs WHERE id=$1 FOR NO KEY UPDATE", first.job.ID()).Scan(&observedID); err != nil {
			return nil, err
		}
		if observedID != first.job.ID() {
			return nil, fmt.Errorf("locked the wrong log cursor")
		}
		_, probeErr = collector.Exec("UPDATE jobs SET first_logged_build_id=first_logged_build_id WHERE id=$1", first.job.ID())
	default:
		return nil, fmt.Errorf("unsupported log contention fault %d", in.fault)
	}
	if gcSQLState(probeErr) != "55P03" {
		return nil, fmt.Errorf("expected actual log-GC lock timeout, got %v", probeErr)
	}
	fmt.Printf("actual log-GC %s contention pipeline %d job %d blocker %d collector %d SQLSTATE %s\n", kind, first.pipeline.ID(), first.job.ID(), blockerPID, collectorPID, gcSQLState(probeErr))
	return collector, nil
}

// The production collector deliberately swallows per-job errors. Retain its
// actual ERROR output as evidence alongside the original row/cursor assertions.
func observeLogSweep(in LogReady, run func(context.Context) error, interruption *logReadInterruption) (LogSwept, error) {
	if interruption == nil && in.fault != faultDeleteRefused && in.fault != faultCursorRefused {
		return LogSwept{Ready: in, Err: run(in.Ctx)}, nil
	}
	var output bytes.Buffer
	logger := lager.NewLogger("brine-log-contention")
	logger.RegisterSink(lager.NewWriterSink(&output, lager.ERROR))
	ctx := lagerctx.NewContext(in.Ctx, logger)
	var err error
	if interruption != nil {
		var fixtureErr error
		err, fixtureErr = interruption.run(ctx, run)
		if fixtureErr != nil {
			return LogSwept{}, fixtureErr
		}
	} else {
		err = run(ctx)
	}
	fmt.Printf("actual log-GC production errors: %s", output.String())
	return LogSwept{Ready: in, Err: err}, nil
}
