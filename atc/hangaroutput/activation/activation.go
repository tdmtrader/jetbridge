// Package activation is the only production writer of
// hangar_output_activation_epochs.
//
// The epoch row is the plane's single authority. A node label is a scheduling
// hint, a daemon handshake is evidence, a Helm value is an intention -- none of
// them authorizes a capture, a receipt registration, a claim acquisition or a
// finalization. This row does, and it moves only through the four guarded
// transitions below.
//
// Why a separate package and a separate binary. The rule "only the activation
// command writes this table" is enforceable rather than aspirational because
// the Jobs that run it have a PostgreSQL role of their own (decision F4), and
// an architecture guard reads the repository for any other writer. Two facets,
// two independent state machines in one row, and the constraint that output can
// never be in service while base is not is in the SCHEMA rather than here.
package activation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// Facet is one of the two independent state machines in an epoch row.
type Facet string

const (
	// FacetBase is exact execution control: the ledger, the control key, the
	// protocol and a homogeneous attested runtime cohort.
	FacetBase Facet = "base"
	// FacetOutput is the durable-capture extension on top of it.
	FacetOutput Facet = "output"
)

// ErrStaleEpoch is the typed refusal every CAS returns when the row moved under
// it.
//
// Typed, and distinct from "no such epoch", because the two mean opposite
// things to an operator: a stale revision is a concurrent activation that
// already won, and retrying it is correct only after re-reading; a missing
// epoch is a `begin` that never happened, and retrying will never help.
var ErrStaleEpoch = errors.New("hangar/activation: stale epoch")

// Column returns the state column this facet moves.
func (facet Facet) Column() (string, error) {
	switch facet {
	case FacetBase:
		return "base_state", nil
	case FacetOutput:
		return "output_state", nil
	}

	return "", fmt.Errorf("%w: %q is not a facet; there are two, base and output",
		output.ErrUnknownMember, facet)
}

// ParseFacet reads a facet from a flag.
func ParseFacet(value string) (Facet, error) {
	facet := Facet(strings.TrimSpace(value))
	if _, err := facet.Column(); err != nil {
		return "", err
	}

	return facet, nil
}

// Evidence is what an attestation writes into the row.
//
// The base half is the cohort: what the daemons on this cluster speak, and a
// digest over the whole set so that "homogeneous" is a value somebody can
// compare rather than a claim somebody made. The output half is the identity a
// receipt and a read warrant are checked against.
//
// It is one struct with both halves because the two attestations write into one
// row and the schema's CHECK constraints are stated across them: a row cannot be
// output-enabled without both key ids, and they must differ.
type Evidence struct {
	Attestation json.RawMessage

	// Base.
	ProtocolVersion string
	LedgerVersion   string
	CohortDigest    string

	// Output.
	ReceiptPublicKeyID   string
	ReceiptKeyValidFrom  time.Time
	ReceiptKeyValidUntil time.Time
	MaterializationKeyID string
	BucketFingerprint    string
	DerivedNamespace     string
}

// Validate refuses evidence that cannot justify the transition it is for.
func (evidence Evidence) Validate(facet Facet) error {
	if len(evidence.Attestation) == 0 {
		return fmt.Errorf("%w: an attestation with no evidence bundle. The row records what "+
			"justified the transition precisely so that an enabled epoch cannot exist without "+
			"it", output.ErrIncomplete)
	}
	if facet == FacetBase {
		if evidence.ProtocolVersion == "" || evidence.LedgerVersion == "" {
			return fmt.Errorf("%w: the base attestation names no protocol or ledger version",
				output.ErrIncomplete)
		}
		if evidence.CohortDigest == "" {
			return fmt.Errorf("%w: the base attestation carries no cohort digest. "+
				"Homogeneity is what the base facet attests, and a digest is what makes it "+
				"comparable rather than asserted", output.ErrIncomplete)
		}

		return nil
	}

	for _, required := range []struct{ name, value string }{
		{"receipt public key id", evidence.ReceiptPublicKeyID},
		{"materialization key id", evidence.MaterializationKeyID},
		{"bucket fingerprint", evidence.BucketFingerprint},
		{"derived namespace", evidence.DerivedNamespace},
	} {
		if required.value == "" {
			return fmt.Errorf("%w: the output attestation names no %s",
				output.ErrIncomplete, required.name)
		}
	}
	if evidence.ReceiptPublicKeyID == evidence.MaterializationKeyID {
		return fmt.Errorf("%w: the receipt and materialization key ids are the same. A read "+
			"warrant must not be signable by anything that can mint a publication receipt",
			output.ErrIncomplete)
	}
	if !evidence.ReceiptKeyValidUntil.After(evidence.ReceiptKeyValidFrom) {
		return fmt.Errorf("%w: the receipt key's validity window is empty", output.ErrIncomplete)
	}

	return nil
}

// Epochs is the CAS row, and every method on it is one statement.
//
// One statement, not a transaction with a read and a write: the predicate IS the
// WHERE clause, so two concurrent activations cannot both observe `attested` and
// both write `enabled`. The revision is what makes it a compare-and-swap rather
// than a last-writer-wins, and the schema's trigger refuses any write that does
// not advance it.
type Epochs struct {
	DB *sql.DB
}

// Begin creates the next epoch row in initial/initial.
//
// Rotation calls it while the outgoing epoch is still enabled: the partial
// unique indexes are per facet and scoped to `enabled`, so an `initial` row and
// an `enabled` row coexist by design, and so do a `draining` one and an
// `enabled` one. That overlap is what gives rotation no emission gap.
func (epochs Epochs) Begin(ctx context.Context, epoch executioncontrol.ActivationEpoch) error {
	if epoch == 0 {
		return fmt.Errorf("%w: an epoch id of zero; zero is the absence of an epoch",
			output.ErrIncomplete)
	}
	result, err := epochs.DB.ExecContext(ctx, `
		INSERT INTO hangar_output_activation_epochs (epoch_id)
		VALUES ($1)
		ON CONFLICT (epoch_id) DO NOTHING`, int64(epoch))
	if err != nil {
		return fmt.Errorf("%w: beginning epoch %d: %v", output.ErrInfrastructure, epoch, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%w: beginning epoch %d: %v", output.ErrInfrastructure, epoch, err)
	}
	if affected == 0 {
		return fmt.Errorf("%w: epoch %d already exists. An epoch row's identity is immutable "+
			"and a facet never moves backwards, so beginning an existing epoch would either "+
			"be a no-op an operator read as progress or a rewrite the schema refuses",
			output.ErrConflict, epoch)
	}

	return nil
}

// Attest records the evidence and moves the facet to `attested`.
//
// From `initial` or `attesting` only. `attested -> attesting` is a backwards
// move the schema's trigger refuses, and deliberately: re-attesting an enabled
// epoch in place would let a cohort change under a running plane without the
// captures that recorded the old epoch noticing. A new cohort is a new epoch.
func (epochs Epochs) Attest(ctx context.Context, epoch executioncontrol.ActivationEpoch,
	facet Facet, evidence Evidence) error {
	column, err := facet.Column()
	if err != nil {
		return err
	}
	if err := evidence.Validate(facet); err != nil {
		return err
	}

	var statement string
	var arguments []any
	if facet == FacetBase {
		statement = `
			UPDATE hangar_output_activation_epochs
			   SET base_state = 'attested',
			       base_attestation = $2::jsonb,
			       protocol_version = $3,
			       ledger_version = $4,
			       cohort_digest = $5,
			       revision = revision + 1,
			       updated_at = now()
			 WHERE epoch_id = $1
			   AND base_state IN ('initial', 'attesting')`
		arguments = []any{int64(epoch), []byte(evidence.Attestation),
			evidence.ProtocolVersion, evidence.LedgerVersion, evidence.CohortDigest}
	} else {
		statement = `
			UPDATE hangar_output_activation_epochs
			   SET output_state = 'attested',
			       output_attestation = $2::jsonb,
			       receipt_public_key_id = $3,
			       receipt_key_valid_from = $4,
			       receipt_key_valid_until = $5,
			       materialization_key_id = $6,
			       bucket_fingerprint = $7,
			       derived_namespace = $8,
			       revision = revision + 1,
			       updated_at = now()
			 WHERE epoch_id = $1
			   AND output_state IN ('initial', 'attesting')
			   AND base_state IN ('attested', 'enabled')`
		arguments = []any{int64(epoch), []byte(evidence.Attestation),
			evidence.ReceiptPublicKeyID, evidence.ReceiptKeyValidFrom,
			evidence.ReceiptKeyValidUntil, evidence.MaterializationKeyID,
			evidence.BucketFingerprint, evidence.DerivedNamespace}
	}

	return epochs.apply(ctx, epoch, facet, column, "attested", statement, arguments)
}

// Enable moves an attested facet into service.
//
// The `attested` precondition is the whole guard: nothing can be enabled that
// was not attested, and the evidence that justified it is already in the row
// because Attest wrote it in the same statement that moved the state.
func (epochs Epochs) Enable(ctx context.Context, epoch executioncontrol.ActivationEpoch,
	facet Facet) error {
	column, err := facet.Column()
	if err != nil {
		return err
	}

	statement := fmt.Sprintf(`
		UPDATE hangar_output_activation_epochs
		   SET %s = 'enabled', revision = revision + 1, updated_at = now()
		 WHERE epoch_id = $1
		   AND %s = 'attested'`, column, column)
	if facet == FacetOutput {
		// The SCHEMA's own readiness predicate, which is the same one Rotate
		// takes. Output effective admission is the conjunction of base
		// readiness, the output facets and the CURRENT policy, so the base
		// check is here as well as in the CHECK -- but it is the same check.
		//
		// It used to be the stricter `base_state = 'enabled'`, and that was a
		// one-way door. A rotation leaves the incoming row at
		// base=attested/output=enabled, because the incoming row's base facet
		// cannot be enabled while the outgoing row's still is; so after the
		// first output rotation the serving row's base sits at `attested`
		// permanently. Take the output facet out of service from there and
		// there is nothing left to rotate FROM and nothing whose base is
		// `enabled` to rotate TO -- the only such row's output facet is
		// terminally disabled -- and the plane's output half could not be
		// turned back on at all.
		//
		// base=attested with output=enabled is not a state this loosens the
		// plane into: it is the state every output rotation already produces
		// and runs in, and the schema's hangar_output_epoch_needs_base admits
		// it by name.
		statement += ` AND base_state IN ('attested', 'enabled')`
	}

	return epochs.apply(ctx, epoch, facet, column, "enabled", statement, []any{int64(epoch)})
}

// Drain takes an enabled facet out of service, and then to `disabled`.
//
// Two steps and not one, because they mean different things. `draining` stops
// NEW admission while every settlement already in flight continues -- releases,
// terminal dispositions, an admitted conditional delete finishing. `disabled` is
// terminal and says the facet holds nothing; the caller's predicate is what
// decides whether that is true, and this method refuses to assert it on the
// caller's behalf.
func (epochs Epochs) Drain(ctx context.Context, epoch executioncontrol.ActivationEpoch,
	facet Facet) error {
	column, err := facet.Column()
	if err != nil {
		return err
	}

	statement := fmt.Sprintf(`
		UPDATE hangar_output_activation_epochs
		   SET %s = 'draining', revision = revision + 1, updated_at = now()
		 WHERE epoch_id = $1
		   AND %s = 'enabled'`, column, column)

	return epochs.apply(ctx, epoch, facet, column, "draining", statement, []any{int64(epoch)})
}

// Disable is the terminal step, and it is separate from Drain on purpose.
//
// Nothing here checks whether the facet is empty: `DrainStep` does, against the
// live tables, and refuses long before this runs. A method that both decided
// emptiness and wrote the terminal state would be one where "the check passed"
// and "the check was skipped" produce the same row.
//
// That separation used to mean the decision lived in `cmd/hangar-output-activate`
// and was exercised by nothing, which is why `DrainStep` exists.
func (epochs Epochs) Disable(ctx context.Context, epoch executioncontrol.ActivationEpoch,
	facet Facet) error {
	column, err := facet.Column()
	if err != nil {
		return err
	}

	statement := fmt.Sprintf(`
		UPDATE hangar_output_activation_epochs
		   SET %s = 'disabled', revision = revision + 1, updated_at = now()
		 WHERE epoch_id = $1
		   AND %s = 'draining'`, column, column)

	return epochs.apply(ctx, epoch, facet, column, "disabled", statement, []any{int64(epoch)})
}

// State reads both facets and the revision.
type State struct {
	Epoch    executioncontrol.ActivationEpoch
	Base     string
	Output   string
	Revision int64
}

// Read returns one epoch row.
func (epochs Epochs) Read(ctx context.Context,
	epoch executioncontrol.ActivationEpoch) (State, error) {
	state := State{Epoch: epoch}
	err := epochs.DB.QueryRowContext(ctx, `
		SELECT base_state, output_state, revision
		  FROM hangar_output_activation_epochs
		 WHERE epoch_id = $1`, int64(epoch)).Scan(&state.Base, &state.Output, &state.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return State{}, fmt.Errorf("%w: no activation epoch %d. `begin` creates one; nothing "+
			"else does, and no migration inserts one", output.ErrNotFound, epoch)
	}
	if err != nil {
		return State{}, fmt.Errorf("%w: reading epoch %d: %v",
			output.ErrInfrastructure, epoch, err)
	}

	return state, nil
}

// apply runs one CAS and turns "no rows" into the typed refusal.
//
// The distinction it draws is the one an operator needs: a row that does not
// exist is a `begin` that never happened and no retry will help; a row whose
// facet is somewhere else is a transition somebody already made, and the answer
// is to read the row rather than to run this again.
func (epochs Epochs) apply(ctx context.Context, epoch executioncontrol.ActivationEpoch,
	facet Facet, column, target, statement string, arguments []any) error {
	result, err := epochs.DB.ExecContext(ctx, statement, arguments...)
	if err != nil {
		return fmt.Errorf("%w: moving epoch %d's %s facet to %s: %v",
			output.ErrInfrastructure, epoch, facet, target, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%w: moving epoch %d's %s facet to %s: %v",
			output.ErrInfrastructure, epoch, facet, target, err)
	}
	if affected == 1 {
		return nil
	}

	state, readErr := epochs.Read(ctx, epoch)
	if readErr != nil {
		return readErr
	}
	current := state.Base
	if facet == FacetOutput {
		current = state.Output
	}

	return fmt.Errorf("%w: epoch %d's %s facet is %q and this transition needs it elsewhere "+
		"to reach %q. A facet never moves backwards and `disabled` is terminal, so the way "+
		"back is a new epoch row rather than a rewrite of this one (base=%s output=%s "+
		"revision=%d)", ErrStaleEpoch, epoch, facet, current, target,
		state.Base, state.Output, state.Revision)
}

// Deferred: rotation is the only one of the five transitions with no operator
// path: the activation command has four modes and the chart's activation Job
// renders those four, so neither a receipt-key rotation nor an epoch handover
// can be asked for. Wiring it is a fifth mode, a second epoch flag and the Job
// name that carries both, and it belongs with the first rotation rather than
// ahead of the first activation: this plane ships dormant, and an epoch nobody
// has enabled has nothing to rotate off.
//
// Rotate moves a facet from one epoch to the next in ONE transaction.
//
// Two statements and one transaction, and the transaction is the whole point.
// Decision F1 asks for a rotation with "no window in which the facet has no
// `enabled` row", and the partial unique index makes the obvious reading of that
// -- enable the incoming row while the outgoing one is still enabled --
// impossible: there is at most one `enabled` row per facet, globally. What IS
// possible, and is what the decision is actually after, is that no other session
// ever OBSERVES a moment with none. Inside one transaction the outgoing row
// moves to `draining` and the incoming one to `enabled`; the unique index is
// checked at statement end, so the intermediate state is legal, and every
// concurrent reader sees either the state before both or the state after both.
//
// What coexists afterwards is a `draining` row and an `enabled` row, which is
// the coexistence F1 names: the outgoing epoch keeps settling everything already
// in flight -- releases, terminal dispositions, an admitted conditional delete
// finishing -- while the incoming one admits the new work.
func (epochs Epochs) Rotate(ctx context.Context, outgoing, incoming executioncontrol.ActivationEpoch,
	facet Facet) error {
	column, err := facet.Column()
	if err != nil {
		return err
	}
	if outgoing == incoming {
		return fmt.Errorf("%w: rotating epoch %d onto itself. Rotation creates a NEW epoch "+
			"rather than replacing a key in place", output.ErrIncomplete, outgoing)
	}

	// The base facet cannot be rotated off a row whose output facet is in
	// service, and the refusal is here rather than as a constraint violation
	// three lines down, because the constraint's message does not say why.
	//
	// hangar_output_epoch_needs_base forbids base_state leaving
	// ('attested','enabled') unless output_state is 'initial' or 'disabled',
	// and output_state is monotone, so reaching 'disabled' means passing the
	// drain predicate -- which is not empty on a plane with live generations.
	// So on a running plane the base facet's epoch is pinned until the output
	// facet has settled. That is a real property of the schema as landed, and
	// it is the one place decision F1's rotation shape does not reach.
	if facet == FacetBase {
		state, err := epochs.Read(ctx, outgoing)
		if err != nil {
			return err
		}
		if state.Output != "initial" && state.Output != "disabled" {
			return fmt.Errorf("%w: epoch %d's base facet cannot be rotated while its output "+
				"facet is %q. hangar_output_epoch_needs_base holds base_state in "+
				"('attested','enabled') for as long as output_state is anything but 'initial' "+
				"or 'disabled', and output_state never moves backwards -- so the base facet "+
				"stays with this epoch until the output facet has drained to 'disabled', "+
				"which needs every capture, claim, lease and reclaim job under it settled. "+
				"Rotate the OUTPUT facet, which is what a receipt-key rotation is",
				output.ErrConflict, outgoing, state.Output)
		}
	}

	tx, err := epochs.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%w: beginning the rotation: %v", output.ErrInfrastructure, err)
	}
	defer func() { _ = tx.Rollback() }()

	drain := fmt.Sprintf(`
		UPDATE hangar_output_activation_epochs
		   SET %s = 'draining', revision = revision + 1, updated_at = now()
		 WHERE epoch_id = $1 AND %s = 'enabled'`, column, column)
	if err := exactlyOne(ctx, tx, drain, int64(outgoing)); err != nil {
		return fmt.Errorf("%w: epoch %d's %s facet is not enabled, so there is nothing to "+
			"rotate off it: %v", ErrStaleEpoch, outgoing, facet, err)
	}

	enable := fmt.Sprintf(`
		UPDATE hangar_output_activation_epochs
		   SET %s = 'enabled', revision = revision + 1, updated_at = now()
		 WHERE epoch_id = $1 AND %s = 'attested'`, column, column)
	if facet == FacetOutput {
		// The SCHEMA's own readiness predicate, not a stricter one. It admits
		// `attested` as well as `enabled`, and that is what makes an output
		// rotation reachable at all: the incoming row's base facet cannot be
		// enabled while the outgoing row's still is, so requiring `enabled`
		// here would make every output rotation depend on a base rotation the
		// same constraints forbid.
		enable += ` AND base_state IN ('attested', 'enabled')`
	}
	if err := exactlyOne(ctx, tx, enable, int64(incoming)); err != nil {
		return fmt.Errorf("%w: epoch %d's %s facet is not attested, so it cannot take over: "+
			"%v. Attest the incoming epoch BEFORE rotating; a rotation that had to attest "+
			"mid-transaction would hold the outgoing facet out of service for the length of a "+
			"cohort handshake", ErrStaleEpoch, incoming, facet, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%w: committing the rotation: %v", output.ErrInfrastructure, err)
	}

	return nil
}

func exactlyOne(ctx context.Context, tx *sql.Tx, statement string, arguments ...any) error {
	result, err := tx.ExecContext(ctx, statement, arguments...)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("the row was not in the state this transition needs")
	}

	return nil
}
