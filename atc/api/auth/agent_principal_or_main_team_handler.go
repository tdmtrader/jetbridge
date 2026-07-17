package auth

import (
	"net/http"
	"strings"

	"github.com/concourse/concourse/agent/api/principals"
)

// AgentPrincipalOrMainTeamHandler implements the combined route tiers
// of 00-shared-contracts.md §4.2 ("authorized member/viewer (main);
// also principal(<scope>)"): requests bearing a cap1 principal token
// are authenticated by the principal tier
// (CheckAgentPrincipalHandlerFactory.HandlerFor); everything else —
// user JWTs, anonymous — falls through to main-team authorization
// (CheckAgentAuthorizationHandler). Owned by ticket-core; reused by
// platform-mcp-hitl for GetAgentQuestion/AnswerAgentQuestion in wave 3
// (ticket-core contract addendum).
func AgentPrincipalOrMainTeamHandler(principalTier, mainTeamTier http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearer, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if strings.HasPrefix(bearer, principals.TokenVersionPrefix) {
			principalTier.ServeHTTP(w, r)
			return
		}
		mainTeamTier.ServeHTTP(w, r)
	})
}
