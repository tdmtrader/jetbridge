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
// connection budget: see AdmitRun.
func (a *admitter) authorize(tx db.Tx, teamName string, principal Principal) (authorization, error) {
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
