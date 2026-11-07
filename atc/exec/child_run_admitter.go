package exec

import (
	"context"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/runs"
)

// ChildRunAdmitter admits one child run for one node of one build.
//
// It is declared here rather than imported because the only production
// implementation is atc/agent/composition, and atc/exec may not name the
// agentic layer -- architecture_test.go permits exactly one core package to,
// and that package is the composition root. So exec states what it needs and
// takes whatever satisfies it; the adapter from this shape to composition's
// lives in atc/atccmd, and neither this file nor the step knows it exists.
//
// The types on the request are core's own: atc for the plan's values and
// atc/runs for admission's, which is core importing core. See
// docs/superpowers/specs/2026-09-08-run-pipeline-step-design.md.
type ChildRunAdmitter interface {
	AdmitChildRun(context.Context, ChildRunRequest) (ChildRun, error)

	// ObserveChildRun reads a child run's status. Each call is one short
	// read: a waiting step polls it and holds nothing in between.
	ObserveChildRun(context.Context, ChildRunObservation) (ChildRunStatus, error)

	// CancelChildRun asks for a child run to stop. It is nil when the run
	// was already being cancelled or had already finished, so a step
	// cancelling on its way out need not tell those apart.
	CancelChildRun(context.Context, ChildRunCancellation) error
}

// ChildRunRequest is one node of one build asking for one child run.
//
// BuildID and PlanID are the call's identity and the only things it is derived
// from: the same node asking twice re-attaches rather than admitting a second
// run. InputDigest is not part of that identity -- it is the sealed inputs,
// recorded on the first admission and compared on every later one, so that a
// re-attach whose inputs have moved is refused rather than silently answered
// with a run that was admitted for something else.
type ChildRunRequest struct {
	BuildID int
	PlanID  atc.PlanID

	Template  runs.TemplateRef
	Params    atc.RunParams
	Principal runs.Principal

	// Inputs binds the child's declared run inputs, each to a result of an
	// earlier child run. Core's replay compares them with the inputs the
	// run was first admitted with, so a moved binding is refused.
	Inputs map[string]atc.RunInputSource

	InputDigest string
}

// ChildRun is the admitted run, in the terms the step reports it in.
//
// Number rather than RunID is what the stdout line and the run's URL are built
// from: the id is a primary key nothing outside the database is addressed by,
// and the number is the ordinal a person sees in the web and passes to fly.
// Replayed distinguishes a run this call admitted from one it found.
type ChildRun struct {
	RunID    int
	Number   int
	Replayed bool
}

// ChildRunObservation is a step asking after the child run it admitted, as
// the build it is running in.
type ChildRunObservation struct {
	RunID     int
	Principal runs.Principal
}

// ChildRunStatus is a child run's status as the step reads it: running until
// the run publishes a terminal status, then that status for good.
// CancellationRequested says a stop has been asked for while the run may
// still read running.
type ChildRunStatus struct {
	RunID                 int
	Number                int
	Status                atc.RunStatus
	CancellationRequested bool
}

// Terminal reports whether the child run has finished.
func (s ChildRunStatus) Terminal() bool { return s.Status != atc.RunStatusRunning }

// ChildRunCancellation is a step asking for the child run it admitted to be
// stopped. Reason is recorded on the run when it is not empty.
type ChildRunCancellation struct {
	RunID     int
	Principal runs.Principal
	Reason    string
}
