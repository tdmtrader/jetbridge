package atc

var EnableResourceCausality bool

func FeatureFlags() map[string]bool {
	// If a feature flag is removed from this map, make sure it is also removed
	// from the corresponding type in Elm (web/elm/src/Concourse.elm -> FeatureFlags)
	return map[string]bool{
		"resource_causality": EnableResourceCausality,
	}
}
