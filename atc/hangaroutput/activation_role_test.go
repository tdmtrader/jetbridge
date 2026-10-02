package hangaroutput_test

import (
	"context"
	"errors"
	"testing"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput/activation"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// The activation database role runs every activation command against real
// PostgreSQL, logged in as itself over SCRAM (hangar_activation_db_role A1).
func TestTheActivationDatabaseRoleRunsEveryActivationCommand(t *testing.T) {
	epochs, owner := activationFixture(t)
	ctx := context.Background()
	const epoch = executioncontrol.ActivationEpoch(91)

	mustBegin(t, epochs, epoch)
	mustAttestBase(t, epochs, epoch)
	if _, err := epochs.EnableStep(ctx, epoch, activation.FacetBase, false); err != nil {
		t.Fatalf("the enable pre-flight, as the role: %v", err)
	}
	if err := epochs.Enable(ctx, epoch, activation.FacetBase); err != nil {
		t.Fatalf("enable base, as the role: %v", err)
	}
	mustAttestAndEnableOutput(t, epochs, epoch)
	if _, err := epochs.DrainResidue(ctx, epoch, activation.FacetOutput); err != nil {
		t.Fatalf("the drain residue read, as the role: %v", err)
	}
	if _, err := epochs.DrainStep(ctx, epoch, activation.FacetOutput, false); err != nil {
		t.Fatalf("drain output, as the role: %v", err)
	}

	// A real finding, recorded by web's side, reconciled by the role.
	if _, err := owner.Exec(`INSERT INTO hangar_policy_violations (activation_epoch, violation, subject)
		VALUES ($1, 'out_of_band_absence', 'objects/lost')`, int64(epoch)); err != nil {
		t.Fatalf("recording a finding as web: %v", err)
	}
	tx, err := epochs.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := (&db.HangarOutputRepository{}).ReconcilePolicyViolation(ctx, tx, int64(epoch),
		output.PolicyViolation("out_of_band_absence"), "objects/lost"); err != nil {
		t.Fatalf("reconcile-integrity, as the role: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var resolved bool
	if err := owner.QueryRow(`SELECT resolved_at IS NOT NULL FROM hangar_policy_violations WHERE subject = 'objects/lost'`).
		Scan(&resolved); err != nil || !resolved {
		t.Fatalf("the finding is not resolved (%v, %v)", resolved, err)
	}
}

// The role may do nothing else: every other operation on every application
// table and column, and DDL, is a permission error (A2). The tables are
// enumerated through the owner, so one the role cannot even see is still in
// the matrix.
func TestTheActivationDatabaseRoleMayDoNothingElse(t *testing.T) {
	epochs, owner := activationFixture(t)
	role := epochs.DB

	selectable := map[string]bool{
		"hangar_output_activation_epochs": true, "migrations_history": true, "hangar_policy_violations": true,
		"hangar_handoff_predeclarations": true, "hangar_handoff_dispositions": true, "hangar_capture_reservations": true,
		"hangar_exact_lifecycles": true, "hangar_claims": true, "hangar_read_leases": true,
		"hangar_reclaim_jobs": true, "hangar_inventory_debt": true,
	}
	insertable := map[string]bool{"hangar_output_activation_epochs": true}
	updatable := map[string]map[string]bool{
		"hangar_output_activation_epochs": set("base_state", "base_attestation", "protocol_version", "ledger_version",
			"cohort_digest", "output_state", "output_attestation", "receipt_public_key_id", "receipt_key_valid_from",
			"receipt_key_valid_until", "materialization_key_id", "bucket_fingerprint", "derived_namespace",
			"revision", "updated_at"),
		"hangar_policy_violations": set("resolved_at"),
	}

	rows, err := owner.Query(`
		SELECT c.table_name, c.column_name
		  FROM information_schema.columns c
		  JOIN information_schema.tables t ON t.table_schema = c.table_schema AND t.table_name = c.table_name
		 WHERE c.table_schema = 'public' AND t.table_type = 'BASE TABLE'
		 ORDER BY c.table_name, c.ordinal_position`)
	if err != nil {
		t.Fatal(err)
	}
	columns := map[string][]string{}
	var tables []string
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			t.Fatal(err)
		}
		if columns[table] == nil {
			tables = append(tables, table)
		}
		columns[table] = append(columns[table], column)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	type refusal struct{ name, statement string }
	var matrix []refusal
	for _, table := range tables {
		name := pgx.Identifier{table}.Sanitize()
		if !selectable[table] {
			matrix = append(matrix, refusal{"select " + table, "SELECT 1 FROM " + name + " LIMIT 0"})
		}
		if !insertable[table] {
			matrix = append(matrix, refusal{"insert " + table, "INSERT INTO " + name + " DEFAULT VALUES"})
		}
		for _, column := range columns[table] {
			if !updatable[table][column] {
				col := pgx.Identifier{column}.Sanitize()
				matrix = append(matrix, refusal{"update " + table + "." + column,
					"UPDATE " + name + " SET " + col + " = " + col + " WHERE false"})
			}
		}
		matrix = append(matrix,
			refusal{"delete " + table, "DELETE FROM " + name + " WHERE false"},
			refusal{"truncate " + table, "TRUNCATE " + name})
	}
	matrix = append(matrix,
		refusal{"alter a table", "ALTER TABLE hangar_output_activation_epochs ADD COLUMN smuggled int"},
		refusal{"drop a table", "DROP TABLE hangar_claims"},
		refusal{"drop the guard", "DROP TRIGGER hangar_output_activation_role_guard ON hangar_output_activation_epochs"},
	)
	var version int
	if err := owner.QueryRow(`SELECT current_setting('server_version_num')::int`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version >= 150000 {
		matrix = append(matrix,
			refusal{"create a table", "CREATE TABLE smuggled (id int)"},
			refusal{"create a function", "CREATE FUNCTION smuggled() RETURNS int LANGUAGE sql AS 'SELECT 1'"},
		)
	} else {
		// Below PostgreSQL 15, PUBLIC holds CREATE on schema public, so a
		// non-temporary CREATE succeeds for any role. CI and concourse.home
		// run 17, where these rows run.
		t.Logf("PostgreSQL %d: the CREATE rows run only on 15 and later", version)
	}
	if len(tables) < 10 || len(matrix) < 100 {
		t.Fatalf("the matrix enumerated %d tables and %d refusals; the scan is broken", len(tables), len(matrix))
	}

	for _, row := range matrix {
		_, err := role.Exec(row.statement)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Errorf("%s: got %v, want a permission error (42501)", row.name, err)
		}
	}
}

func set(values ...string) map[string]bool {
	out := map[string]bool{}
	for _, value := range values {
		out[value] = true
	}
	return out
}
