package auth

import (
	"net/http"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
)

// CheckAgentAuthorizationHandler authorizes team-less /api/v1/agent/*
// routes against the main team (00-shared-contracts.md §4.2, decision
// 21). CheckAuthorizationHandler reads the team from the :team_name URL
// param; on team-less paths that yields IsAuthorized(""), which reduces
// to isAdmin — making such routes silently admin-only and their
// accessor DefaultRoles entries dead. Hardcoding atc.DefaultTeamName
// makes those entries effective.
func CheckAgentAuthorizationHandler(
	handler http.Handler,
	rejector Rejector,
) http.Handler {
	return checkAgentAuthorizationHandler{
		handler:  handler,
		rejector: rejector,
	}
}

type checkAgentAuthorizationHandler struct {
	handler  http.Handler
	rejector Rejector
}

func (h checkAgentAuthorizationHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	acc := accessor.GetAccessor(r)

	if !acc.IsAuthenticated() {
		h.rejector.Unauthorized(w, r)
		return
	}

	if !acc.IsAuthorized(atc.DefaultTeamName) {
		h.rejector.Forbidden(w, r)
		return
	}

	h.handler.ServeHTTP(w, r)
}
