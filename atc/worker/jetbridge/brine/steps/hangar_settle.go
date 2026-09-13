package steps

// `the capture settles`: the control plane's half, driven to quiescence.
//
// Everything under it is production. The repository is atc/db's, over the
// scenario's own real PostgreSQL. The node half is jetbridge's own
// OutputControlClient against the real output daemon this fixture started. The
// coordinator is atc/hangaroutput's, and it takes exactly the transitions it
// would take in a deployment -- the fixture chooses none of them and asserts on
// none of the intermediate calls.
//
// WHY IT RUNS TO QUIESCENCE. A settlement is a state, not a step: what a
// scenario means by "the capture settles" is that nothing is owed any more. The
// coordinator advances one bounded transition per call, so this loops until it
// says `none`, and it records the durable record AFTER EACH ONE. That is what
// lets "the producer checkpoint is committed with an unresolved reservation"
// and "the outcome was pending between the two halves" be assertions about
// ORDERING rather than about a final state that no longer shows either.
//
// THE LOOP IS BOUNDED, and the bound is an assertion. A coordinator that made
// no progress would spin, and a scenario that spun would be reported by
// timecheck.sh as a hang rather than as a failure.

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/google/uuid"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/hangar/executioncontrol"
	hangaroutputleaf "github.com/concourse/concourse/hangar/output"
)

// settlementPlane is the control plane a scenario's settle runs on: this
// scenario's database, this scenario's daemon, one coordinator over both.
type settlementPlane struct {
	DB          JetbridgeDB
	Repository  *db.HangarOutputRepository
	Coordinator *hangaroutput.Coordinator
	Recoverer   *hangaroutput.Recoverer
}

// brineTransactor adapts the scenario's connection to the coordinator's port.
type brineTransactor struct{ conn db.DbConn }

func (transactor brineTransactor) Begin() (hangaroutput.Transaction, error) {
	return transactor.conn.Begin()
}

// oneDaemonDialer is the whole cluster a brine scenario has: one node, one
// daemon. The locator is still opaque and still handed back unchanged, because
// the coordinator must not be able to tell that there is only one.
type oneDaemonDialer struct{ control hangaroutput.SourceControl }

func (dialer oneDaemonDialer) ForLocator(string) (hangaroutput.SourceControl, error) {
	return dialer.control, nil
}

// brineDrain stands for the deployment's writer termination and Kubernetes
// proof.
//
// There is no cluster in this fixture, so what it proves is the only thing it
// honestly can: that the captured drain set is empty, which is true because no
// scenario here admits a writer. A drain set with anything in it is refused
// rather than waved through, so a seal that captured a writer becomes
// seal_unconfirmed instead of publishing over a source somebody may still be
// writing. The real proof is a K3s flow under a build tag.
type brineDrain struct{}

func (brineDrain) ConfirmDrain(_ context.Context, _ string,
	started hangaroutputleaf.SealStarted) ([]hangaroutputleaf.DrainedWriter, error) {
	if len(started.DrainSet) != 0 {
		return nil, fmt.Errorf("%w: this fixture admitted no writer and the seal captured %d; "+
			"terminating a pod's writers needs a cluster", hangaroutputleaf.ErrSealUnconfirmed,
			len(started.DrainSet))
	}

	return nil, nil
}

// newSettlementPlane wires the coordinator over a scenario's database and
// daemon, and opens the activation epoch the daemon is already attested for.
func newSettlementPlane(source HeldSource, res brine.Resources) (settlementPlane, error) {
	jdb, err := jetbridgeDBFrom(res)
	if err != nil {
		return settlementPlane{}, err
	}

	prefix, err := db.HangarConsumerPrefixHeld("brine-capture")
	if err != nil {
		return settlementPlane{}, err
	}
	repository := db.NewHangarOutputRepository(prefix)

	if err := openActivationEpoch(jdb); err != nil {
		return settlementPlane{}, err
	}

	client := jetbridgeOutputClient(source)
	transactor := brineTransactor{conn: jdb.Conn}

	verifier, err := hangaroutputleaf.NewReceiptSignatureVerifier(
		hangarReceiptRing(source.Draft.Daemon), hangaroutputleaf.ClockFunc(func() time.Time {
			return time.Now().UTC()
		}))
	if err != nil {
		return settlementPlane{}, err
	}

	coordinator := &hangaroutput.Coordinator{
		Transactor:   transactor,
		Repository:   repository,
		Dialer:       oneDaemonDialer{control: client},
		Drain:        brineDrain{},
		Verifier:     verifier,
		Announcer:    hangaroutput.AnnouncerFunc(repository.RecordAnnouncement),
		OwnerID:      uuid.NewString(),
		ReceiptKeyID: hangarReceiptKeyID,
	}

	return settlementPlane{
		DB:          jdb,
		Repository:  repository,
		Coordinator: coordinator,
		Recoverer: &hangaroutput.Recoverer{
			Coordinator: coordinator,
			Incomplete:  repository,
			Transactor:  transactor,
		},
	}, nil
}

// jetbridgeOutputClient is PRODUCTION's client against this fixture's daemon.
//
// Not a second HTTP client written here: the capability minting, the facet
// scoping, the per-call nonce and the wire encoding are the ones a deployment
// uses, and a fixture that reimplemented them would be asserting its own
// encoding rather than the ATC's.
func jetbridgeOutputClient(source HeldSource) hangaroutput.SourceControl {
	return jetbridgeClientFor(source.Draft.Daemon)
}

// admitControlPlane records everything the control plane commits before a
// producer's outcome exists: the predeclaration, the daemon's reservation, and
// the hold the control init established.
//
// It runs HERE, at the settle, rather than where the daemon chain established
// them, and the reason is the chain's shape: `the daemon holds the source` is
// shared with the daemon-handoff family, which has no database in it at all.
// The ORDER is production's -- predeclare, then reserve, then hold -- and the
// schema enforces it: a hold over an unreserved source is refused by a CHECK.
func admitControlPlane(plane settlementPlane, source HeldSource) error {
	ctx := context.Background()

	tx, err := plane.DB.Conn.Begin()
	if err != nil {
		return err
	}
	defer db.Rollback(tx)

	if err := plane.Repository.PredeclareHandoff(ctx, tx, source.Admission); err != nil {
		return fmt.Errorf("predeclaring the handoff: %w", err)
	}
	if err := plane.Repository.RecordSourceReservation(ctx, tx, source.Reserved,
		"brine-node"); err != nil {
		return fmt.Errorf("recording the reservation: %w", err)
	}
	if source.Acknowledgement.Signature != "" {
		if err := plane.Repository.AcknowledgeSourceHold(ctx, tx,
			source.Acknowledgement); err != nil {
			return fmt.Errorf("acknowledging the hold: %w", err)
		}
	}

	return tx.Commit()
}

// settle drives the coordinator to quiescence, recording the durable record
// after every transition.
func settle(in FinishWitnessed, res brine.Resources) (CaptureOutcome, error) {
	outcome := CaptureOutcome{Source: in.Source}

	plane, err := newSettlementPlane(in.Source, res)
	if err != nil {
		return outcome, err
	}
	if err := admitControlPlane(plane, in.Source); err != nil {
		return outcome, err
	}

	ctx := context.Background()
	handoff := in.Source.Admission.HandoffID

	// A cancellation is a REQUEST and not a row, so it enters through the
	// product-neutral seam rather than by setting something. Which branch it
	// then wins is the arbiter's, and this does not decide it.
	if in.Cancelled {
		decision, err := plane.Coordinator.Cancel(ctx, handoff)
		outcome.Transitions = append(outcome.Transitions, string(decision.Transition))
		if err != nil {
			outcome.Err = err

			return recordSettlement(plane, handoff, outcome)
		}
		outcome.Snapshots = append(outcome.Snapshots, mustRead(plane, handoff))
	}

	for i := 0; i < 12; i++ {
		decision, err := plane.Coordinator.Advance(ctx, handoff)
		outcome.Transitions = append(outcome.Transitions, string(decision.Transition))
		if err != nil {
			outcome.Err = err

			break
		}
		outcome.Snapshots = append(outcome.Snapshots, mustRead(plane, handoff))
		if decision.Transition == hangaroutput.TransitionNone ||
			decision.Transition == hangaroutput.TransitionAwaitOutcome {
			break
		}
		if i == 11 {
			return outcome, fmt.Errorf("the coordinator took 12 transitions without settling: %v",
				outcome.Transitions)
		}
	}

	return recordSettlement(plane, handoff, outcome)
}

// recordSettlement reads back what production wrote: the final record and the
// announcements, both through production readers.
func recordSettlement(plane settlementPlane, handoff hangaroutputleaf.HandoffID,
	outcome CaptureOutcome) (CaptureOutcome, error) {
	ctx := context.Background()

	tx, err := plane.DB.Conn.Begin()
	if err != nil {
		return outcome, err
	}
	defer db.Rollback(tx)

	record, err := plane.Repository.LoadHandoffRecord(ctx, tx, handoff)
	if err != nil {
		return outcome, err
	}
	outcome.Final = record
	outcome.Settled = record.Settled
	if record.Disposition != nil {
		outcome.Disposition = *record.Disposition
	}
	outcome.Reason = terminalReasonOf(record)
	if record.Receipt != nil {
		outcome.Receipt = *record.Receipt
	}
	if record.Ref.Validate() == nil {
		outcome.Published = hangaroutputleaf.PublicationResult{
			ProtocolVersion: hangaroutputleaf.ProtocolVersion,
			Ref:             record.Ref,
			MarkerVersion:   hangaroutputleaf.MarkerVersion,
			ReservationID:   record.ReservationID,
			Metageneration:  1,
		}
	}

	announcements, err := plane.Repository.ReadAnnouncements(ctx, tx, handoff)
	if err != nil {
		return outcome, err
	}
	outcome.Announcements = announcements
	outcome.Plane = &plane

	return outcome, nil
}

func mustRead(plane settlementPlane, handoff hangaroutputleaf.HandoffID) hangaroutputleaf.HandoffRecord {
	tx, err := plane.DB.Conn.Begin()
	if err != nil {
		return hangaroutputleaf.HandoffRecord{}
	}
	defer db.Rollback(tx)

	record, err := plane.Repository.LoadHandoffRecord(context.Background(), tx, handoff)
	if err != nil {
		return hangaroutputleaf.HandoffRecord{}
	}

	return record
}

// terminalReasonOf is the reason word a watcher is told, derived from the
// durable record rather than from anything the fixture remembers.
func terminalReasonOf(record hangaroutputleaf.HandoffRecord) hangaroutputleaf.NoCaptureReason {
	if record.Disposition == nil || *record.Disposition != hangaroutputleaf.DispositionNoCapture {
		return ""
	}
	if record.FinishUnresolvable != "" {
		return record.FinishUnresolvable
	}

	return hangaroutputleaf.NoCaptureAuthoritativeNonSuccess
}

// jetbridgeDBFrom pulls the scenario's database out of the resource plane.
func jetbridgeDBFrom(res brine.Resources) (JetbridgeDB, error) {
	jdb, ok := res.Get("jetbridge-db").(JetbridgeDB)
	if !ok {
		return JetbridgeDB{}, fmt.Errorf("brine: jetbridge-db is %T", res.Get("jetbridge-db"))
	}

	return jdb, nil
}

// openActivationEpoch puts the plane in the state a deployment is in after
// activation: one enabled epoch and one fresh, safe policy attestation.
//
// Without both, nothing admits anything -- which is the held state the
// migration deliberately leaves behind, and is why this is a fixture step
// rather than a default.
func openActivationEpoch(jdb JetbridgeDB) error {
	if _, err := jdb.Conn.Exec(`
		INSERT INTO hangar_output_activation_epochs
			(epoch_id, base_state, output_state, base_attestation, output_attestation,
			 receipt_public_key_id, receipt_key_valid_from, receipt_key_valid_until,
			 materialization_key_id, bucket_fingerprint, derived_namespace)
		VALUES ($1, 'enabled', 'enabled', '{}', '{}', $2,
			now() - interval '1 day', now() + interval '30 days',
			'brine-materialize-key-1', 'gs://brine-output', 'brine/one')
		ON CONFLICT (epoch_id) DO NOTHING`,
		int64(hangarEpoch), hangarReceiptKeyID); err != nil {
		return fmt.Errorf("opening the activation epoch: %w", err)
	}

	ctx := context.Background()
	prefix, err := db.HangarConsumerPrefixHeld("brine-capture")
	if err != nil {
		return err
	}

	tx, err := jdb.Conn.Begin()
	if err != nil {
		return err
	}
	defer db.Rollback(tx)

	if err := db.NewHangarOutputRepository(prefix).RecordPolicySnapshot(ctx, tx,
		hangaroutputleaf.PolicySnapshot{
			ProtocolVersion:      hangaroutputleaf.ProtocolVersion,
			ActivationEpoch:      executioncontrol.ActivationEpoch(hangarEpoch),
			BucketFingerprint:    "gs://brine-output",
			Metageneration:       3,
			PolicyHash:           "brine-policy-1",
			LifecycleDeleteRules: 0,
			State:                hangaroutputleaf.PolicySafe,
			ObservedAt:           hangaroutputleaf.NewTimestamp(time.Now()),
		}); err != nil {
		return fmt.Errorf("attesting the bucket policy: %w", err)
	}

	return tx.Commit()
}

// hangarReceiptRing pins the daemon's receipt key for the activation epoch, so
// a receipt is verified under a key the plane declares rather than under
// whatever signed it.
func hangarReceiptRing(daemon HangarDaemon) *hangaroutputleaf.ReceiptKeyRing {
	ring, err := hangaroutputleaf.NewReceiptKeyRing(hangaroutputleaf.EpochKey{
		KeyID:      hangarReceiptKeyID,
		Epoch:      executioncontrol.ActivationEpoch(hangarEpoch),
		PublicKey:  daemon.ReceiptPublic,
		ValidFrom:  hangaroutputleaf.NewTimestamp(time.Now().Add(-time.Hour)),
		ValidUntil: hangaroutputleaf.NewTimestamp(time.Now().Add(time.Hour)),
	})
	if err != nil {
		panic("brine: the fixture's receipt key ring is invalid: " + err.Error())
	}

	return ring
}

// jetbridgeClientFor is the production client bound to this fixture's output
// daemon, minting capabilities with the fixture's own minter.
func jetbridgeClientFor(daemon HangarDaemon) hangaroutput.SourceControl {
	return jetbridge.NewOutputControlClient(daemon.Output.URL,
		&http.Client{Timeout: 30 * time.Second}, daemon.Minter,
		executioncontrol.ActivationEpoch(hangarEpoch))
}

// snapshotAfter returns the durable record as it stood immediately after the
// named transition, and whether that transition happened at all.
//
// It is what makes two of this family's assertions about ORDERING rather than
// about a settled state that no longer shows either.
func (outcome CaptureOutcome) snapshotAfter(transition string) (hangaroutputleaf.HandoffRecord, bool) {
	for i, taken := range outcome.Transitions {
		if taken == transition && i < len(outcome.Snapshots) {
			return outcome.Snapshots[i], true
		}
	}

	return hangaroutputleaf.HandoffRecord{}, false
}

// allAnnouncements reads what the plane told watchers about every handoff.
func (plane settlementPlane) allAnnouncements() ([]db.HangarAnnouncement, error) {
	tx, err := plane.DB.Conn.Begin()
	if err != nil {
		return nil, err
	}
	defer db.Rollback(tx)

	return plane.Repository.ReadEveryAnnouncement(context.Background(), tx)
}

// sealFromPredeclaration offers a predeclaration where a Stage 2 reservation
// belongs.
//
// It goes through the coordinator's own guard rather than posting to the daemon
// directly, because the guard is the thing under test: a predeclaration has no
// reservation id to offer, and the refusal has to name that rather than saying
// "no reservation", which is true of half a dozen states.
func sealFromPredeclaration(draft CaptureDraft, res brine.Resources) (CaptureOutcome, error) {
	// The reservation, from the daemon, exactly as it happens before a Pod is
	// built. A predeclaration with no reservation would be refused by the
	// schema before the publication guard was ever reached, and this scenario
	// is about the guard.
	reserving := draft.Daemon.capture("reserve-incarnation",
		"/capture/v1/reserve-incarnation", draft.Admission.Execution, draft.Admission)
	reserved, err := decodeControl[hangaroutputleaf.ReservedIncarnation](reserving)
	if err != nil {
		return CaptureOutcome{}, fmt.Errorf("reserving the incarnation: %w", err)
	}
	draft.Reserved = reserved

	source := HeldSource{
		Draft:     HeldDraft{Handle: string(draft.Admission.HandoffID), Output: draft.Output, Daemon: draft.Daemon},
		Admission: draft.Admission,
		Execution: draft.Admission.Execution,
		Reserved:  draft.Reserved,
	}
	outcome := CaptureOutcome{Source: source}

	plane, planeErr := newSettlementPlane(source, res)
	if planeErr != nil {
		return outcome, planeErr
	}

	ctx := context.Background()
	tx, err := plane.DB.Conn.Begin()
	if err != nil {
		return outcome, err
	}
	defer db.Rollback(tx)

	if err := plane.Repository.PredeclareHandoff(ctx, tx, draft.Admission); err != nil {
		return outcome, fmt.Errorf("predeclaring the handoff: %w", err)
	}
	if err := plane.Repository.RecordSourceReservation(ctx, tx, draft.Reserved,
		"brine-node"); err != nil {
		return outcome, fmt.Errorf("recording the reservation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return outcome, err
	}

	// A predeclaration has no Stage 2, so the only transition available is to
	// wait for an outcome. The refusal is the publication guard's, asked
	// directly: the coordinator would never REACH a seal from here, and a
	// scenario that only observed "it did not seal" could not tell a guard
	// from a coordinator that had simply not got round to it.
	read, err := plane.Coordinator.Advance(ctx, draft.Admission.HandoffID)
	if err != nil {
		outcome.Err = err

		return outcome, nil
	}
	outcome.Transitions = append(outcome.Transitions, string(read.Transition))

	record, err := loadRecord(plane, draft.Admission.HandoffID)
	if err != nil {
		return outcome, err
	}
	outcome.Final = record
	outcome.Refusal = hangaroutput.PublicationAuthority(record, "begin_seal")

	return outcome, nil
}

func loadRecord(plane settlementPlane, handoff hangaroutputleaf.HandoffID) (hangaroutputleaf.HandoffRecord, error) {
	tx, err := plane.DB.Conn.Begin()
	if err != nil {
		return hangaroutputleaf.HandoffRecord{}, err
	}
	defer db.Rollback(tx)

	return plane.Repository.LoadHandoffRecord(context.Background(), tx, handoff)
}
