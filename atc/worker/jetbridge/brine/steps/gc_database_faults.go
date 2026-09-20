package steps

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/gc"
)

// Each pool is the owned scenario database's real production connection,
// limited by postgresrunner to one session. Neither repository nor SQL is wrapped.
func ownGCConnection(database JetbridgeDB, rec *brine.Recorder) (db.DbConn, error) {
	conn := database.runner.OpenConn()
	TrackDisposer(rec, "the owned GC connection", func() error {
		if err := conn.Close(); err != nil {
			return fmt.Errorf("close owned GC connection: %w", err)
		}
		return nil
	})
	if _, err := conn.Exec("SET statement_timeout = '3s'"); err != nil {
		return nil, err
	}
	if _, err := conn.Exec("SET lock_timeout = '250ms'"); err != nil {
		return nil, err
	}
	return conn, nil
}

func gcSQLState(err error) string {
	var state interface{ SQLState() string }
	if errors.As(err, &state) {
		return state.SQLState()
	}
	return ""
}

// Privileges are removed only from a fresh NOLOGIN role on the owned clone.
// Independent zero-row SQL probes prove the refusal without changing data.
func gcPermissionFailure(pattern, table, privilege string) brine.StepDefinition {
	return brine.DefineMap[ContainerGCReady, ContainerGCReady](pattern,
		func(in ContainerGCReady, _ brine.Params, rec *brine.Recorder) (ContainerGCReady, error) {
			probe := ""
			switch {
			case table == "builds" && privilege == "SELECT":
				probe = "SELECT id FROM builds LIMIT 0"
			case table == "containers" && privilege == "DELETE":
				probe = "DELETE FROM containers WHERE false"
			default:
				return ContainerGCReady{}, fmt.Errorf("unsupported GC privilege boundary")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var nonce [8]byte
			if _, err := rand.Read(nonce[:]); err != nil {
				return ContainerGCReady{}, err
			}
			// Only this fixed prefix and hex bytes enter the SQL identifier.
			role := fmt.Sprintf("brine_gc_%x", nonce)
			if _, err := in.DB.Conn.ExecContext(ctx, "CREATE ROLE "+role+" NOLOGIN"); err != nil {
				return ContainerGCReady{}, err
			}
			TrackDisposer(rec, "the owned GC role "+role, func() error {
				clean, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if _, err := in.DB.Conn.ExecContext(clean, "DROP OWNED BY "+role); err != nil {
					return err
				}
				if _, err := in.DB.Conn.ExecContext(clean, "DROP ROLE "+role); err != nil {
					return err
				}
				var remains bool
				if err := in.DB.Conn.QueryRowContext(clean, "SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname=$1)", role).Scan(&remains); err != nil {
					return err
				}
				if remains {
					return fmt.Errorf("owned GC role %s remains", role)
				}
				fmt.Printf("removed owned GC role %s\n", role)
				return nil
			})
			fmt.Printf("created owned GC role %s\n", role)
			for _, query := range []string{
				"GRANT USAGE ON SCHEMA public TO " + role,
				"GRANT SELECT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO " + role,
				"REVOKE " + privilege + " ON " + table + " FROM " + role,
			} {
				if _, err := in.DB.Conn.ExecContext(ctx, query); err != nil {
					return ContainerGCReady{}, err
				}
			}
			conn, err := ownGCConnection(in.DB, rec)
			if err != nil {
				return ContainerGCReady{}, err
			}
			if _, err := conn.ExecContext(ctx, "SET ROLE "+role); err != nil {
				return ContainerGCReady{}, err
			}
			var actualRole string
			var allowed bool
			if err := conn.QueryRowContext(ctx, "SELECT current_user, has_table_privilege(current_user, $1, $2)", table, privilege).Scan(&actualRole, &allowed); err != nil {
				return ContainerGCReady{}, err
			}
			if actualRole != role || allowed {
				return ContainerGCReady{}, fmt.Errorf("GC permission boundary was not installed")
			}
			_, probeErr := conn.ExecContext(ctx, probe)
			if gcSQLState(probeErr) != "42501" {
				return ContainerGCReady{}, fmt.Errorf("expected real permission refusal, got %v", probeErr)
			}
			fmt.Printf("actual GC permission fault role %s table %s privilege %s SQLSTATE %s\n", role, table, privilege, gcSQLState(probeErr))
			in.Repo = db.NewContainerRepository(conn)
			in.Collector = gc.NewContainerCollector(in.Repo, gcGracePeriod, gcGracePeriod)
			in.faultState = "42501"
			return in, nil
		})
}

// Hold one actual failed row, leaving the orphaned/missing/check rows writable.
func gcFailedContainerContention() brine.StepDefinition {
	return brine.DefineMap[ContainerGCReady, ContainerGCReady](
		"a competing transaction holds the failed container {string}",
		func(in ContainerGCReady, p brine.Params, rec *brine.Recorder) (ContainerGCReady, error) {
			name, err := paramAt("a competing transaction holds the failed container {string}", p, 0)
			if err != nil {
				return ContainerGCReady{}, err
			}
			handle, ok := in.handle[name]
			if !ok {
				return ContainerGCReady{}, fmt.Errorf("no failed container named %q", name)
			}
			collector, err := ownGCConnection(in.DB, rec)
			if err != nil {
				return ContainerGCReady{}, err
			}
			holder, err := ownGCConnection(in.DB, rec)
			if err != nil {
				return ContainerGCReady{}, err
			}
			tx, err := holder.Begin()
			if err != nil {
				return ContainerGCReady{}, err
			}
			var blockerPID, rowID int
			var state string
			TrackDisposer(rec, "the owned GC row lock on handle "+handle, func() error {
				if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
					return err
				}
				var locked bool
				if err := in.DB.Conn.QueryRow("SELECT EXISTS (SELECT 1 FROM pg_locks WHERE pid=$1 AND locktype='transactionid' AND mode='ExclusiveLock')", blockerPID).Scan(&locked); err != nil {
					return err
				}
				if locked {
					return fmt.Errorf("GC blocker %d still holds a transaction lock", blockerPID)
				}
				fmt.Printf("released owned GC row lock handle %s blocker %d\n", handle, blockerPID)
				return nil
			})
			if err := tx.QueryRow("SELECT pg_backend_pid(), id, state FROM containers WHERE handle=$1 FOR UPDATE", handle).Scan(&blockerPID, &rowID, &state); err != nil {
				return ContainerGCReady{}, err
			}
			if state != "failed" {
				return ContainerGCReady{}, fmt.Errorf("GC contention needs an actual failed row, got %q", state)
			}
			var collectorPID int
			if err := collector.QueryRow("SELECT pg_backend_pid()").Scan(&collectorPID); err != nil {
				return ContainerGCReady{}, err
			}
			if collectorPID == blockerPID {
				return ContainerGCReady{}, fmt.Errorf("GC contention requires separate PostgreSQL sessions")
			}
			_, probeErr := collector.Exec("UPDATE containers SET state=state WHERE id=$1", rowID)
			if gcSQLState(probeErr) != "55P03" {
				return ContainerGCReady{}, fmt.Errorf("expected actual failed-row lock timeout, got %v", probeErr)
			}
			fmt.Printf("actual GC row contention handle %s id %d blocker %d collector %d SQLSTATE %s\n", handle, rowID, blockerPID, collectorPID, gcSQLState(probeErr))
			in.Repo = db.NewContainerRepository(collector)
			in.Collector = gc.NewContainerCollector(in.Repo, gcGracePeriod, gcGracePeriod)
			in.faultState = "55P03"
			return in, nil
		})
}
