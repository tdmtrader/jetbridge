package hangar

import (
	"fmt"
	"strings"
)

const digestPrefix = "sha256:"

type Scope string

func (scope Scope) Validate() error {
	raw := string(scope)
	if len(raw) < 1 || len(raw) > 63 {
		return fmt.Errorf("hangar: scope must contain 1 to 63 ASCII bytes")
	}
	if !isLowerAlphanumeric(raw[0]) {
		return fmt.Errorf("hangar: scope must start with a lowercase alphanumeric character")
	}
	for i := 1; i < len(raw); i++ {
		character := raw[i]
		if !isLowerAlphanumeric(character) && character != '.' && character != '_' && character != '-' {
			return fmt.Errorf("hangar: scope contains an invalid character")
		}
	}
	return nil
}

type Digest string

func (digest Digest) Validate() error {
	raw := string(digest)
	if len(raw) != len(digestPrefix)+64 || !strings.HasPrefix(raw, digestPrefix) {
		return fmt.Errorf("hangar: digest must be sha256 followed by 64 lowercase hexadecimal characters")
	}
	for i := len(digestPrefix); i < len(raw); i++ {
		if !isLowerHexadecimal(raw[i]) {
			return fmt.Errorf("hangar: digest must be sha256 followed by 64 lowercase hexadecimal characters")
		}
	}
	return nil
}

type TreeRef struct {
	Scope      Scope  `json:"scope"`
	Digest     Digest `json:"digest"`
	Generation int64  `json:"generation"`
}

func NewTreeRef(scope Scope, digest Digest, generation int64) (TreeRef, error) {
	ref := TreeRef{Scope: scope, Digest: digest, Generation: generation}
	if err := ref.Validate(); err != nil {
		return TreeRef{}, err
	}
	return ref, nil
}

func (ref TreeRef) Validate() error {
	if err := ref.Scope.Validate(); err != nil {
		return fmt.Errorf("hangar: tree scope: %w", err)
	}
	if err := ref.Digest.Validate(); err != nil {
		return fmt.Errorf("hangar: tree digest: %w", err)
	}
	if ref.Generation <= 0 {
		return fmt.Errorf("hangar: tree generation must be positive")
	}
	return nil
}

func ValidateDeploymentPrefix(prefix string) error {
	if prefix == "" {
		return nil
	}
	if strings.HasPrefix(prefix, "/") || strings.HasSuffix(prefix, "/") || strings.Contains(prefix, "\\") {
		return fmt.Errorf("hangar: deployment prefix must be a relative slash-separated path")
	}
	for _, segment := range strings.Split(prefix, "/") {
		if err := Scope(segment).Validate(); err != nil {
			return fmt.Errorf("hangar: deployment prefix segment: %w", err)
		}
	}
	return nil
}

func TreeKey(prefix string, scope Scope, digest Digest) (string, error) {
	if err := ValidateDeploymentPrefix(prefix); err != nil {
		return "", err
	}
	if err := scope.Validate(); err != nil {
		return "", err
	}
	if err := digest.Validate(); err != nil {
		return "", err
	}

	key := "hangar/v1/scopes/" + string(scope) + "/trees/sha256/" + strings.TrimPrefix(string(digest), digestPrefix) + ".tar.zst"
	if prefix != "" {
		key = prefix + "/" + key
	}
	return key, nil
}

func isLowerAlphanumeric(character byte) bool {
	return character >= 'a' && character <= 'z' || character >= '0' && character <= '9'
}

func isLowerHexadecimal(character byte) bool {
	return character >= '0' && character <= '9' || character >= 'a' && character <= 'f'
}
