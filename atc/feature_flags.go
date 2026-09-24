package atc

var (
	EnableGlobalResources                bool
	EnableBuildRerunWhenWorkerDisappears bool
	EnableResourceCausality              bool
)

func FeatureFlags() map[string]bool {
	// If a feature flag is removed from this map, make sure it is also removed
	// from the corresponding type in Elm (web/elm/src/Concourse.elm -> FeatureFlags)
	return map[string]bool{
		"global_resources":   EnableGlobalResources,
		"build_rerun":        EnableBuildRerunWhenWorkerDisappears,
		"resource_causality": EnableResourceCausality,
	}
}

var (
	DisableRedactSecrets bool

	// PipelineRunActivationEpoch is the Run contract activation epoch this
	// server admits pipeline runs under, or zero when it admits none. It is
	// the Run contract's own and independent of the Hangar output epoch.
	// It is deliberately absent from FeatureFlags() above:
	// atc/api/infoserver serves that map on atc.GetInfo, which the auth wrappa
	// leaves in its unauthenticated case, and whether this server holds run
	// creation is not a fact an anonymous caller may read. It lives here, with
	// DisableRedactSecrets, for the same reason -- a process-wide operator
	// setting that is not published.
	PipelineRunActivationEpoch int64
)

// PipelineRunsActivated reports whether this server admits pipeline runs.
func PipelineRunsActivated() bool { return PipelineRunActivationEpoch > 0 }
