package composition

import (
	"errors"
	"fmt"
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

// ErrCallRecordIncomplete reports a claimed call row with no iteration row for
// its first ordinal.
//
// This should be unreachable, and saying so is the point. The claim only
// returns no row when a conflicting call row is already committed, and the
// call row, the iteration row and the run commit together -- so a committed
// call without its iteration means that guarantee has been broken somewhere.
// Reporting it is how the break becomes visible instead of surfacing as a
// confusing nil run id.
var ErrCallRecordIncomplete = errors.New("call row exists with no iteration for its first ordinal")
