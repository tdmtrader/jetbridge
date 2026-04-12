package vars

type MultiVars struct {
	varss []Variables
}

func NewMultiVars(varss []Variables) MultiVars {
	return MultiVars{varss}
}

var _ Variables = MultiVars{}

func (m MultiVars) Get(ref Reference) (any, bool, error) {
	for _, vars := range m.varss {
		val, found, err := vars.Get(ref)
		if found || err != nil {
			return val, found, err
		}
	}

	return nil, false, nil
}

func (m MultiVars) GetSecretRef(ref Reference) (*SecretRef, bool) {
	for _, vars := range m.varss {
		// Find the source that has this variable
		_, found, err := vars.Get(ref)
		if err != nil || !found {
			continue
		}
		// If that source implements SecretRefResolver, ask it for the ref
		if resolver, ok := vars.(SecretRefResolver); ok {
			return resolver.GetSecretRef(ref)
		}
		// Source found the var but doesn't support secret refs
		return nil, false
	}
	return nil, false
}

func (m MultiVars) List() ([]Reference, error) {
	var allRefs []Reference

	for _, vars := range m.varss {
		defs, err := vars.List()
		if err != nil {
			return nil, err
		}

		allRefs = append(allRefs, defs...)
	}

	return allRefs, nil
}
