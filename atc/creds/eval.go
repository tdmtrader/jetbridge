package creds

import (
	"encoding/json"

	"github.com/concourse/concourse/vars"
	"sigs.k8s.io/yaml"
)

func evaluate(variablesResolver vars.Variables, in, out any) error {
	return evaluateWithReferenceExclusion(variablesResolver, in, out, nil)
}

func evaluateWithReferenceExclusion(variablesResolver vars.Variables, in, out any, excludeReference vars.ReferenceExclusion) error {
	byteParams, err := json.Marshal(in)
	if err != nil {
		return err
	}

	tpl := vars.NewTemplate(byteParams)

	bytes, err := tpl.Evaluate(variablesResolver, vars.EvaluateOpts{
		ExpectAllKeys:    true,
		ExcludeReference: excludeReference,
	})
	if err != nil {
		return err
	}

	return yaml.Unmarshal(bytes, out, UseJSONNumber)
}

// UseJSONNumber is a yaml.Unmarshal option that decodes numbers as json.Number.
func UseJSONNumber(decoder *json.Decoder) *json.Decoder {
	decoder.UseNumber()
	return decoder
}
