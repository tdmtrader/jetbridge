package steps

// The consumer's half: what the ATC binds in PostgreSQL, and what the consuming
// Pod says.
//
// Every check over BoundOutput is a PRODUCTION read, never a raw SQL select —
// a repository that writes the right row through the wrong API has to fail
// these. The database is the real scenario-scoped PostgreSQL the estate already
// runs (steps/resources.go), reached by the fixture rather than by a phrase.
//
// The consumer's Pod reuses the existing PodCreated state, so the existing mount
// and volume checks compose with the two Go tests this family adopts from
// storage_daemonset_test.go: the exact-receipt one and the one-read-only-mount
// one. The init-script signal test does NOT move — it runs the generated shell
// with a fake wget on PATH, and script text is not spec.

import (
	"github.com/brine-dev/brine-go/pkg/brine"
)

const bindingPhase = "Phase 6 Green"

// HangarBindingDefinitions is the binding and consumer-pod family.
func HangarBindingDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		stubMap[PublishedTree, ConsumerDraft](
			"a later step {string} takes the published output {string} at {string}",
			bindingPhase,
			"the consumer-side input that resolves a published output to a TreeRef"),

		stubMap[ConsumerDraft, PodCreated](
			"the consumer's pod is built",
			bindingPhase,
			"BuildFetchInitContainers' managed-output branch"),

		stubMap[PublishedTree, BoundOutput](
			"the consumer binds the output inside its own transaction",
			bindingPhase,
			"AcquireClaim and the binding write, both inside the caller's transaction"),

		stubMap[BoundOutput, BoundOutput](
			"the consumer's transaction is rolled back",
			bindingPhase,
			"the rollback path that must leave no claim behind"),

		stubMap[BoundOutput, BoundOutput](
			"the binding is released",
			bindingPhase,
			"ReleaseClaim and the permanent tombstone it writes"),

		stubMap[BoundOutput, BoundOutput](
			"the binding is released again",
			bindingPhase,
			"idempotent release"),

		// The conflict twin: "release is idempotent" passes for a repository
		// that ignores the claim ID entirely.
		stubMap[BoundOutput, BoundOutput](
			"the released claim ID is acquired again",
			bindingPhase,
			"the typed lifecycle conflict a tombstoned claim ID must return"),

		stubMap[BoundOutput, BoundOutput](
			"a fresh claim ID is acquired",
			bindingPhase,
			"claim acquisition under a new ID"),

		stubMap[BoundOutput, BoundOutput](
			"the binding is verified",
			bindingPhase,
			"the verification that makes a binding visible"),

		// Checks over the binding, all through production reads.
		stubCheck[BoundOutput](
			"the claim protects generation {int}",
			bindingPhase,
			"the claim's exact generation, read back through the repository"),

		stubCheck[BoundOutput](
			"exactly {int} claim is recorded",
			bindingPhase,
			"the claim ledger view — a count of what is there, not of what was asked for"),

		stubCheck[BoundOutput](
			"no claim is left behind",
			bindingPhase,
			"the rollback that writes nothing"),

		stubCheck[BoundOutput](
			"the tombstone is permanent",
			bindingPhase,
			"the tombstone ReleaseClaim writes instead of deleting the row"),

		stubCheck[BoundOutput](
			"the candidate claim ID is unchanged",
			bindingPhase,
			"the claim ID that survives a hidden-to-published transition"),

		stubCheck[BoundOutput](
			"the binding is not visible",
			bindingPhase,
			"the visibility gate an unverified binding sits behind"),

		stubCheck[BoundOutput](
			"the binding is visible",
			bindingPhase,
			"the visibility a verified binding gains"),

		stubCheck[BoundOutput](
			"the binding is refused as {string}",
			bindingPhase,
			"the typed refusals a claim against an unregistered or unmanaged ref returns"),

		stubCheck[BoundOutput](
			"the managed read is granted",
			"Phase 6 Green",
			"the read lease and the grant its committed transaction authorizes"),

		stubCheck[BoundOutput](
			"the managed read is refused as {string}",
			"Phase 6 Green",
			"the refusal a read without an active claim must receive"),

		// Checks over the consuming Pod. PodCreated is reused deliberately: a
		// consumer's pod is a pod, and every existing mount and volume check
		// already reads it.
		stubCheck[PodCreated](
			"the consumer's Hangar init verifies exactly the receipt for its tree",
			bindingPhase,
			"the exact receipt BuildFetchInitContainers encodes into the verification command"),

		stubCheck[PodCreated](
			"the consumer's pod declares exactly {int} read-only verification mount per tree",
			bindingPhase,
			"the one fixed read-only verification mount each verified tree gets"),

		stubCheck[PodCreated](
			"no user-controlled destination enters the verification command",
			bindingPhase,
			"the server-derived destination the verification command is built from"),

		stubCheck[PodCreated](
			"the consumer's pod materializes exactly the receipt's tree",
			bindingPhase,
			"the materialization request the init container issues for the receipt's ref"),
	}
}
