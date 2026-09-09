package runs

import (
	"strings"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/db"
)

// authorization is what one admission's authorization step decided.
//
// It carries the resolved team as well as the verdict because both come out of
// the same single read of the teams table. Keeping them together is not a
// convenience: it is what makes resolution and authorization agree by
// construction, since they are answered from one snapshot rather than from two
// reads a deletion could land between.
type authorization struct {
	// createdBy is the display identity to record as the run's creator.
	createdBy string

	// isAdmin is the accessor's own verdict, which short-circuits every team
	// check. It is carried because it changes which refusal an unresolvable
	// team deserves: see resolveTemplate.
	isAdmin bool

	// team is the team the reference named, when it exists. Nil is reachable
	// only for an admin, who is authorized for teams that are not there.
	team db.Team
}

// authorize decides whether the principal may create runs on the named team,
// and returns the display identity to record as the run's creator.
//
// It calls accessor rather than restating the role table, and that is the
// whole design. The predicate has one declaration: the same DefaultRoles
// entry, the same EffectiveRole override, the same ValidateCustomRoles bound,
// the same RoleHasRequiredRole ordering and the same admin-team shortcut the
// HTTP create route applies. An injected Authorizer interface, or a local copy
// of "CreatePipelineRun requires member", would let the in-process path drift
// more permissive than the HTTP path silently -- which is exactly the drift
// architecture_test.go records mcpserver committing when it talked to the
// database instead of core's handlers.
//
// The second consequence is that no double is needed to test it. A spec builds
// a real team with a real atc.TeamAuth, real claims and the real display-user-id
// generator, and the verdict it observes is the production verdict.
//
// It reads through the caller's transaction, which is the whole of the port's
// connection budget: see AdmitRun. Both principal forms are decided from the
// one GetTeamsInTx read below and no other, so adding the build form did not
// add a connection to the budget connection_budget_test.go pins.
func (a *admitter) authorize(tx db.Tx, teamName string, principal Principal) (authorization, error) {
	// The principal has to be one identity before anything else can be said
	// about it, so this comes before any read and before the operator's role
	// mapping is weighed: a request that names two identities, or none, is
	// malformed rather than unauthorized, and answering it with a verdict
	// would mean having picked one of them.
	hasClaims, hasBuild := principal.Claims != nil, principal.Build != nil
	if hasClaims == hasBuild {
		return authorization{}, ErrPrincipalAmbiguous
	}

	// Refused at admission rather than at construction so the refusal is an
	// admission outcome a caller can observe. atccmd validates the same map at
	// startup; this is the port's own guarantee, not a second copy of that one.
	if err := accessor.ValidateCustomRoles(a.customRoles); err != nil {
		return authorization{}, CustomRolesInvalidError{Err: err}
	}

	teams, err := a.teamFactory.GetTeamsInTx(tx)
	if err != nil {
		return authorization{}, err
	}

	if hasBuild {
		return authorizeBuild(teams, teamName, principal.Build)
	}

	// HasToken and IsTokenValid are true because the caller has already
	// verified the principal. The port authorizes; it does not authenticate.
	access := accessor.NewAccessor(
		accessor.Verification{HasToken: true, IsTokenValid: true, RawClaims: principal.Claims},
		accessor.EffectiveRole(a.customRoles, atc.CreatePipelineRun),
		"", nil,
		teams,
		a.displayUserIds,
	)

	if !access.IsAuthorized(teamName) {
		return authorization{}, ErrUnauthorized
	}

	return authorization{
		// The same value the HTTP handler records as created_by.
		createdBy: access.UserInfo().DisplayUserId,
		isAdmin:   access.IsAdmin(),
		team:      findTeam(teams, teamName),
	}, nil
}

// authorizeBuild decides a build acting for itself.
//
// There is no accessor here and there is nothing for one to read: a build
// presents no token, so there are no claims to weigh against the team's auth
// config, and inventing a synthetic set of them would put a fabricated
// identity through the very predicate the accessor exists to declare once.
// What a build has instead is the team it belongs to, and the rule is the one
// set_pipeline already applies to a build mutating pipeline configs -- its own
// team and no other. A cross-team call is not a role question, so it is not
// answered by one; it is ErrUnauthorized, which is what the reference's team
// deserves when the build has no standing on it at all.
//
// Case-insensitively, and the fold mirrors findTeam's rather than being chosen
// afresh: the two have to agree, because a match here that findTeam then
// failed to resolve would hand resolveTemplate a nil team for a principal that
// is not an admin, and it would answer ErrUnauthorized for a team the build
// really does belong to.
//
// isAdmin is false and no build can make it true. Admin is a property of a
// team's auth applied to a person, and no build is a person; a build that
// happened to belong to the admin team would otherwise inherit the accessor's
// short-circuit over every other team, which is precisely the authority this
// form is meant not to have.
//
// The operator's custom role mapping is validated before this is reached even
// though no role is consulted in it. That is deliberate: the port declines to
// admit anything at all under a mapping it refuses to honour, and a build's
// admission is not an exception carved out of that guarantee.
func authorizeBuild(teams []db.Team, teamName string, build *BuildPrincipal) (authorization, error) {
	if !strings.EqualFold(teamName, build.TeamName) {
		return authorization{}, ErrUnauthorized
	}

	return authorization{
		createdBy: buildCreatedBy(build),
		isAdmin:   false,
		team:      findTeam(teams, teamName),
	}, nil
}

// buildCreatedBy is the display identity recorded as the run's creator when a
// build admitted it.
//
// The "build:" prefix is what keeps it from colliding with a person: the
// claims path records a display user id, which is whatever the connector's
// user_id claim held, and nothing stops one of those looking like a path. The
// rest is the build's own address in the same order a person reads it off the
// web -- team, pipeline, job, then the build's name after the hash, which is
// how build names are written everywhere else in this product.
func buildCreatedBy(build *BuildPrincipal) string {
	return "build:" + build.TeamName + "/" + build.PipelineName + "/" +
		build.JobName + "#" + build.BuildName
}

// findTeam picks the named team out of the list authorization was decided
// from, so that resolving a template costs no second read.
//
// Case-insensitively, because that is how TeamFactory.FindTeam matches and this
// stands in for it. Note that the accessor's own verdict is case-*sensitive*
// (it keys a map by team name), so for a non-admin the fold can only ever match
// the name that already authorized; it is the admin short-circuit that makes
// the difference observable.
func findTeam(teams []db.Team, name string) db.Team {
	for _, team := range teams {
		if strings.EqualFold(team.Name(), name) {
			return team
		}
	}

	return nil
}
