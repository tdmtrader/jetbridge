// Package runs is core's publisher of pipeline-run admission.
//
// # Why the package exists
//
// atc/db.PipelineRunFactory.CreateRunInTx is the extension point every
// composition feature rests on, and every one of its non-stdlib parameters and
// refusals is an atc/db type. A package that called it directly would import
// atc/db, and that is exactly the coupling architecture_test.go's ratchet
// exists to keep out of the layers above core -- mcpserver's pin records what
// happens when a second caller reaches past core's handlers into its database:
// its authorization drifted from the route it shadowed until it no longer
// enforced the same rules.
//
// So this package sits on the seam. It is core, classified as core by
// architecture_test.go, and it may import atc/db freely. What it publishes is
// the seam re-expressed in types a caller that cannot see atc/db can name --
// its own transaction interfaces, its own run struct, its own typed refusals.
//
// # The rule, stated exactly
//
// The port's exported *method* signatures name no atc/db type. NewAdmitter
// does name three, and cannot avoid it: there is no way to construct something
// over CreateRunInTx without naming the collaborator that owns it. The
// constructor is called from the composition root (atc/atccmd) and from suite
// files -- never from a consumer. This is the shape gc.NewDestroyer already
// uses (atc/gc/destroyer.go). A consumer holds an Admitter and never builds
// one, which is what makes the boundary checkable: atc/runs/db_free_consumer_test.go
// is a consumer whose import block contains no atc/db, and
// composition_boundary_test.go asserts that from outside the package.
//
// # What the port owns, and what it does not
//
// It owns authorization, because an in-process caller has no route to own it:
// there is no request, no handler factory and no team-scoped path segment, so
// the predicate the public create route applies has to be applied here or not
// at all. It owns resolving a template from a reference, which is also what
// lets it tell "no such pipeline" from "that pipeline is not a template" --
// a distinction CreateRunInTx cannot make, because by the time it runs the
// pipeline is already resolved.
//
// It does not own the transaction. Admission runs inside one the caller
// opened, so that a caller can commit its own rows in the same transaction as
// the run -- which is the only way a consumer-side call record and the run it
// admitted can be atomic. Begin is published for that reason and no other.
//
// It does not read any consumer's tables. A consumer's call record is the
// consumer's; a SELECT from here into it would breach the boundary this
// package exists to draw. The whole of the port's contract-key check is that a
// non-empty key was presented.
package runs

import (
	"context"
	"database/sql"

	"github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/atc"
)

// Tx is the transaction handle the port hands out and takes back.
//
// It is deliberately structural rather than an adapter: db.Tx satisfies it
// with no wrapper at the call site, so Begin can return the value BeginTx gave
// it by plain interface assignment, and AdmitRun can pass it back down. That
// assignment is the cheapest possible check that this interface is right --
// widen it wrongly and the port stops compiling.
//
// It is not stdlib-only. QueryRowContext returns squirrel.RowScanner because
// db.Tx's does; squirrel is a module dependency already and out-of-module
// imports are not what the reach guard counts, so a consumer calls .Scan() on
// the result without naming the package. Dropping the method to avoid the
// mention would buy nothing and would turn every single-row read into a
// QueryContext plus Next plus Scan.
type Tx interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) squirrel.RowScanner
}

// Transaction is what a consumer holds: a Tx it may also finish.
//
// The split from Tx is not decoration. AdmitRun and the before-commit callback
// take a Tx, so neither can commit: the callback runs inside the caller's
// transaction, and a callback that could commit there would take a lifecycle
// the caller owns. Only the value Begin returned can be finished, and only by
// whoever opened it.
type Transaction interface {
	Tx

	Commit() error
	Rollback() error
}

// TemplateRef names the template to admit a run of.
//
// The team name is part of the reference rather than derived from the
// principal: an in-process caller has no team-scoped route to inherit one
// from, and admission is scoped by the template's team.
type TemplateRef struct {
	Team     string
	Pipeline atc.PipelineRef
}

// Principal is the caller's already-verified identity.
//
// Claims are in the shape the API's token verification produces -- the same
// map the request accessor reads. The port derives two things from them: the
// role verdict on the template's team, and the display identity recorded as
// the run's creator. It does not authenticate; a caller that presents claims
// it did not verify has already lost.
type Principal struct {
	Claims map[string]any
}

// Admission is one request to admit one run.
type Admission struct {
	Template  TemplateRef
	Params    atc.RunParams
	Principal Principal

	// ContractKey is the caller's identity for this invocation. It is opaque
	// to the port, which checks only that it is non-empty: the record it keys
	// lives in a consumer table, and core never reads one.
	ContractKey string

	// CausedByRun is the causal-parent run, when there is one. The port passes
	// it through and defines no column, no migration and none of the refusals
	// that will attach to the edge; that is the run-contract track's work.
	CausedByRun *int

	// BeforeCommit runs inside the caller's transaction after the run and its
	// payload pipeline exist and before the caller commits. Returning an error
	// aborts creation. This is where a consumer writes the rows that must be
	// atomic with the run.
	BeforeCommit func(Tx, Run) error
}

// Run is the admitted run's identity, in the port's own terms.
//
// Deliberately a plain struct and not db.PipelineRun: the interface would drag
// atc.RunStatus's neighbours and the whole header model across the boundary,
// which is precisely the leak this package exists to prevent. A consumer that
// needs more than identity needs a read operation on the port, and there is
// not one yet.
type Run struct {
	ID                 int
	Number             int
	TemplatePipelineID int
	PayloadPipelineID  int
	CreatedBy          string
}
