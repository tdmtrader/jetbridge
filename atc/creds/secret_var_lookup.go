package creds

import (
	"fmt"

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
	if err := sl.checkLookup(ref); err != nil {
		return nil, false, err
	}
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

// checkLookup refuses, before any backend is asked, a lookup that could
// resolve outside the team's prefix: a var path with a "." or ".." segment,
// or a team or pipeline name that is one. A backend that joins or cleans its
// lookup path (Vault's kv2 helper calls path.Join; credhub and dummy build
// their prefixes with it) would collapse the segment. Slashes alone are
// legitimate: ((vault:foo/bar)) names a secret beneath the prefix. This is
// the one gate: every var lookup reaches a backend through Get or
// GetSecretRef.
func (sl VariableLookupFromSecrets) checkLookup(ref vars.Reference) error {
	if seg, found := vars.DotSegment(ref.Path); found {
		return fmt.Errorf("invalid var '%s': path segment '%s' is not allowed", ref, seg)
	}
	if len(sl.LookupPaths) == 0 {
		// The names reach no path: the backend gets the var name alone.
		return nil
	}
	// Identifier validation only warns, so a pipeline can be renamed "..",
	// and its lookup prefix would then be its team's parent.
	for _, scope := range []struct{ kind, name string }{{"team", sl.Context.Team}, {"pipeline", sl.Context.Pipeline}} {
		if _, found := vars.DotSegment(scope.name); found {
			return fmt.Errorf("cannot look up var '%s': %s name '%s' is not a valid secret path segment", ref, scope.kind, scope.name)
		}
	}
	return nil
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
	if sl.checkLookup(ref) != nil {
		return nil, false
	}

	if len(sl.LookupPaths) == 0 {
		_, _, found, err := GetWithParams(sl.Secrets, ref.Path, sl.Context)
		if err != nil || !found {
			return nil, false
		}
		return provider.GetSecretRef(ref.Path, ref.Fields)
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
		return provider.GetSecretRef(secretPath, ref.Fields)
	}
	return nil, false
}

func (sl VariableLookupFromSecrets) List() ([]vars.Reference, error) {
	return nil, nil
}
