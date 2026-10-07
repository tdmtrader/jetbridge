package steps

// `the capture settles`: the control plane's half, driven as far as it goes.
//
// Everything under it is production. The capture rows are atc/db's, over the
// scenario's own real PostgreSQL. The node half is jetbridge's own
// OutputControlClient against the real output daemon this fixture started. The
// coordinator is atc/hangaroutput's, and it takes exactly the steps it would
// take in a deployment -- seal, publish, release -- and the fixture chooses
// none of them.
//
// WHAT THE FIXTURE PLAYS. Two things a deployment's other components do: the
// capture row is inserted where the step is admitted (the coordinator's own
// Insert, which is what a consumer that composes no transaction uses), and the
// producing Pod's containers are declared stopped, which is what the kubelet's
// status says in a cluster and what a standalone daemon reads from its
// --pod-terminations-dir.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/atc/postgresrunner"
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
}

// brineTransactor adapts the scenario's connection to the coordinator's port.
type brineTransactor struct{ conn db.DbConn }

func (transactor brineTransactor) Begin() (hangaroutput.Transaction, error) {
	tx, err := transactor.conn.Begin()
	if err != nil {
		return nil, err
	}

	// The same adapter production uses, for the same reason: the deferred
	// triggers refuse at COMMIT, and a scenario running an unmapped one would
	// be watching a different system from the deployed one.
	return db.HangarOutputTx{Tx: tx}, nil
}

// oneDaemonDialer is the whole cluster a brine scenario has: one node, one
// daemon. The node name and UID are still handed over, and a capture that
// names a node this fixture did not start is refused rather than routed to
// the one daemon there is.
func oneDaemonDialer(daemon HangarDaemon) hangaroutput.SourceDialer {
	control := jetbridgeClientFor(daemon)

	return hangaroutput.SourceDialerFunc(func(_ context.Context, name string,
		uid executioncontrol.NodeUID) (hangaroutput.SourceControl, error) {
		if string(uid) != daemon.NodeUID {
			return nil, fmt.Errorf("%w: capture names node %s (%s); this fixture's daemon is %s",
				hangaroutputleaf.ErrInfrastructure, name, uid, daemon.NodeUID)
		}

		return control, nil
	})
}

// brineCaptureNode is the node name the fixture's capture rows carry. The
// daemon is reached by UID; the name is what a deployment would resolve it by.
const brineCaptureNode = "brine-node"

// newSettlementPlane wires the coordinator over a scenario's database and
// daemon, and opens the activation epoch the daemon is already attested for.
func newSettlementPlane(daemon HangarDaemon, res brine.Resources) (settlementPlane, error) {
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

	return settlementPlane{
		DB:         jdb,
		Repository: repository,
		Coordinator: &hangaroutput.Coordinator{
			Transactor:      brineTransactor{conn: jdb.Conn},
			Rows:            repository,
			Dialer:          oneDaemonDialer(daemon),
			ActivationEpoch: executioncontrol.ActivationEpoch(hangarEpoch),
		},
	}, nil
}

// insert is step 1's database half for a fixture capture: a pending row on
// the fixture's one node.
func (plane settlementPlane) insert(admission captureAdmission) error {
	_, err := plane.Coordinator.Insert(context.Background(), hangaroutputleaf.PendingCapture{
		Execution: admission.Execution,
		Output:    admission.Output,
		Node:      brineCaptureNode,
		NodeUID:   executioncontrol.NodeUID(hangarNodeUID),
		Term:      time.Until(admission.CaptureDeadline),
	})

	return err
}

// read is the row as production left it.
func (plane settlementPlane) read(key hangaroutputleaf.CaptureKey) (hangaroutputleaf.Capture, error) {
	tx, err := plane.DB.Conn.Begin()
	if err != nil {
		return hangaroutputleaf.Capture{}, err
	}
	defer db.Rollback(tx)

	return plane.Repository.GetCapture(context.Background(), tx, key)
}

// settle inserts the capture's row, declares the producing Pod stopped, and
// lets the coordinator take it as far as it goes. A cancellation enters first,
// through the coordinator's own Discard, because a cancellation is a request
// and not a row: what the row then becomes is the coordinator's answer.
func settle(in FinishWitnessed, res brine.Resources) (CaptureOutcome, error) {
	outcome := CaptureOutcome{Source: in.Source}

	plane, err := newSettlementPlane(in.Source.Draft.Daemon, res)
	if err != nil {
		return outcome, err
	}
	outcome.Plane = &plane
	if err := plane.insert(in.Source.Admission); err != nil {
		return outcome, fmt.Errorf("inserting the pending capture: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	key := in.Source.key()

	if in.Cancelled {
		if _, err := plane.Coordinator.Discard(ctx, key, hangaroutputleaf.DiscardRunCancelled); err != nil {
			outcome.Err = err

			return recordSettlement(plane, key, outcome)
		}
	}
	if in.Source.PodUID != "" {
		if err := in.Source.Draft.Daemon.terminate(in.Source.PodUID); err != nil {
			return outcome, err
		}
	}
	if err := plane.Coordinator.Advance(ctx, key); err != nil {
		outcome.Err = err
	}

	return recordSettlement(plane, key, outcome)
}

// recordSettlement reads back what production wrote, through the production
// repository.
func recordSettlement(plane settlementPlane, key hangaroutputleaf.CaptureKey,
	outcome CaptureOutcome) (CaptureOutcome, error) {
	capture, err := plane.read(key)
	if err != nil {
		return outcome, err
	}
	outcome.Capture = capture
	if ref, err := capture.Ref(); err == nil {
		outcome.Published = hangaroutputleaf.CapturePublishResult{
			ProtocolVersion: hangaroutputleaf.ProtocolVersion,
			Ref:             ref,
			MarkerVersion:   hangaroutputleaf.MarkerVersion,
			Metageneration:  1,
		}
	}

	return outcome, nil
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
func openActivationEpoch(jdb JetbridgeDB, cohort ...string) error {
	attestation := "{}"
	if len(cohort) > 0 {
		body, err := json.Marshal(map[string]any{"members": []map[string]any{{"node": cohort[0], "control_key_id": hangarControlKeyID, "activation_epoch": hangarEpoch}}})
		if err != nil {
			return err
		}
		attestation = string(body)
	}

	if err := postgresrunner.ExecAsActivationRole(jdb.Conn, `
		INSERT INTO hangar_output_activation_epochs
			(epoch_id, base_state, output_state, base_attestation, output_attestation,
			 materialization_key_id, bucket_fingerprint, derived_namespace)
		VALUES ($1, 'enabled', 'enabled', $2::jsonb, '{}',
			'brine-materialize-key-1', 'gs://brine-output', 'brine/one')
		ON CONFLICT (epoch_id) DO NOTHING`,
		int64(hangarEpoch), attestation); err != nil {
		return fmt.Errorf("opening the activation epoch: %w", err)
	}

	return nil

}

// jetbridgeClientFor is the production client bound to this fixture's output
// daemon, minting capabilities with the fixture's own minter.
//
// Not a second HTTP client written here: the capability minting, the facet
// scoping, the per-call nonce and the wire encoding are the ones a deployment
// uses, and a fixture that reimplemented them would be asserting its own
// encoding rather than the ATC's.
func jetbridgeClientFor(daemon HangarDaemon) hangaroutput.SourceControl {
	return jetbridge.NewOutputControlClient(daemon.Output.URL,
		daemon.HTTP, daemon.Minter,
		executioncontrol.ActivationEpoch(hangarEpoch))
}
