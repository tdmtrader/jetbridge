package steps

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
)

// This coordinates real PostgreSQL sessions; it implements no repository interface.
type logReadInterruption struct {
	admin                   db.DbConn
	collector               db.DbConn
	holderPID, collectorPID int
	table                   string
	release                 func() error
}

func (in LogReady) holdLogRead(rec *brine.Recorder) (*logReadInterruption, error) {
	if len(in.jobs) < 2 || in.jobs[0].pipeline.ID() == in.jobs[1].pipeline.ID() {
		return nil, fmt.Errorf("log read interruption requires affected and healthy pipelines")
	}
	table := ""
	switch in.fault {
	case faultJobsUnreadable:
		table = "jobs"
	case faultBuildsUnreadable:
		table = "builds"
	default:
		return nil, fmt.Errorf("unsupported log read fault %d", in.fault)
	}
	collector, err := ownGCConnection(in.DB, rec)
	if err != nil {
		return nil, err
	}
	// Wait for the coordinator, not a timeout which could also fail the neighbour.
	if _, err = collector.Exec("SET lock_timeout = '0'"); err != nil {
		return nil, err
	}
	if _, err = collector.Exec("SET statement_timeout = '10s'"); err != nil {
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
	gate := &logReadInterruption{admin: in.DB.Conn, collector: collector, table: table}
	var once sync.Once
	var releaseErr error
	gate.release = func() error {
		once.Do(func() {
			releaseErr = tx.Rollback()
			if errors.Is(releaseErr, sql.ErrTxDone) {
				releaseErr = nil
			}
			if releaseErr != nil {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			var locked bool
			releaseErr = gate.admin.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM pg_locks WHERE pid=$1 AND locktype IN ('relation','transactionid') AND granted)", gate.holderPID).Scan(&locked)
			if releaseErr == nil && locked {
				releaseErr = fmt.Errorf("log read blocker %d still holds locks", gate.holderPID)
			}
			if releaseErr == nil {
				fmt.Printf("released owned log-GC read lock %s blocker %d\n", table, gate.holderPID)
			}
		})
		return releaseErr
	}
	TrackDisposer(rec, "the log-read lock on "+table, gate.release)
	if err = tx.QueryRow("SELECT pg_backend_pid()").Scan(&gate.holderPID); err != nil {
		return nil, err
	}
	if err = collector.QueryRow("SELECT pg_backend_pid()").Scan(&gate.collectorPID); err != nil {
		return nil, err
	}
	if gate.holderPID == gate.collectorPID {
		return nil, fmt.Errorf("log read interruption needs distinct sessions")
	}
	// The owned database is cloned from the suite template, which carries
	// pipelines other fixtures saved there (the auth fixture's "private", in a
	// team whose name sorts before this scenario's). The collector visits
	// pipelines in AllPipelines order and skips a paused one without a query,
	// so the first read this gate sees blocked on the table is whichever
	// unpaused pipeline sorts first — not necessarily the affected one. Pause
	// every top-level pipeline the scenario does not own, then require the
	// affected pipeline to lead the collector's order, so the one read that
	// gets cancelled is exactly its own.
	if err = in.leadCollectorOrder(gate.admin, table); err != nil {
		return nil, err
	}
	// Identifier is selected exclusively from the fixed switch above.
	if _, err = tx.Exec("LOCK TABLE " + table + " IN ACCESS EXCLUSIVE MODE"); err != nil {
		return nil, err
	}
	return gate, nil
}

func (in LogReady) leadCollectorOrder(admin db.DbConn, table string) error {
	own := make([]string, 0, len(in.jobs))
	for _, under := range in.jobs {
		own = append(own, strconv.Itoa(under.pipeline.ID()))
	}
	// Pipeline ids are integers the scenario created; nothing here is user text.
	rows, err := admin.Query("UPDATE pipelines SET paused = true WHERE pipeline_run_id IS NULL AND NOT paused AND id NOT IN (" + strings.Join(own, ",") + ") RETURNING id")
	if err != nil {
		return fmt.Errorf("pause pipelines outside the scenario: %w", err)
	}
	var paused []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		paused = append(paused, id)
	}
	rows.Close()
	// Same order as PipelineFactory.AllPipelines; drift fails here, loudly,
	// rather than cancelling a neighbour's read and passing on the affected one.
	var first int
	err = admin.QueryRow(`SELECT p.id FROM pipelines p JOIN teams t ON t.id = p.team_id
   WHERE p.pipeline_run_id IS NULL AND NOT p.paused
   ORDER BY t.name ASC, p.ordering ASC, p.secondary_ordering ASC LIMIT 1`).Scan(&first)
	if err != nil {
		return fmt.Errorf("find the pipeline the log collector reads first: %w", err)
	}
	if affected := in.jobs[0].pipeline.ID(); first != affected {
		return fmt.Errorf("the log collector would read pipeline %d before the affected pipeline %d", first, affected)
	}
	fmt.Printf("paused %v outside the scenario so the first blocked log-GC %s read is pipeline %d's\n", paused, table, in.jobs[0].pipeline.ID())
	return nil
}

// Match the actual blocked SQL and backend before cancelling. Waiting for that
// query to end before unlocking prevents cancellation from racing with success.
func (g *logReadInterruption) interrupt(ctx context.Context) error {
	var query string
	var started time.Time
	err := g.poll(ctx, func() (bool, error) {
		err := g.admin.QueryRowContext(ctx, `
   SELECT a.query,a.query_start FROM pg_stat_activity a
   WHERE a.pid=$1 AND a.datname=current_database()
    AND a.state='active' AND a.wait_event_type='Lock'
    AND $2=ANY(pg_blocking_pids(a.pid))
    AND EXISTS (SELECT 1 FROM pg_locks l WHERE l.pid=a.pid
     AND NOT l.granted AND l.locktype='relation' AND l.relation=$3::regclass
     AND l.database=(SELECT oid FROM pg_database WHERE datname=current_database()))
  `, g.collectorPID, g.holderPID, g.table).Scan(&query, &started)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return err == nil, err
	})
	if err != nil {
		return fmt.Errorf("observe blocked log read: %w", err)
	}
	valid := strings.Contains(query, "FROM jobs j") && strings.Contains(query, "j.pipeline_id =")
	if g.table == "builds" {
		// pg_stat_activity truncates this long projection before FROM.
		// The build projection plus the exact ungranted builds relation lock
		// identifies this read; PID, query_start and text are rechecked below.
		valid = strings.HasPrefix(strings.Join(strings.Fields(query), " "), "SELECT b.id, b.name, b.job_id,")
	}
	if !valid {
		return fmt.Errorf("unexpected blocked %s read: %s", g.table, query)
	}
	var cancelled bool
	err = g.admin.QueryRowContext(ctx, `
  SELECT pg_cancel_backend(a.pid) FROM pg_stat_activity a
  WHERE a.pid=$1 AND a.datname=current_database()
   AND a.state='active' AND a.wait_event_type='Lock'
   AND a.query_start=$2 AND a.query=$3 AND $4=ANY(pg_blocking_pids(a.pid))
 `, g.collectorPID, started, query, g.holderPID).Scan(&cancelled)
	if err != nil {
		return fmt.Errorf("cancel identified log read: %w", err)
	}
	if !cancelled {
		return fmt.Errorf("PostgreSQL did not cancel log read")
	}
	err = g.poll(ctx, func() (bool, error) {
		var ended bool
		err := g.admin.QueryRowContext(ctx, "SELECT state <> 'active' OR query_start <> $2 FROM pg_stat_activity WHERE pid=$1 AND datname=current_database()", g.collectorPID, started).Scan(&ended)
		return ended, err
	})
	if err != nil {
		return fmt.Errorf("await cancelled log read: %w", err)
	}
	fmt.Printf("cancelled actual log-GC %s read blocker %d collector %d query-start %s\n", g.table, g.holderPID, g.collectorPID, started.Format(time.RFC3339Nano))
	return g.release()
}

func (g *logReadInterruption) poll(ctx context.Context, check func() (bool, error)) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		ready, err := check()
		if err != nil {
			return err
		}
		if ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// Separate fixture failures from the production result: original feature
// assertions must still decide whether the collector handles a database error.
func (g *logReadInterruption) run(ctx context.Context, run func(context.Context) error) (error, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- run(ctx) }()
	fixtureErr := g.interrupt(ctx)
	fixtureErr = errors.Join(fixtureErr, g.release())
	// Always unlock before joining, including on fixture failure.
	select {
	case err := <-result:
		return err, fixtureErr
	case <-time.After(12 * time.Second):
		return nil, errors.Join(fixtureErr, fmt.Errorf("log collector did not drain after unlocking"))
	}
}
