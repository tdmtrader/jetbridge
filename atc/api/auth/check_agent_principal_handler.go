package auth

import (
	"net/http"
	"strings"

	"github.com/concourse/concourse/agent/api/principals"
	"github.com/concourse/concourse/atc/api/accessor"
)

// CheckAgentPrincipalHandlerFactory implements the principal(<scope>)
// auth tier (00-shared-contracts.md §4.1): a cap1.<id>.<secret> bearer
// token verified against agent_principals with a required scope. Admin
// user tokens are also accepted so fly curl debugging works. All
// principal verification failures are 401.
type CheckAgentPrincipalHandlerFactory interface {
	// HandlerFor is the strict tier: principal token or admin user.
	HandlerFor(delegate http.Handler, rejector Rejector, scope string) http.Handler
	// HandlerForWithLegacyBypass additionally passes requests without a
	// cap1 token through to the delegate, which validates the legacy
	// static publish token itself
	// (agent/api/reviews.Handler.SubmitReview). Dual-accept window
	// only — removed together with --agent-review-publish-token.
	HandlerForWithLegacyBypass(delegate http.Handler, rejector Rejector, scope string) http.Handler
}

func NewCheckAgentPrincipalHandlerFactory(verifier *principals.Verifier) CheckAgentPrincipalHandlerFactory {
	return &checkAgentPrincipalHandlerFactory{verifier: verifier}
}

type checkAgentPrincipalHandlerFactory struct {
	verifier *principals.Verifier
}

func (f *checkAgentPrincipalHandlerFactory) HandlerFor(delegate http.Handler, rejector Rejector, scope string) http.Handler {
	return checkAgentPrincipalHandler{
		verifier: f.verifier, delegate: delegate, rejector: rejector, scope: scope,
	}
}

func (f *checkAgentPrincipalHandlerFactory) HandlerForWithLegacyBypass(delegate http.Handler, rejector Rejector, scope string) http.Handler {
	return checkAgentPrincipalHandler{
		verifier: f.verifier, delegate: delegate, rejector: rejector, scope: scope,
		legacyBypass: true,
	}
}

type checkAgentPrincipalHandler struct {
	verifier     *principals.Verifier
	delegate     http.Handler
	rejector     Rejector
	scope        string
	legacyBypass bool
}

func (h checkAgentPrincipalHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	bearer, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")

	if strings.HasPrefix(bearer, principals.TokenVersionPrefix) {
		p, err := h.verifier.Verify(bearer, h.scope)
		if err != nil {
			h.rejector.Unauthorized(w, r)
			return
		}
		h.delegate.ServeHTTP(w, r.WithContext(principals.NewContext(r.Context(), p)))
		return
	}

	acc := accessor.GetAccessor(r)
	if acc.IsAuthenticated() && acc.IsAdmin() {
		h.delegate.ServeHTTP(w, r)
		return
	}

	if h.legacyBypass {
		// Dual-accept window: the delegate validates the static publish
		// token itself and attributes the write to 'legacy-publish'.
		h.delegate.ServeHTTP(w, r)
		return
	}

	if !acc.IsAuthenticated() {
		h.rejector.Unauthorized(w, r)
	} else {
		h.rejector.Forbidden(w, r)
	}
}
