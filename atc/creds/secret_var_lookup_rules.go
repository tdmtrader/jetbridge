package creds

// SecretLookupPath transforms variable name into full secret path
type SecretLookupPath interface {
	VariableToSecretPath(string) (string, error)
}

// secretLookupWithPrefix is an implementation which returns [prefix][separator][varName]
type secretLookupWithPrefix struct {
	Prefix string
}

func NewSecretLookupWithPrefix(prefix string) SecretLookupPath {
	return &secretLookupWithPrefix{
		Prefix: prefix,
	}
}

func (sl secretLookupWithPrefix) VariableToSecretPath(path string) (string, error) {
	return sl.Prefix + path, nil
}
