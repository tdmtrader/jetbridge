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
		"global_resources":    EnableGlobalResources,
		"build_rerun":         EnableBuildRerunWhenWorkerDisappears,
		"resource_causality":  EnableResourceCausality,
	}
}

var (
	DisableRedactSecrets bool
)
