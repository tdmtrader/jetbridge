package hangaroutput_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/concourse/concourse/atc/hangaroutput/activation"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// The activation epoch row, against real PostgreSQL.
//
// Real, because the whole subject is the SCHEMA: two partial unique indexes, a
// transition trigger that refuses a backwards move and a revision that must
// advance, and a CHECK that says output can never be in service while base is
// not. A fake repository here would be a second answer to the question the
// database is the only authority for.

// activationFixture is a migrated database with no epoch row in it, and a raw
// *sql.DB rather than the ATC's pooled connection.
//
// Raw because the activation command holds one: it runs as a one-shot Job under
// its own least-privilege PostgreSQL role, and giving the test the ATC's handle
// would drive the CAS through a path production does not have.
//
// No row to start with, deliberately. No migration inserts one -- a migration
// that created an enabled epoch would be a migration that turned the feature
// on, and the epoch attests migrations, keys, workers, bucket, policy,
// namespaces and principals, none of which a DDL script can observe.
func activationFixture(t *testing.T) (activation.Epochs, *sql.DB) {
	t.Helper()

	postmaster.CreateTestDBFromTemplate()
	conn := postmaster.OpenDB()
	t.Cleanup(func() {
		_ = conn.Close()
		postmaster.DropTestDB()
	})

	var existing int
	if err := conn.QueryRow(`SELECT count(*) FROM hangar_output_activation_epochs`).
		Scan(&existing); err != nil {
		t.Fatalf("counting activation epochs: %v", err)
	}
	if existing != 0 {
		t.Fatalf("a freshly migrated database already has %d activation epoch row(s); "+
			"nothing may be admitted until an operator runs the activation command, and a "+
			"migration that inserted one would be a migration that turned the feature on",
			existing)
	}

	return activation.Epochs{DB: conn}, conn
}

func baseEvidence() activation.Evidence {
	return activation.Evidence{
		Attestation:     json.RawMessage(`{"members":[{"node":"a"}]}`),
		ProtocolVersion: executioncontrol.ProtocolVersion,
		LedgerVersion:   executioncontrol.LedgerVersion,
		CohortDigest:    "digest-base",
	}
}

func outputEvidence() activation.Evidence {
	now := time.Now().UTC()

	return activation.Evidence{
		Attestation:          json.RawMessage(`{"members":[{"node":"a"}]}`),
		CohortDigest:         "digest-output",
		ReceiptPublicKeyID:   "receipt-1",
		MaterializationKeyID: "materialize-1",
		BucketFingerprint:    "gs://activation-output",
		DerivedNamespace:     "deployments/blue/one",
		ReceiptKeyValidFrom:  now.Add(-time.Hour),
		ReceiptKeyValidUntil: now.Add(90 * 24 * time.Hour),
	}
}

func TestTheBaseFacetIsAttestedAndEnabledIndependentlyOfOutput(t *testing.T) {
	epochs, _ := activationFixture(t)
	ctx := context.Background()

	const epoch = executioncontrol.ActivationEpoch(11)
	if err := epochs.Begin(ctx, epoch); err != nil {
		t.Fatalf("begin: %v", err)
	}

	state, err := epochs.Read(ctx, epoch)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if state.Base != "initial" || state.Output != "initial" {
		t.Fatalf("a new epoch is base=%s output=%s; both facets start out of service",
			state.Base, state.Output)
	}

	// Output cannot be enabled here, and the reason is not "not attested" -- it
	// is that base is not in service. Both refusals are typed and neither is a
	// silent no-op.
	if err := epochs.Enable(ctx, epoch, activation.FacetOutput); !errors.Is(err, activation.ErrStaleEpoch) {
		t.Errorf("output was enabled on a base-less epoch: %v", err)
	}

	if err := epochs.Attest(ctx, epoch, activation.FacetBase, baseEvidence()); err != nil {
		t.Fatalf("attesting base: %v", err)
	}
	if err := epochs.Enable(ctx, epoch, activation.FacetBase); err != nil {
		t.Fatalf("enabling base: %v", err)
	}

	state, err = epochs.Read(ctx, epoch)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if state.Base != "enabled" {
		t.Errorf("base is %q after enable", state.Base)
	}
	if state.Output != "initial" {
		t.Errorf("output is %q; enabling base is not enabling output, and a base-only cohort "+
			"is a real deployment", state.Output)
	}
	if state.Revision < 3 {
		t.Errorf("the revision is %d after begin, attest and enable; every write advances it, "+
			"which is what makes the CAS predicate mean anything", state.Revision)
	}
}

func TestOutputCannotBeEnabledWithoutBaseAndCannotSkipAttestation(t *testing.T) {
	epochs, _ := activationFixture(t)
	ctx := context.Background()

	const epoch = executioncontrol.ActivationEpoch(12)
	mustBegin(t, epochs, epoch)

	// Attesting output before base is refused by the schema's CHECK, reached
	// through the CAS's own base_state predicate.
	if err := epochs.Attest(ctx, epoch, activation.FacetOutput, outputEvidence()); err == nil {
		t.Error("output was attested on an epoch whose base facet is still initial")
	}

	mustAttestAndEnableBase(t, epochs, epoch)

	// Enabling output without attesting it is refused.
	if err := epochs.Enable(ctx, epoch, activation.FacetOutput); !errors.Is(err, activation.ErrStaleEpoch) {
		t.Errorf("output was enabled without an attestation: %v", err)
	}

	if err := epochs.Attest(ctx, epoch, activation.FacetOutput, outputEvidence()); err != nil {
		t.Fatalf("attesting output: %v", err)
	}
	if err := epochs.Enable(ctx, epoch, activation.FacetOutput); err != nil {
		t.Fatalf("enabling output: %v", err)
	}
}

// Decision F1's rotation, end to end, as the plan's authoritative sequence
// specifies it.
//
// The source is plan.md's "Rotation overlaps; it does not gap": `begin` creates
// the next epoch row in `initial`, `attest` moves it to `attested`, and then, in
// the SAME TRANSACTION, the outgoing row is moved `enabled -> draining` first
// and the incoming row `attested -> enabled` second -- because the per-facet
// `enabled` partial unique index is checked per statement and cannot be
// deferred, so the order inside the transaction matters. That is what
// Epochs.Rotate implements, line for line.
//
// The first assertion below is that the OTHER order is refused: enabling the
// incoming row while the outgoing one is still `enabled` violates
// `hangar_output_one_enabled_base_epoch`, which admits one `enabled` row per
// facet globally. It is asserted because a rotation written that way must fail
// loudly rather than silently leave two cohorts claiming one bucket -- not
// because the plan and the schema disagree. They do not; three summaries of the
// plan did, and were corrected on 2026-09-11 (Phase 8 review R1-F5): decision
// F1's paragraph, this migration's index comment, and the Phase 8 activation
// box. The checkpointed schema was never in question.
//
// Inside one transaction the intermediate state is legal, no other session ever
// OBSERVES a moment with no enabled row -- which is what "no window" means here
// -- and afterwards a `draining` row and an `enabled` row coexist, which is the
// coexistence F1 names: the outgoing epoch keeps settling what is already in
// flight while the incoming one admits new work.
func TestRotationOverlapsAndNoObserverSeesTheFacetWithoutAnEnabledRow(t *testing.T) {
	epochs, conn := activationFixture(t)
	ctx := context.Background()

	const outgoing = executioncontrol.ActivationEpoch(20)
	const incoming = executioncontrol.ActivationEpoch(21)

	mustBegin(t, epochs, outgoing)
	mustAttestAndEnableBase(t, epochs, outgoing)
	mustAttestAndEnableOutput(t, epochs, outgoing)

	enabledRows := func(facet string) int {
		t.Helper()
		var count int
		if err := conn.QueryRow(`SELECT count(*) FROM hangar_output_activation_epochs
			 WHERE ` + facet + `_state = 'enabled'`).Scan(&count); err != nil {
			t.Fatalf("counting enabled %s rows: %v", facet, err)
		}

		return count
	}

	// A second row in `initial` coexists with the enabled one. The index says
	// nothing about any state but `enabled`, which is what lets the next epoch
	// be begun and attested while the current one is still serving.
	mustBegin(t, epochs, incoming)
	mustAttestBase(t, epochs, incoming)
	mustAttestOutputOnly(t, epochs, incoming)
	for _, facet := range []string{"base", "output"} {
		if got := enabledRows(facet); got != 1 {
			t.Fatalf("%d enabled %s rows while the next epoch is attested", got, facet)
		}
	}

	// F1's literal sequence, and the refusal.
	if err := epochs.Enable(ctx, incoming, activation.FacetBase); err == nil {
		t.Fatal("two epochs were enabled for the base facet at once. The partial unique index " +
			"is what makes \"exactly one enabled row per facet\" true; two enabled cohorts on " +
			"one bucket is two planes disagreeing about whose object is whose")
	}

	// The BASE facet cannot be rotated at all while the output facet is in
	// service, and that is a property of the schema rather than of this code:
	// hangar_output_epoch_needs_base pins base_state in ('attested','enabled')
	// for as long as output_state is not 'initial' or 'disabled', and
	// output_state never moves backwards. The refusal says so.
	err := epochs.Rotate(ctx, outgoing, incoming, activation.FacetBase)
	if !errors.Is(err, output.ErrConflict) {
		t.Errorf("the base facet was rotated off a row whose output facet is enabled: %v", err)
	}
	if err != nil && !strings.Contains(err.Error(), "hangar_output_epoch_needs_base") {
		t.Errorf("the refusal does not name the constraint that causes it: %v", err)
	}

	// The OUTPUT facet -- which is the one a receipt-key rotation is about, and
	// the one F1's retained-old-private-key rule names -- rotates in one
	// transaction.
	if err := epochs.Rotate(ctx, outgoing, incoming, activation.FacetOutput); err != nil {
		t.Fatalf("rotating the output facet: %v", err)
	}

	var states [4]string
	if err := conn.QueryRow(`SELECT
			(SELECT base_state FROM hangar_output_activation_epochs WHERE epoch_id = $1),
			(SELECT output_state FROM hangar_output_activation_epochs WHERE epoch_id = $1),
			(SELECT base_state FROM hangar_output_activation_epochs WHERE epoch_id = $2),
			(SELECT output_state FROM hangar_output_activation_epochs WHERE epoch_id = $2)`,
		int64(outgoing), int64(incoming)).
		Scan(&states[0], &states[1], &states[2], &states[3]); err != nil {
		t.Fatalf("reading both rows: %v", err)
	}
	if states[0] != "enabled" || states[1] != "draining" {
		t.Errorf("the outgoing row is base=%s output=%s; its output facet drains and its base "+
			"facet stays in service, because the schema pins it there until output is disabled",
			states[0], states[1])
	}
	if states[2] != "attested" || states[3] != "enabled" {
		t.Errorf("the incoming row is base=%s output=%s", states[2], states[3])
	}
	if got := enabledRows("output"); got != 1 {
		t.Errorf("%d enabled output rows after rotation", got)
	}
}

// The transaction is load-bearing, and this is what shows it.
//
// A reader polling as fast as it can while the rotation runs must never observe
// the facet with zero enabled rows. Without the transaction the drain and the
// enable are two separately visible statements, and between them there is
// exactly such a moment.
//
// The base facet is the subject here rather than output, because a base-only
// epoch CAN be rotated -- hangar_output_epoch_needs_base only pins base while
// output is in service, and this epoch's output facet is still `initial`.
func TestNoConcurrentReaderEverSeesTheFacetWithZeroEnabledRows(t *testing.T) {
	epochs, conn := activationFixture(t)
	ctx := context.Background()

	const outgoing = executioncontrol.ActivationEpoch(60)
	const incoming = executioncontrol.ActivationEpoch(61)

	mustBegin(t, epochs, outgoing)
	mustAttestAndEnableBase(t, epochs, outgoing)
	mustBegin(t, epochs, incoming)
	mustAttestBase(t, epochs, incoming)

	var readings atomic.Int64
	var zeroes atomic.Int64
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			var count int
			if err := conn.QueryRow(`SELECT count(*) FROM hangar_output_activation_epochs
				 WHERE base_state = 'enabled'`).Scan(&count); err != nil {
				return
			}
			readings.Add(1)
			if count != 1 {
				zeroes.Add(1)
			}
		}
	}()

	// Let the observer get going before the rotation, so the two really do
	// overlap rather than the rotation finishing first.
	for readings.Load() < 50 {
		runtime.Gosched()
	}
	before := readings.Load()

	if err := epochs.Rotate(ctx, outgoing, incoming, activation.FacetBase); err != nil {
		t.Fatalf("rotating: %v", err)
	}
	for readings.Load() < before+50 {
		runtime.Gosched()
	}
	close(stop)
	<-done

	if zeroes.Load() != 0 {
		t.Errorf("a concurrent reader observed the base facet with no enabled row %d times "+
			"out of %d readings. The drain and the enable are ONE transaction precisely so "+
			"that no session sees that moment", zeroes.Load(), readings.Load())
	}
	if readings.Load() < 100 {
		t.Fatalf("the observer took only %d readings; it did not overlap the rotation and "+
			"this rule would pass vacuously", readings.Load())
	}
}

// A facet never moves backwards, and `disabled` is terminal. Re-attesting an
// enabled epoch in place would let a cohort change under a running plane without
// the captures that recorded the old epoch noticing.
func TestAFacetNeverMovesBackwards(t *testing.T) {
	epochs, _ := activationFixture(t)
	ctx := context.Background()

	const epoch = executioncontrol.ActivationEpoch(30)
	mustBegin(t, epochs, epoch)
	mustAttestAndEnableBase(t, epochs, epoch)

	if err := epochs.Attest(ctx, epoch, activation.FacetBase, baseEvidence()); !errors.Is(err, activation.ErrStaleEpoch) {
		t.Errorf("an enabled base facet was re-attested in place: %v", err)
	}

	if err := epochs.Drain(ctx, epoch, activation.FacetBase); err != nil {
		t.Fatalf("draining: %v", err)
	}
	if err := epochs.Disable(ctx, epoch, activation.FacetBase); err != nil {
		t.Fatalf("disabling: %v", err)
	}
	for _, forbidden := range []struct {
		name string
		call func() error
	}{
		{"enable", func() error { return epochs.Enable(ctx, epoch, activation.FacetBase) }},
		{"drain", func() error { return epochs.Drain(ctx, epoch, activation.FacetBase) }},
		{"attest", func() error {
			return epochs.Attest(ctx, epoch, activation.FacetBase, baseEvidence())
		}},
	} {
		if err := forbidden.call(); !errors.Is(err, activation.ErrStaleEpoch) {
			t.Errorf("a disabled base facet accepted %s: %v", forbidden.name, err)
		}
	}
}

// begin is not idempotent, and says so.
//
// A silent no-op would be the worst of both: an operator reads success and the
// row they think they created is somebody else's, in whatever state it reached.
func TestBeginningAnExistingEpochIsATypedConflict(t *testing.T) {
	epochs, _ := activationFixture(t)
	ctx := context.Background()

	const epoch = executioncontrol.ActivationEpoch(40)
	mustBegin(t, epochs, epoch)

	err := epochs.Begin(ctx, epoch)
	if !errors.Is(err, output.ErrConflict) {
		t.Errorf("beginning an existing epoch answered %v; it is a typed conflict", err)
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("the refusal does not say what happened: %v", err)
	}
}

// An attestation with no evidence bundle cannot justify anything, and the row
// records the evidence in the same statement that moves the state precisely so
// that an enabled epoch cannot exist without it.
func TestAnAttestationWithoutEvidenceIsRefused(t *testing.T) {
	epochs, _ := activationFixture(t)
	ctx := context.Background()

	const epoch = executioncontrol.ActivationEpoch(50)
	mustBegin(t, epochs, epoch)

	empty := baseEvidence()
	empty.Attestation = nil
	if err := epochs.Attest(ctx, epoch, activation.FacetBase, empty); !errors.Is(err, output.ErrIncomplete) {
		t.Errorf("an attestation with no evidence was accepted: %v", err)
	}

	noDigest := baseEvidence()
	noDigest.CohortDigest = ""
	if err := epochs.Attest(ctx, epoch, activation.FacetBase, noDigest); !errors.Is(err, output.ErrIncomplete) {
		t.Errorf("a base attestation with no cohort digest was accepted: %v", err)
	}

	mustAttestAndEnableBase(t, epochs, epoch)

	sameKey := outputEvidence()
	sameKey.MaterializationKeyID = sameKey.ReceiptPublicKeyID
	if err := epochs.Attest(ctx, epoch, activation.FacetOutput, sameKey); !errors.Is(err, output.ErrIncomplete) {
		t.Errorf("one key id for receipts and read grants was accepted: %v", err)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func mustBegin(t *testing.T, epochs activation.Epochs, epoch executioncontrol.ActivationEpoch) {
	t.Helper()

	if err := epochs.Begin(context.Background(), epoch); err != nil {
		t.Fatalf("begin %d: %v", epoch, err)
	}
}

func mustAttestBase(t *testing.T, epochs activation.Epochs, epoch executioncontrol.ActivationEpoch) {
	t.Helper()

	if err := epochs.Attest(context.Background(), epoch, activation.FacetBase, baseEvidence()); err != nil {
		t.Fatalf("attest base %d: %v", epoch, err)
	}
}

func mustAttestAndEnableBase(t *testing.T, epochs activation.Epochs,
	epoch executioncontrol.ActivationEpoch) {
	t.Helper()

	mustAttestBase(t, epochs, epoch)
	if err := epochs.Enable(context.Background(), epoch, activation.FacetBase); err != nil {
		t.Fatalf("enable base %d: %v", epoch, err)
	}
}

func mustAttestOutputOnly(t *testing.T, epochs activation.Epochs,
	epoch executioncontrol.ActivationEpoch) {
	t.Helper()

	if err := epochs.Attest(context.Background(), epoch, activation.FacetOutput, outputEvidence()); err != nil {
		t.Fatalf("attest output %d: %v", epoch, err)
	}
}

func mustAttestAndEnableOutput(t *testing.T, epochs activation.Epochs,
	epoch executioncontrol.ActivationEpoch) {
	t.Helper()

	mustAttestOutputOnly(t, epochs, epoch)
	if err := epochs.Enable(context.Background(), epoch, activation.FacetOutput); err != nil {
		t.Fatalf("enable output %d: %v", epoch, err)
	}
}

// AFTER ONE OUTPUT ROTATION, ENABLE MUST STILL BE REACHABLE.
//
// Enable required `base_state = 'enabled'` for the output facet, and Rotate
// deliberately admitted `base_state IN ('attested','enabled')` -- the schema's
// own predicate -- because the incoming row's base facet cannot be enabled
// while the outgoing row's still is. The two disagreed, and the disagreement is
// a one-way door: after the first output rotation the serving row's base sits
// at `attested` permanently, so the only way to move output again was another
// rotation. Take the output facet out of service entirely and there is nothing
// left to rotate FROM, and nothing whose base is `enabled` to rotate TO. The
// plane's output half cannot be turned back on at all.
//
// The sequence below is that door, walked. It is the operator's own vocabulary
// throughout: no row is written by hand.
func TestOutputCanBeEnabledAgainAfterARotationAndAFullDrain(t *testing.T) {
	epochs, conn := activationFixture(t)
	ctx := context.Background()

	const (
		first  = executioncontrol.ActivationEpoch(61)
		second = executioncontrol.ActivationEpoch(62)
		third  = executioncontrol.ActivationEpoch(63)
	)

	mustBegin(t, epochs, first)
	mustAttestAndEnableBase(t, epochs, first)
	mustAttestAndEnableOutput(t, epochs, first)

	// One output rotation, which is the ordinary receipt-key rotation.
	mustBegin(t, epochs, second)
	mustAttestBase(t, epochs, second)
	mustAttestOutputOnly(t, epochs, second)
	if err := epochs.Rotate(ctx, first, second, activation.FacetOutput); err != nil {
		t.Fatalf("rotating output: %v", err)
	}
	if err := epochs.Disable(ctx, first, activation.FacetOutput); err != nil {
		t.Fatalf("disabling the outgoing output facet: %v", err)
	}

	state, err := epochs.Read(ctx, second)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if state.Base != "attested" {
		t.Fatalf("the serving row's base facet is %q; this test is about the state a rotation "+
			"leaves behind, and that state is base=attested", state.Base)
	}

	// The output facet goes out of service entirely -- a deployment turning the
	// output plane off, which every drain path is written for.
	if err := epochs.Drain(ctx, second, activation.FacetOutput); err != nil {
		t.Fatalf("draining the serving output facet: %v", err)
	}
	if err := epochs.Disable(ctx, second, activation.FacetOutput); err != nil {
		t.Fatalf("disabling the serving output facet: %v", err)
	}

	var enabled int
	if err := conn.QueryRow(`SELECT count(*) FROM hangar_output_activation_epochs
		 WHERE output_state = 'enabled'`).Scan(&enabled); err != nil {
		t.Fatalf("counting enabled output rows: %v", err)
	}
	if enabled != 0 {
		t.Fatalf("%d output facets are still enabled, so this is not the state the test is "+
			"named for", enabled)
	}

	// And now output is turned back on. Rotate cannot do it -- there is no
	// enabled output facet to rotate off -- so Enable has to, and the row it
	// has to do it on is one whose base facet is `attested`, because the only
	// `enabled` base facet belongs to a row whose output facet is terminally
	// disabled.
	mustBegin(t, epochs, third)
	mustAttestBase(t, epochs, third)
	mustAttestOutputOnly(t, epochs, third)

	if err := epochs.Rotate(ctx, second, third, activation.FacetOutput); err == nil {
		t.Fatal("a rotation succeeded off a disabled output facet, so the dead end this test " +
			"is about is not reachable and the test proves nothing")
	}
	if err := epochs.Enable(ctx, third, activation.FacetOutput); err != nil {
		t.Fatalf("the output facet cannot be brought back into service at all: %v.\n\n"+
			"Enable and Rotate disagree about base readiness. Rotate takes the schema's own "+
			"predicate -- base_state IN ('attested','enabled') -- and Enable takes a stricter "+
			"one, so after the first output rotation Enable is unreachable forever: the "+
			"serving row's base stays at `attested` and the only `enabled` base belongs to a "+
			"row whose output facet is terminally disabled", err)
	}

	state, err = epochs.Read(ctx, third)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if state.Base != "attested" || state.Output != "enabled" {
		t.Errorf("the re-enabled row is base=%s output=%s", state.Base, state.Output)
	}
}
