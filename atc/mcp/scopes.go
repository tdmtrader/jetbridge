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
	switch action {
	case atc.GetPipeline:
		return mcpauth.ScopeRead, true
	case atc.SaveConfig, atc.CreatePipelineRun, atc.CreateBuild:
		return mcpauth.ScopePipelines, true
	case atc.CreateJobBuild, atc.AbortBuild:
		return mcpauth.ScopeBuilds, true
	case atc.HijackContainer:
		return mcpauth.ScopeHijack, true
	default:
		return "", false
	}
}

func AllowsAction(principal mcpauth.Principal, action string) bool {
	scope, known := ScopeForAction(action)
	return known && principal.HasScope(scope)
}
