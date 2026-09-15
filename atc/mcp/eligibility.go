package mcp

import (
	"context"
	"fmt"
	"net/http"

	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/skymarshal/mcpauth"
)

type AccountEligibility string

const (
	AccountTargetCheckRequired AccountEligibility = "target_check_required"
	AccountIneligible          AccountEligibility = "ineligible"
)

// AccountEligibilityForAction proves only target-free exclusions. The caller must
// authenticate the MCP principal first and independently enforce consent/support.
// This never authorizes an operation, probes target existence or evaluates policy
// against invented payloads. The full wrapped API remains the execution boundary.
func AccountEligibilityForAction(ctx context.Context, factory accessor.AccessFactory, principal mcpauth.Principal, customRoles map[string]string, action string) (AccountEligibility, error) {
	kind, known := auth.AuthorizationKindForAction(action)
	if !known {
		return "", fmt.Errorf("unclassified API action %q", action)
	}
	if factory == nil {
		return "", fmt.Errorf("capability access factory is unavailable")
	}
	r, err := http.NewRequestWithContext(accessor.WithTrustedClaims(ctx, principal.Claims), http.MethodGet, "/", nil)
	if err != nil {
		return "", err
	}
	access, err := factory.Create(r, accessor.EffectiveRole(customRoles, action))
	if err != nil {
		return "", fmt.Errorf("capability access temporarily unavailable: %w", err)
	}
	if !access.IsAuthenticated() {
		return "", fmt.Errorf("authenticated capability context required")
	}
	switch kind {
	case auth.AuthorizationAdmin:
		if !access.IsAdmin() {
			return AccountIneligible, nil
		}
	case auth.AuthorizationTeam, auth.AuthorizationBuildWrite:
		if !access.IsAdmin() && len(access.TeamNames()) == 0 {
			return AccountIneligible, nil
		}
	case auth.AuthorizationPipelineRead, auth.AuthorizationBuildRead, auth.AuthorizationBuildOutput,
		auth.AuthorizationAuthenticated, auth.AuthorizationDelegated:
		// Public targets or handler-level checks can permit these actions without
		// an eligible team. In particular metadata is not permission to read logs.
	default:
		return "", fmt.Errorf("unclassified authorization kind for %q", action)
	}
	return AccountTargetCheckRequired, nil
}
