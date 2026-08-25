package atc

import (
	"sigs.k8s.io/yaml"

	"github.com/concourse/concourse/vars"
)

// declaredReferenceNames returns the placeholder names a template owns: its
// declared parameters plus the reserved run identity. It returns nil for a
// config that is not a template, so callers get a nil (inert) exclusion.
func declaredReferenceNames(isTemplate bool, paramNames []string) []string {
	if !isTemplate {
		return nil
	}
	names := make([]string, 0, len(paramNames)+2)
	for _, name := range paramNames {
		if name != "" {
			names = append(names, name)
		}
	}
	return append(names, "run", "run_id")
}

// ParamReferenceExclusion returns the interpolation exclusion for a parsed
// config. Declared parameters and the run identity are filled in per run by
// the ATC when the run config is materialized; no earlier evaluator may
// consume them, and no credential check may treat them as missing secrets.
// It returns nil (no exclusion at all) for an ordinary pipeline.
func (config Config) ParamReferenceExclusion() vars.ReferenceExclusion {
	paramNames := make([]string, 0, len(config.Params))
	for _, schema := range config.Params {
		paramNames = append(paramNames, schema.Name)
	}
	names := declaredReferenceNames(config.Template, paramNames)
	if names == nil {
		return nil
	}
	return vars.NewExactReferenceExclusion(names)
}

// ParamReferenceExclusionFromPayload does the same for a raw, not yet
// interpolated pipeline payload, for the evaluators that run before the
// config can be unmarshaled. It reads only the two top-level keys it needs
// and fails open (nil) on anything it cannot understand, so a task config
// whose `params` is a map of environment variables is never mistaken for a
// pipeline template declaration.
func ParamReferenceExclusionFromPayload(payload []byte) vars.ReferenceExclusion {
	var declaration struct {
		Template bool `json:"template"`
		Params   []struct {
			Name string `json:"name"`
		} `json:"params"`
	}
	if err := yaml.Unmarshal(payload, &declaration); err != nil {
		return nil
	}

	paramNames := make([]string, 0, len(declaration.Params))
	for _, param := range declaration.Params {
		paramNames = append(paramNames, param.Name)
	}
	names := declaredReferenceNames(declaration.Template, paramNames)
	if names == nil {
		return nil
	}
	return vars.NewExactReferenceExclusion(names)
}