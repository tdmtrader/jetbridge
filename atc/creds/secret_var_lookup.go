package creds

import (
	"github.com/concourse/concourse/vars"
)

type VariableLookupFromSecrets struct {
	Secrets     Secrets
	LookupPaths []SecretLookupPath
	Context     SecretLookupParams
}

func NewVariables(secrets Secrets, secretsLookupParams SecretLookupParams, allowRootPath bool) vars.Variables {
	return VariableLookupFromSecrets{
		Secrets:     secrets,
		LookupPaths: NewSecretLookupPathsWithParams(secrets, secretsLookupParams, allowRootPath),
		Context:     secretsLookupParams,
	}
}

func (sl VariableLookupFromSecrets) Get(ref vars.Reference) (any, bool, error) {
	val, found, err := sl.get(ref.Path)
	if err != nil {
		return nil, false, err
	}
	if !found {
		return nil, false, nil
	}
	result, err := vars.Traverse(val, ref.String(), ref.Fields)
	if err != nil {
		return nil, false, err
	}
	return result, true, nil
}

func (sl VariableLookupFromSecrets) get(path string) (any, bool, error) {
	if len(sl.LookupPaths) == 0 {
		// if no paths are specified (i.e. for fake & noop secret managers), then try 1-to-1 var->secret mapping
		result, _, found, err := GetWithParams(sl.Secrets, path, sl.Context)
		return result, found, err
	}
	// try to find a secret according to our var->secret lookup paths
	for _, rule := range sl.LookupPaths {
		// prepends any additional prefix paths to front of the path
		secretPath, err := rule.VariableToSecretPath(path)
		if err != nil {
			return nil, false, err
		}
		result, _, found, err := GetWithParams(sl.Secrets, secretPath, sl.Context)
		if err != nil {
			return nil, false, err
		}
		if !found {
			continue
		}
		return result, true, nil
	}
	return nil, false, nil
}

// GetSecretRef returns the K8s Secret coordinates for a resolved variable,
// if the underlying Secrets backend implements SecretRefProvider. It replicates
// the same lookup-path resolution as Get to find the matching secret path.
func (sl VariableLookupFromSecrets) GetSecretRef(ref vars.Reference) (*vars.SecretRef, bool) {
	provider, ok := sl.Secrets.(SecretRefProvider)
	if !ok {
		return nil, false
	}

	if len(sl.LookupPaths) == 0 {
		_, _, found, err := GetWithParams(sl.Secrets, ref.Path, sl.Context)
		if err != nil || !found {
			return nil, false
		}
		return provider.GetSecretRef(ref.Path)
	}

	for _, rule := range sl.LookupPaths {
		secretPath, err := rule.VariableToSecretPath(ref.Path)
		if err != nil {
			continue
		}
		_, _, found, err := GetWithParams(sl.Secrets, secretPath, sl.Context)
		if err != nil || !found {
			continue
		}
		return provider.GetSecretRef(secretPath)
	}
	return nil, false
}

func (sl VariableLookupFromSecrets) List() ([]vars.Reference, error) {
	return nil, nil
}
