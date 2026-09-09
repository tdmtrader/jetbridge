package composition

import (
	"errors"
	"fmt"

	"github.com/concourse/concourse/atc/runs"
)

// DigestConflictError reports a call presenting a different sealed-input
// digest from the one recorded when it was first admitted.
//
// It names both digests because the caller cannot otherwise tell what moved.
// It is never a second admission: the digest is recorded and verified, not
// keyed on. Keying on it would break idempotence -- a prior step's pod
// eviction moves the digest, the key would move with it, and one logical
// invocation would get two runs.
type DigestConflictError struct {
	Recorded  string
	Presented string
}

func (e DigestConflictError) Error() string {
	return fmt.Sprintf("sealed input digest changed for an already-admitted call: recorded %s, presented %s",
		e.Recorded, e.Presented)
}

// AdmissionRefusal marks the conflict as a refusal rather than a fault: the
// call's inputs moved, which is a fact about what the caller asked for and is
// not fixed by asking again.
//
// It is declared here because the consumer that has to act on it -- the
// run_pipeline step -- cannot name this package, and core cannot name it
// either. runs.IsRefusal reads the marker instead, so the classification
// travels with the error and the dependency points from the agentic layer into
// core, which is the only direction architecture_test.go allows.
func (e DigestConflictError) AdmissionRefusal() {}

// The value form is what service.go returns, so that is the form the assertion
// pins: a pointer receiver here would compile and would silently stop
// satisfying the interface at every call site that returns the value.
var _ runs.Refusal = DigestConflictError{}

// ErrCallRecordIncomplete reports a claimed call row with no iteration row for
// its first ordinal.
//
// This should be unreachable, and saying so is the point. The claim only
// returns no row when a conflicting call row is already committed, and the
// call row, the iteration row and the run commit together -- so a committed
// call without its iteration means that guarantee has been broken somewhere.
// Reporting it is how the break becomes visible instead of surfacing as a
// confusing nil run id.
//
// It is unreachable on one stated condition, and the condition is not the
// consumer's to enforce, so it is written down here rather than assumed. The
// two cascades are asymmetric: composition_iterations cascades from
// pipeline_runs as well as from the call, while composition_calls cascades only
// from builds. Delete a child run while its parent build survives and the
// iteration goes with it, leaving a committed call row with nothing under it --
// and from then on every Admit for that (build_id, plan_id) claims nothing,
// finds no iteration, and returns this error rather than re-admitting. Today
// the only production DELETE FROM pipeline_runs is team destruction
// (atc/db/team.go), which takes the builds with it, so no such state exists.
// Run retention is a named follow-on and would create one. Whoever adds it owns
// the choice: keep the cascade symmetric, or make replay treat a call row with
// no first iteration as re-claimable.
var ErrCallRecordIncomplete = errors.New("call row exists with no iteration for its first ordinal")
