package creds

import (
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/vars"
)

type Source struct {
	variablesResolver vars.Variables
	rawSource         atc.Source
}

func NewSource(variables vars.Variables, source atc.Source) Source {
	return Source{
		variablesResolver: variables,
		rawSource:         source,
	}
}

func (s Source) Evaluate() (atc.Source, error) {
	return s.EvaluateWithReferenceExclusion(nil)
}

// EvaluateWithReferenceExclusion evaluates the source while leaving every
// reference matched by excludeReference in place. Template parameters are
// filled in per run, so they must not be reported as missing credentials.
func (s Source) EvaluateWithReferenceExclusion(excludeReference vars.ReferenceExclusion) (atc.Source, error) {
	var source atc.Source
	err := evaluateWithReferenceExclusion(s.variablesResolver, s.rawSource, &source, excludeReference)
	if err != nil {
		return nil, err
	}

	return source, nil
}
