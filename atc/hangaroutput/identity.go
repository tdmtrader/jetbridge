package hangaroutput

// The idempotency fingerprints, in one place.
//
// Every cross-system step in this plane is two steps, and the only way to
// resolve an ambiguous first step is to repeat it with the SAME identity. That
// makes "what identity does a retry offer" the single most load-bearing
// decision in the coordinator, and the answer is always the same: derive it
// from durable facts, never mint it.
//
// A minted id looks harmless and is not. A fresh producer checkpoint on a retry
// is a different Stage 2 wearing an old idempotency key, which the repository
// correctly refuses -- forever. A fresh release intent is a second release of
// one source, which the daemon correctly refuses -- forever. Both turn a lost
// answer, which is recoverable, into a handoff that can never complete.
//
// They live together because they are one rule, and because a third one added
// later should have to read this comment before choosing a uuid.

import (
	"github.com/google/uuid"

	"github.com/concourse/concourse/hangar/output"
)

// fingerprintNamespace is this plane's own UUID namespace.
//
// A constant, so that two processes deriving the same identity derive the same
// value -- which is the entire property. It is not secret and does not need to
// be: these are idempotency keys, not capabilities, and nothing is authorized
// by knowing one.
var fingerprintNamespace = uuid.MustParse("6f1f0b6e-3a3a-4d2a-9a1c-2d0f5f7f0a11")

// checkpointFor derives the opaque producer checkpoint id for a handoff.
//
// Hangar attaches no meaning to it -- an opaque producer checkpoint has no
// semantic interpretation here -- so the only property that matters is that
// repeating an identity repeats it.
func checkpointFor(handoff output.HandoffID) output.OpaqueID {
	return output.OpaqueID("checkpoint-" + string(handoff))
}

// releaseIntentFor derives a branch's release intent id.
//
// Version 5 over the branch AND the handoff, so the three branches cannot
// collide on one intent and the same branch always derives the same one.
func releaseIntentFor(handoff output.HandoffID, branch output.Disposition) output.ReleaseIntentID {
	return output.ReleaseIntentID(
		uuid.NewSHA1(fingerprintNamespace, []byte(string(branch)+":"+string(handoff))).String())
}
