package hangaroutput_test

import (
	"context"
	"errors"
	"testing"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/hangar/output"
)

// The operator status surface, read from the real schema: the in-service flag,
// the residue a drain waits on, and the open integrity findings.

func newStatusReader(t *testing.T, h *harness) *hangaroutput.StatusReader {
	t.Helper()

	return &hangaroutput.StatusReader{
		Transactor: h.Coordinator.Transactor,
		Repository: h.Repository,
	}
}

func readStatus(t *testing.T, h *harness) hangaroutput.Status {
	t.Helper()

	status, err := newStatusReader(t, h).Read(context.Background())
	if err != nil {
		t.Fatalf("reading status: %v", err)
	}

	return status
}

func recordFinding(t *testing.T, h *harness, violation output.IntegrityViolation) {
	t.Helper()

	tx, err := h.Conn.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := h.Repository.RecordRuntimeAtRisk(context.Background(), db.HangarOutputTx{Tx: tx},
		output.IntegrityFindingRecord{
			Violation: violation,
			Subject:   "gs://harness-output/some/object",
			Detail:    "recorded by the status specs",
		}); err != nil {
		t.Fatalf("recording %s: %v", violation, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// The control first: an in-service plane with nothing in flight is healthy and
// is not drained -- it is in service.
func TestAnInServicePlaneWithNothingInFlightReportsItself(t *testing.T) {
	h := newHarness(t)
	status := readStatus(t, h)

	if !status.Enabled {
		t.Error("the harness put the plane in service and the status says it is not")
	}
	if status.AtRisk || len(status.Findings) != 0 {
		t.Errorf("a fresh plane reports findings: %v", status.Findings)
	}
	if status.Counts.Residue() != 0 {
		t.Errorf("a fresh plane reports residue: %+v", status.Counts)
	}
	if status.Drained() {
		t.Error("an in-service plane reported itself drained; removing its daemon would strand " +
			"the next capture it admits")
	}
}

// A recorded runtime finding puts the plane at risk, says why, lists the
// finding with the id an operator resolves it by -- and resolving it by that
// id clears it.
func TestEveryRuntimeFindingClassPutsThePlaneAtRiskAndResolvesByID(t *testing.T) {
	for _, class := range output.IntegrityViolations() {
		t.Run(string(class), func(t *testing.T) {
			h := newHarness(t)
			recordFinding(t, h, class)

			status := readStatus(t, h)
			if !status.AtRisk {
				t.Errorf("an open %s did not put the plane at risk; the chart's one at-risk "+
					"alert covers every class only if every class raises it", class)
			}
			if status.Violations[class] != 1 || len(status.Findings) != 1 {
				t.Fatalf("%s was not counted and listed: %v %v", class, status.Violations, status.Findings)
			}
			if len(status.Why) != 1 || status.Why[0] != string(class) {
				t.Errorf("the at-risk reasons do not name %s: %v", class, status.Why)
			}
			if status.Counts.OpenIntegrityFindings != 1 {
				t.Errorf("the open finding was not counted: %+v", status.Counts)
			}

			finding := status.Findings[0]
			if err := hangaroutput.ResolveFinding(context.Background(), h.Coordinator.Transactor,
				h.Repository, finding.ID); err != nil {
				t.Fatalf("resolving finding %d: %v", finding.ID, err)
			}
			if err := hangaroutput.ResolveFinding(context.Background(), h.Coordinator.Transactor,
				h.Repository, finding.ID); err != nil {
				t.Errorf("resolving a resolved finding again: %v, want idempotent success", err)
			}
			if err := hangaroutput.ResolveFinding(context.Background(), h.Coordinator.Transactor,
				h.Repository, finding.ID+1000000); !errors.Is(err, output.ErrNotFound) {
				t.Errorf("resolving no finding at all answered %v, want not found", err)
			}

			after := readStatus(t, h)
			if after.AtRisk || len(after.Findings) != 0 {
				t.Errorf("a resolved finding still blocks: %v", after.Findings)
			}
		})
	}
}

// The residue is counted in one pass, from the rows the production capture
// path writes.
func TestThePlaneResidueIsCountedInOnePass(t *testing.T) {
	h := newHarness(t)

	pending := h.admit(t)
	status := readStatus(t, h)
	if status.Counts.PendingCaptures != 1 || status.Counts.NonterminalCaptures != 1 {
		t.Errorf("an admitted capture is not counted pending: %+v", status.Counts)
	}
	if status.Counts.Residue() == 0 {
		t.Error("a pending capture is not residue; a drain would remove the daemon under it")
	}
	_ = pending

	ref := registeredRef(t, h)
	claimOn(t, h, ref)
	status = readStatus(t, h)
	if status.Counts.LiveGenerations != 1 {
		t.Errorf("a registered generation is not counted: %+v", status.Counts)
	}
	if status.Counts.OpenClaims != 2 {
		t.Errorf("the capture's own claim and a consumer's are not two open claims: %+v", status.Counts)
	}
}

func TestAStatusReaderWithoutItsStoreIsRefused(t *testing.T) {
	h := newHarness(t)
	reader := newStatusReader(t, h)
	reader.Repository = nil

	if _, err := reader.Read(context.Background()); err == nil {
		t.Error("a status reader with no repository produced a reading")
	}
}
