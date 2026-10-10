package runs_test

import (
	"context"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runs"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// enableOutputPlane puts Hangar in service for templates requiring it.
func enableOutputPlane(conn db.DbConn) {
	GinkgoHelper()
	_, err := db.SetHangarEnabled(context.Background(), conn, true)
	Expect(err).NotTo(HaveOccurred())

}

// admitIn admits through the port and
// drops the replay flag, for specs that are about something else.
func admitIn(ctx context.Context, port runs.Admitter, tx runs.Tx, adm runs.Admission) (runs.Run, error) {
	run, _, err := port.AdmitVersionedRun(ctx, tx, adm)
	return run, err
}
