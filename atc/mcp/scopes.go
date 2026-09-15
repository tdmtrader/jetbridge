package mcp

import (
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/skymarshal/mcpauth"
)

// ScopeForAction classifies operations, not HTTP verbs: hijack is a GET that
// executes commands, and pipeline runs can interpolate executable configuration.
// An unclassified operation is denied until its authority is explicitly chosen.
// These classifications do not register tools; only pipeline_status ships now.
func ScopeForAction(action string) (string, bool) {
	scope, known := actionScopes[atc.CanonicalAction(action)]
	return scope, known
}

var actionScopes = func() map[string]string {
	result := map[string]string{}
	for _, op := range Operations() {
		if old, exists := result[op.Action]; exists && old != op.Scope {
			panic("conflicting MCP scopes for action " + op.Action)
		}
		result[op.Action] = op.Scope
	}
	return result
}()

func AllowsAction(principal mcpauth.Principal, action string) bool {
	scope, known := ScopeForAction(action)
	return known && principal.HasScope(scope)
}
