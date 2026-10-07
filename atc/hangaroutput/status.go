package hangaroutput

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/hangar/output"
)

// StatusReader is the operator's view of the output plane, read in one
// transaction: whether it is in service, what residue a drain still waits on,
// and which integrity findings are open.
//
// One transaction, deliberately. These numbers are compared with each other --
// a drain is finished when every residue count is zero AT ONCE -- so reading
// them at different instants would let an operator draw a conclusion about a
// state that never existed.
//
// It DECIDES nothing. Admission takes hangar_enabled FOR SHARE on its own
// path, and the integrity trigger refuses on its own; this asks the same
// questions and reports the answers.
type StatusReader struct {
	Transactor Transactor
	Repository *db.HangarOutputRepository
}

// Status is what one read found.
type Status struct {
	// Enabled is the in-service flag admission takes FOR SHARE.
	Enabled bool

	// AtRisk is true while an open finding blocks admission, and Why is the
	// classes that put it there.
	AtRisk bool
	Why    []string

	// Violations counts open findings by class; Findings lists them, oldest
	// first, with the ids an operator resolves them by.
	Violations map[output.PolicyViolation]int
	Findings   []output.IntegrityFinding

	// Counts are the plane's residue and the live generations it keeps.
	Counts output.PlaneCounts
}

// Drained reports whether nothing is left for a removed daemon to strand:
// the plane is out of service and every residue count is zero.
func (status Status) Drained() bool {
	return !status.Enabled && status.Counts.Residue() == 0
}

// Read takes one pass.
func (reader *StatusReader) Read(ctx context.Context) (Status, error) {
	if reader.Transactor == nil || reader.Repository == nil {
		return Status{}, fmt.Errorf("%w: a status reader needs a transactor and a repository",
			output.ErrIncomplete)
	}

	tx, err := reader.Transactor.Begin()
	if err != nil {
		return Status{}, err
	}
	defer func() { _ = tx.Rollback() }()

	status := Status{Violations: map[output.PolicyViolation]int{}}

	if status.Enabled, err = reader.Repository.HangarEnabled(ctx, tx); err != nil {
		return Status{}, err
	}
	if status.Findings, err = reader.Repository.OpenIntegrityFindings(ctx, tx); err != nil {
		return Status{}, err
	}
	reasons := map[string]bool{}
	for _, finding := range status.Findings {
		status.Violations[finding.Violation]++
		if finding.BlocksAdmission {
			reasons[string(finding.Violation)] = true
		}
	}
	status.AtRisk = len(reasons) != 0
	status.Why = slices.Sorted(maps.Keys(reasons))

	if status.Counts, err = reader.Repository.CountOutputPlaneState(ctx, tx); err != nil {
		return Status{}, err
	}

	return status, nil
}

// ResolveFinding closes one open integrity finding by id, in its own
// transaction: the operator's statement that the cause was repaired.
func ResolveFinding(ctx context.Context, transactor Transactor, repository *db.HangarOutputRepository, id int64) error {
	tx, err := transactor.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if err := repository.ResolveIntegrityFinding(ctx, tx, id); err != nil {
		return err
	}

	return tx.Commit()
}
