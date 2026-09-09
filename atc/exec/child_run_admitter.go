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
//
//counterfeiter:generate . ChildRunAdmitter
type ChildRunAdmitter interface {
	AdmitChildRun(context.Context, ChildRunRequest) (ChildRun, error)
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
