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
// It admits a child run for one node of one build: core's port replays on the
// contract key derived from (build_id, plan_id), and this package records, in
// the same transaction, which call the run belongs to and the sealed-input
// digest it presented, refusing a replay whose digest moved. It also observes
// a child run and requests its cancellation, as thin pass-throughs to the port,
// which authorizes both and holds no transaction of this package's.
package composition
