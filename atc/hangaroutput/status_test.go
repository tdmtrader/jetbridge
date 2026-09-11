package hangaroutput_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/hangar/output"
)

// The operator status surface, read from the real schema.
//
// Real, because every number here is a question about rows: what is open, what
// is unreconciled, how old the newest attestation is, whether the cursor moved.
// A fake repository would be a second answer to each of them, and the one that
// is not the enforcement is the one that drifts.

func newStatusReader(t *testing.T, h *harness) *hangaroutput.StatusReader {
	t.Helper()

	return &hangaroutput.StatusReader{
		Transactor: h.Coordinator.Transactor,
		Repository: h.Repository,
		Epoch:      int64(harnessEpoch),
		Bucket:     "gs://harness-output",
	}
}

// The control first: a healthy plane says so, and says it with the same
// predicate every admission path uses.
func TestAHealthyPlaneReportsItselfHealthy(t *testing.T) {
	h := newHarness(t)
	reader := newStatusReader(t, h)

	status, err := reader.Read(context.Background())
	if err != nil {
		t.Fatalf("reading status: %v", err)
	}

	if status.AtRisk {
		t.Errorf("a freshly attested plane reports itself at risk: %v", status.Why)
	}
	if status.At.Time.IsZero() {
		t.Error("the status carries no instant. Every age is derived from it, and a web node " +
			"with a drifted clock would report an age the plane itself would not agree with")
	}
	if status.EvidenceStale {
		t.Errorf("the harness's own fresh attestation is reported stale at age %s",
			status.EvidenceAge)
	}

	// The closed vocabularies are reported WHOLE, zeroes included. A gauge that
	// only appears when it is nonzero is one an alert cannot tell from a scrape
	// that did not happen.
	if len(status.Violations) != 0 {
		t.Errorf("a healthy plane reported violations: %v", status.Violations)
	}
}

// A recorded runtime at-risk finding puts the plane at risk AND says why.
//
// Both halves. "At risk" with no reason is an alert an operator cannot act on,
// and this plane has eleven violation classes that mean eleven different
// mornings.
func TestARecordedViolationMakesThePlaneAtRiskAndNamesTheClass(t *testing.T) {
	h := newHarness(t)
	reader := newStatusReader(t, h)

	tx, err := h.Conn.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := h.Repository.RecordRuntimeAtRisk(context.Background(), db.HangarOutputTx{Tx: tx},
		int64(harnessEpoch), output.PolicyFinding{
			Violation: output.ViolationOutOfBandAbsence,
			Subject:   "gs://harness-output/some/object",
			Detail:    "the object is gone and this plane never admitted a delete for it",
		}); err != nil {
		t.Fatalf("recording the violation: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	status, err := reader.Read(context.Background())
	if err != nil {
		t.Fatalf("reading status: %v", err)
	}

	if !status.AtRisk {
		t.Error("an open out-of-band absence did not put the plane at risk. That violation is " +
			"an object this plane manages disappearing without an admitted delete, which is " +
			"the one thing the whole lifetime attestation exists to notice")
	}
	if status.Violations[output.ViolationOutOfBandAbsence] != 1 {
		t.Errorf("the violation was not counted by class: %v", status.Violations)
	}
	found := false
	for _, why := range status.Why {
		if why == string(output.ViolationOutOfBandAbsence) {
			found = true
		}
	}
	if !found {
		t.Errorf("the at-risk reasons do not name the class: %v", status.Why)
	}
}

// Stale evidence is at-risk on its own, with no violation row anywhere.
//
// Requirement 51 bounds the evidence age at 15 minutes, and past that "we have
// not checked" and "the check failed" are the same amount of evidence. The
// comparison is the enforcing path's own function rather than a literal
// repeated in the reader.
func TestEvidenceOlderThanItsBoundIsAtRiskWithNoViolationRow(t *testing.T) {
	h := newHarness(t)
	reader := newStatusReader(t, h)

	if _, err := h.Conn.Exec(`
		UPDATE hangar_policy_snapshots
		   SET observed_at = now() - interval '2 hours'
		 WHERE activation_epoch = $1`, int64(harnessEpoch)); err != nil {
		t.Fatalf("ageing the attestation: %v", err)
	}

	status, err := reader.Read(context.Background())
	if err != nil {
		t.Fatalf("reading status: %v", err)
	}

	if !status.EvidenceStale {
		t.Errorf("a two-hour-old attestation is not reported stale (age %s)", status.EvidenceAge)
	}
	if !status.AtRisk {
		t.Error("stale evidence did not put the plane at risk. A monitor that has not read " +
			"the bucket in two hours cannot say the policy is safe, and saying nothing is " +
			"what makes an unnoticed lifecycle rule possible")
	}
	if len(status.Violations) != 0 {
		t.Errorf("staleness invented a violation row: %v", status.Violations)
	}
	if status.EvidenceAge < time.Hour {
		t.Errorf("the reported age is %s; it is measured from the DATABASE clock and the row "+
			"was aged two hours", status.EvidenceAge)
	}
}

// An operation kind nobody holds is -1 and not zero.
//
// Zero reads as "expired right now" and an absent series reads as "not
// scraped"; -1 is neither, and the alert rule that cares says so.
func TestAnUnheldOperationLeaseIsReportedAsUnheldRatherThanExpired(t *testing.T) {
	h := newHarness(t)
	reader := newStatusReader(t, h)

	status, err := reader.Read(context.Background())
	if err != nil {
		t.Fatalf("reading status: %v", err)
	}
	if len(status.Leases) != 0 {
		t.Fatalf("a plane with no controllers running reported held leases: %v", status.Leases)
	}

	// The control: a held lease IS reported, with a positive term.
	tx, err := h.Conn.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := h.Repository.ClaimOperationLease(context.Background(),
		db.HangarOutputTx{Tx: tx}, output.OperationInventory, int64(harnessEpoch),
		uuid.NewString(), 20*time.Minute); err != nil {
		t.Fatalf("claiming: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	status, err = reader.Read(context.Background())
	if err != nil {
		t.Fatalf("reading status: %v", err)
	}
	remaining, held := status.Leases[output.OperationInventory]
	if !held {
		t.Fatal("a claimed lease is not reported at all")
	}
	if remaining <= 0 {
		t.Errorf("a lease claimed for twenty minutes reports %s remaining", remaining)
	}
}

// The plane's inventories are counted, and counted TOGETHER.
//
// One statement, because the numbers are compared with each other: six live
// generations and six open claims is a plane doing its job; six live
// generations and one claim from a build that finished yesterday is a leak.
func TestThePlaneInventoryIsCountedInOnePass(t *testing.T) {
	h := newHarness(t)
	reader := newStatusReader(t, h)

	before, err := reader.Read(context.Background())
	if err != nil {
		t.Fatalf("reading status: %v", err)
	}

	ref := registeredRef(t, h)
	claimOn(t, h, ref)

	after, err := reader.Read(context.Background())
	if err != nil {
		t.Fatalf("reading status: %v", err)
	}

	if after.Counts.LiveGenerations != before.Counts.LiveGenerations+1 {
		t.Errorf("a registered generation moved the count from %d to %d",
			before.Counts.LiveGenerations, after.Counts.LiveGenerations)
	}
	if after.Counts.OpenClaims != before.Counts.OpenClaims+1 {
		t.Errorf("an acquired claim moved the open-claim count from %d to %d",
			before.Counts.OpenClaims, after.Counts.OpenClaims)
	}
}

// A reader with no epoch or no bucket is refused rather than publishing
// somebody else's numbers.
func TestAStatusReaderWithoutItsIdentityIsRefused(t *testing.T) {
	h := newHarness(t)

	for name, mutate := range map[string]func(*hangaroutput.StatusReader){
		"no epoch":  func(r *hangaroutput.StatusReader) { r.Epoch = 0 },
		"no bucket": func(r *hangaroutput.StatusReader) { r.Bucket = "" },
		"no store":  func(r *hangaroutput.StatusReader) { r.Repository = nil },
	} {
		reader := newStatusReader(t, h)
		mutate(reader)

		if _, err := reader.Read(context.Background()); err == nil {
			t.Errorf("a status reader with %s produced a reading", name)
		}
	}
}
