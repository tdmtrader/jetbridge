package runs

import (
	"database/sql"
	"errors"

	"github.com/concourse/concourse/atc/db"
)

// callerBuild is what the builds table says about the build that presented
// itself as the principal.
//
// BuildPrincipal is assembled entirely from step metadata, which the engine
// filled in from the build it is running -- so in production every field of it
// is already true. That is not the same as the port being able to rely on it.
// The principal arrives as a plain struct across an in-process boundary with
// no signature on it, and a port that authorized what it was told would be
// authorizing the caller's own assertion: a consumer with a bug, or a future
// consumer that assembles the principal from somewhere less trustworthy, would
// be admitting runs on any team it chose to name. The row is the evidence, and
// it costs one read through a transaction the caller is already holding.
type callerBuild struct {
	// teamName is the team the build really belongs to, as opposed to the one
	// the principal says it does.
	teamName string

	// pipelineID is the build's own pipeline. Zero for a one-off build, which
	// has none.
	pipelineID int

	pipelineName string
	jobName      string
	buildName    string

	// completed and aborted are the two ways a build stops being entitled to
	// act. A finished build admitting runs is a build whose step is still
	// running somewhere it should not be; an aborted one is a build whose
	// operator has already said stop, and `aborted` is set at the moment of
	// the request rather than when the build settles, which is exactly when
	// the port should stop honouring it.
	completed bool
	aborted   bool

	// templatePipelineID is the template whose run the caller's pipeline is
	// the payload of, when it is one; zero when the caller's pipeline is an
	// ordinary pipeline. It is read here, with the rest of the build's
	// identity, because the recursion check needs it and a second read would
	// want a second connection -- see AdmitRun on the port's connection
	// budget.
	templatePipelineID int
}

// callerBuildQuery reads one build's identity, its liveness and its run
// lineage.
//
// The columns mirror atc/db's own buildsQuery where they overlap, deliberately
// and column for column: pipeline name off the joined pipelines row, job name
// as COALESCE(j.name, b.run_job_name) because a payload pipeline's build
// carries its job name on the build rather than on a jobs row. StepMetadata is
// filled from Build.PipelineName()/JobName()/Name(), which are those same
// expressions, so a comparison against them is a comparison against the values
// the engine really handed the step -- not against a near-miss that would
// refuse honest builds.
//
// The lineage subselect walks the one edge that matters: the caller's pipeline
// carries pipeline_run_id when it is a run's payload, and that run names the
// template it materialized from. COALESCE to zero keeps the scan free of null
// handling for a case -- an ordinary pipeline, or none at all -- that is not
// an error.
const callerBuildQuery = `
	SELECT t.name,
	       COALESCE(b.pipeline_id, 0),
	       COALESCE(p.name, ''),
	       COALESCE(j.name, b.run_job_name, ''),
	       b.name,
	       b.completed,
	       b.aborted,
	       COALESCE((SELECT pr.template_pipeline_id FROM pipeline_runs pr WHERE pr.id = p.pipeline_run_id), 0)
	FROM builds b
	JOIN teams t ON t.id = b.team_id
	LEFT JOIN pipelines p ON p.id = b.pipeline_id
	LEFT JOIN jobs j ON j.id = b.job_id
	WHERE b.id = $1
`

// readCallerBuild reads the principal's build, reporting whether it exists.
//
// Through the caller's transaction, like every other read admission makes: the
// caller has held a pooled connection since Begin, and a read on the pool from
// here would want a second one while the first is still checked out. See
// AdmitRun for the deadlock that is, and connection_budget_test.go for the
// budget it would break.
func readCallerBuild(tx db.Tx, buildID int) (callerBuild, bool, error) {
	var caller callerBuild

	err := tx.QueryRow(callerBuildQuery, buildID).Scan(
		&caller.teamName,
		&caller.pipelineID,
		&caller.pipelineName,
		&caller.jobName,
		&caller.buildName,
		&caller.completed,
		&caller.aborted,
		&caller.templatePipelineID,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return callerBuild{}, false, nil
		}

		return callerBuild{}, false, err
	}

	return caller, true, nil
}
