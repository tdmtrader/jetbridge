// Package credstest holds an in-memory credential manager for tests.
package credstest

import (
	"path"
	"time"

	"github.com/concourse/concourse/atc/creds"
	"github.com/concourse/concourse/vars"
)

// Secrets serves fixed values from memory. Its lookup paths are the real
// team/pipeline prefixing a credential manager applies: a ((var)) is looked
// up under "team/pipeline/", then "team/", then the bare path.
type Secrets struct {
	vars.StaticVariables
}

func (secrets *Secrets) NewSecretLookupPaths(teamName string, pipelineName string, allowRootPath bool) []creds.SecretLookupPath {
	lookupPaths := []creds.SecretLookupPath{}
	if len(pipelineName) > 0 {
		lookupPaths = append(lookupPaths, creds.NewSecretLookupWithPrefix(path.Join(teamName, pipelineName)+"/"))
	}
	lookupPaths = append(lookupPaths, creds.NewSecretLookupWithPrefix(teamName+"/"))
	lookupPaths = append(lookupPaths, creds.NewSecretLookupWithPrefix(""))
	return lookupPaths
}

func (secrets *Secrets) Get(secretPath string) (any, *time.Time, bool, error) {
	v, found, err := secrets.StaticVariables.Get(vars.Reference{Path: secretPath})
	if err != nil || !found {
		return nil, nil, false, err
	}
	return v, nil, true, nil
}
