package atccmd

import (
	"context"

	"github.com/concourse/concourse/atc/agent/composition"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/runs"
	"github.com/concourse/concourse/skymarshal/skycmd"
)

// This file is the one place in core that names the agentic layer.
//
// architecture_test.go states the rule -- core may name the agentic layer
// exactly once, at a composition root -- and lists atc/atccmd in wiringPoints
// with this file as the reason. Everything on either side of the boundary is
// arranged so that this stays true: atc/exec declares the shape of the port it
// needs (exec.ChildRunAdmitter) and imports nothing agentic, atc/engine takes
// that interface as an option, and atc/agent/composition reaches back into
// core through atc/runs alone. The two never meet except here.
//
// So what lives here is deliberately the smallest thing that can: a
// constructor that assembles the port and the service over core's factories,
// and a translation between two structs that happen to hold the same values in
// the same order. If this file ever grows a decision -- a retry, a fallback, a
// second admitter chosen by config -- that decision belongs on one side of the
// boundary or the other, not in the seam.
//
// The design is docs/superpowers/specs/2026-09-08-run-pipeline-step-design.md.

// constructChildRunAdmitter builds the run_pipeline step's admitter: core's
// run-admission port, the composition service that makes admission idempotent
// on (build_id, plan_id), and the adapter that lets exec ask for one without
// naming either.
//
// The display-user-id generator and the custom role mapping are the same two
// values the API's authorization is built from, and they are passed for the
// same reason: a run admitted by a build must be attributed and authorized by
// exactly the rules a run admitted over HTTP is.
func (cmd *RunCommand) constructChildRunAdmitter(
	dbConn db.DbConn,
	lockFactory lock.LockFactory,
	teamFactory db.TeamFactory,
) (exec.ChildRunAdmitter, error) {
	displayUserIdGenerator, err := skycmd.NewSkyDisplayUserIdGenerator(cmd.DisplayUserIdPerConnector)
	if err != nil {
		return nil, err
	}

	customRoles, err := cmd.loadCustomRoles()
	if err != nil {
		return nil, err
	}

	admitter := runs.NewAdmitter(
		dbConn,
		db.NewPipelineRunFactory(dbConn, lockFactory),
		teamFactory,
		displayUserIdGenerator,
		customRoles,
	)

	return childRunAdmitter{service: composition.NewService(admitter)}, nil
}

// childRunAdmitter adapts composition's service to the port atc/exec declared.
//
// The mapping is field for field, and that is the point: the two structs are
// the same request stated on either side of a boundary neither package may
// cross. Keeping them separate types costs this function and buys the rule --
// exec cannot import composition, and composition cannot import exec.
type childRunAdmitter struct {
	service *composition.Service
}

func (a childRunAdmitter) AdmitChildRun(ctx context.Context, req exec.ChildRunRequest) (exec.ChildRun, error) {
	result, err := a.service.Admit(ctx, composition.Request{
		BuildID: req.BuildID,
		PlanID:  req.PlanID,

		Template:  req.Template,
		Params:    req.Params,
		Principal: req.Principal,

		InputDigest: req.InputDigest,
	})
	if err != nil {
		return exec.ChildRun{}, err
	}

	return exec.ChildRun{
		RunID:    result.RunID,
		Number:   result.Number,
		Replayed: result.Replayed,
	}, nil
}
