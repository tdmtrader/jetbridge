package runs_test

import (
	"context"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runs"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// testEpoch is the activation epoch every admission in this suite speaks for.
const testEpoch int64 = 1

// activateVersionedAdmission opens v2 admission on a fresh database the only
// way it can be opened today: the Hangar output plane in service, and the durable Run activation marker admitting
// the same epoch. No supported route does this; it is the state an operator
// would have to reach before any Run -- over HTTP or from run_pipeline -- can
// be admitted, and nothing here fakes around it.
func activateVersionedAdmission(conn db.DbConn) {
	GinkgoHelper()
	_, err := db.SetHangarEnabled(context.Background(), conn, true)
	Expect(err).NotTo(HaveOccurred())

	_, err = conn.Exec(`UPDATE pipeline_run_activation SET epoch=$1, admission_enabled=true WHERE singleton`, testEpoch)
	Expect(err).NotTo(HaveOccurred())
}

// admitIn admits through the port's one admission at this suite's epoch and
// drops the replay flag, for specs that are about something else.
func admitIn(ctx context.Context, port runs.Admitter, tx runs.Tx, adm runs.Admission) (runs.Run, error) {
	run, _, err := port.AdmitVersionedRun(ctx, tx, adm, testEpoch)
	return run, err
}
