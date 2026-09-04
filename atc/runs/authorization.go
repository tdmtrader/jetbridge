package runs

import (
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
)

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
func (a *admitter) authorize(teamName string, principal Principal) (string, error) {
	// Refused at admission rather than at construction so the refusal is an
	// admission outcome a caller can observe. atccmd validates the same map at
	// startup; this is the port's own guarantee, not a second copy of that one.
	if err := accessor.ValidateCustomRoles(a.customRoles); err != nil {
		return "", CustomRolesInvalidError{Err: err}
	}

	teams, err := a.teamFactory.GetTeams()
	if err != nil {
		return "", err
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
		return "", ErrUnauthorized
	}

	// The same value the HTTP handler records as created_by.
	return access.UserInfo().DisplayUserId, nil
}
