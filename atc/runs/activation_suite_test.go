package runs_test

import (
	"context"
	"time"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runs"
	"github.com/concourse/concourse/hangar/output"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// testEpoch is the activation epoch every admission in this suite speaks for.
const testEpoch int64 = 1

// activateVersionedAdmission opens v2 admission on a fresh database the only
// way it can be opened today: an enabled Hangar output epoch with a safe
// bucket-policy attestation, and the durable Run activation marker admitting
// the same epoch. No supported route does this; it is the state an operator
// would have to reach before any Run -- over HTTP or from run_pipeline -- can
// be admitted, and nothing here fakes around it.
func activateVersionedAdmission(conn db.DbConn) {
	GinkgoHelper()
	_, err := conn.Exec(`
		INSERT INTO hangar_output_activation_epochs
			(epoch_id, base_state, output_state, base_attestation, output_attestation,
			 receipt_public_key_id, receipt_key_valid_from, receipt_key_valid_until,
			 materialization_key_id, bucket_fingerprint, derived_namespace)
		VALUES ($1, 'enabled', 'enabled', '{}', '{}', 'receipt-key-1',
			now() - interval '1 day', now() + interval '30 days',
			'materialize-key-1', 'gs://output-bucket', 'deployment/ns')`, testEpoch)
	Expect(err).NotTo(HaveOccurred())

	prefix, err := db.HangarConsumerPrefixHeld("runs-suite-activation")
	Expect(err).NotTo(HaveOccurred())
	tx, err := conn.Begin()
	Expect(err).NotTo(HaveOccurred())
	defer db.Rollback(tx)
	Expect(db.NewHangarOutputRepository(prefix).RecordPolicyAttestation(context.Background(), tx, output.PolicySnapshot{
		ProtocolVersion:      output.ProtocolVersion,
		ActivationEpoch:      1,
		BucketFingerprint:    "gs://output-bucket",
		Metageneration:       3,
		PolicyHash:           "policy-hash-1",
		LifecycleDeleteRules: 0,
		State:                output.PolicySafe,
		ObservedAt:           output.NewTimestamp(time.Now()),
	}, nil)).To(Succeed())
	Expect(tx.Commit()).To(Succeed())

	_, err = conn.Exec(`UPDATE pipeline_run_activation SET epoch=$1, admission_enabled=true WHERE singleton`, testEpoch)
	Expect(err).NotTo(HaveOccurred())
}

// admitIn admits through the port's one admission at this suite's epoch and
// drops the replay flag, for specs that are about something else.
func admitIn(ctx context.Context, port runs.Admitter, tx runs.Tx, adm runs.Admission) (runs.Run, error) {
	run, _, err := port.AdmitVersionedRun(ctx, tx, adm, testEpoch)
	return run, err
}
