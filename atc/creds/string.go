package creds

import "github.com/concourse/concourse/vars"

type String struct {
	variablesResolver vars.Variables
	rawCredString     string
}

func NewString(variables vars.Variables, credString string) String {
	return String{
		variablesResolver: variables,
		rawCredString:     credString,
	}
}

func (s String) Evaluate() (string, error) {
	return s.EvaluateWithReferenceExclusion(nil)
}

// EvaluateWithReferenceExclusion evaluates the string while leaving every
// reference matched by excludeReference in place.
func (s String) EvaluateWithReferenceExclusion(excludeReference vars.ReferenceExclusion) (string, error) {
	var credsString string

	err := evaluateWithReferenceExclusion(s.variablesResolver, s.rawCredString, &credsString, excludeReference)
	if err != nil {
		return s.rawCredString, err
	}

	return credsString, nil
}
