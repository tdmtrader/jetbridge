package composition

import (
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
