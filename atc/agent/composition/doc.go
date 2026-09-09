// Package composition is v4's first commit, and the first consumer of core's
// run-admission port.
//
// # The boundary
//
// This package reaches core through exactly two in-module packages: atc, for
// shared value types, and atc/runs, for admission. Nothing else. Not atc/db,
// not atc/engine, not atc/exec, not atc/worker. architecture_test.go pins that
// set exactly, in both directions -- an import that is not pinned fails, and a
// pin whose import is gone fails -- so widening the reach is a visible edit
// with a reason attached, rather than a coupling that accumulates.
//
// The reason the rule is worth having is written on the pin that came before
// it: mcpserver talked to the database directly instead of going through
// core's own handlers, and its authorization drifted from the REST API it
// shadowed until it no longer enforced the same rules. Composing with core's
// published surface is what stops that happening twice.
//
// # What it does
//
// One thing: it admits a child run for one node of one build, idempotently.
// The call identity is (build_id, plan_id) and nothing else -- no server round
// trip, no identifier minted after the fact -- so the same node asking twice
// re-attaches to the run it already got rather than admitting a second one.
//
// The mechanism is a unique constraint, not a lock. The build tracking lock
// looks like it would do, and it is released while a draining web's waiting
// goroutine is still alive, so two trackers can be live for one build at once.
// A dedup that leans on the lock passes its test and fails in production. The
// constraint holds under any schedule, and dropping it is what makes the race
// spec go red.
//
// The call row, the iteration row and the child run commit in one transaction
// or none of them do. That is why the port publishes an opener at all: this
// package opens the transaction, claims its call row, admits inside it, and
// writes the admitted run id onto the iteration row through the port's
// before-commit hook.
//
// What it reports back is the run's id, its number, and whether this call
// admitted it or found it. The number is the interesting one, because it is
// the smallest fact that shows what the boundary costs and why it is still
// worth it. It lives on pipeline_runs, a core table; on first admission it
// arrives on the run the before-commit hook is handed, but a call that
// re-attaches has only the run id it recorded, and a SELECT from here into
// pipeline_runs to turn that into a number is exactly the reach the rule above
// forbids. So core publishes a read -- runs.Admitter.LookupRun -- and this
// package calls it inside the transaction it already holds. The alternative,
// copying the number onto the iteration row, would put a value core owns into
// a table this package owns, where it could only ever go stale.
package composition
