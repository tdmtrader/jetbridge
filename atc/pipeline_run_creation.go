package atc

import "errors"

// ErrPipelineRunCreationDisabled is the refusal a server answers when
// creation of durable pipeline runs is held by the operator.
//
// It is answered on both of the paths that create one: the public HTTP route,
// and atc/runs -- the in-process port a build's run_pipeline step admits
// through. One sentinel for both is what lets the two agree; a gate the route
// alone consulted would be a gate a build could walk around.
//
// It is declared here, in the API's own vocabulary package, rather than in
// atc/db, for three reasons. atc/db must not learn that the gate exists, so
// that CreateRun/CreateRunInTx keep their behaviour and every internal caller
// keeps working unchanged. Refusing above the factory means no transaction is
// opened, so no row, no run number, no payload pipeline and no notification
// follow by construction rather than by assertion. And an atc-package sentinel
// classified by atc/api/errormap is the existing pattern here, not a new one:
// InvalidRunParamsError is already classified that way.
var ErrPipelineRunCreationDisabled = errors.New("durable run creation is disabled on this server; an operator must enable it before runs can be created")
