package contracts

import (
	"fmt"
	"strings"
)

// A field path addresses one field of a sealed record.
//
// The grammar is frozen: '/'-separated segments whose charset is the record
// identifier charset already used for entity ids (recordIdentifierPattern,
// record.go). Six incompatible spellings were proposed before it was frozen —
// dotted paths, bracketed indices, JSON pointers — so any second spelling
// appearing anywhere in the tree is a defect, not a convenience.
//
// Two forms exist, and they are deliberately not interchangeable:
//
//   - A DECLARATION path may use "*" as a whole segment, standing for every
//     element of an array: "body/findings/*/severity". It never names one
//     element.
//   - A stored ADDRESS never contains "*". It names one element by its stable
//     id: "body/findings/f-001/severity".
//
// Array POSITIONS are in neither form. A position is not an identity: reordering
// an entity set would silently repoint every address that used one.
const fieldPathSeparator = "/"

const fieldPathWildcard = "*"

// ValidateFieldDeclarationPath accepts the declaration form: identifier
// segments, with "*" legal as a whole segment.
func ValidateFieldDeclarationPath(path string) error {
	return validateFieldPath(path, true)
}

// ValidateFieldAddress accepts the stored form: identifier segments only. A
// stored address that contained "*" would address every element of an array at
// once, which is a query, not an address.
func ValidateFieldAddress(path string) error {
	return validateFieldPath(path, false)
}

func validateFieldPath(path string, allowWildcard bool) error {
	if path == "" {
		return fmt.Errorf("field path is required")
	}
	segments := strings.Split(path, fieldPathSeparator)
	for _, segment := range segments {
		if segment == fieldPathWildcard {
			if allowWildcard {
				continue
			}
			return fmt.Errorf("field path %q contains %q, which is legal only in a declaration path and never in a stored address", path, fieldPathWildcard)
		}
		if !recordIdentifierPattern.MatchString(segment) {
			return fmt.Errorf(
				"field path %q segment %q must be a non-empty identifier containing only letters, digits, dot, underscore, colon, or hyphen",
				path, segment,
			)
		}
	}
	return nil
}

// FieldAddressMatchesDeclaration reports whether declaration addresses the field
// that address names. A "*" segment matches exactly one address segment, never
// zero and never several: the two forms describe the same tree at the same
// depth.
//
// Neither argument is validated here. Callers that accept either form from
// outside the process must validate first; callers comparing against the frozen
// declaration table already hold validated paths.
func FieldAddressMatchesDeclaration(declaration, address string) bool {
	declared := strings.Split(declaration, fieldPathSeparator)
	stored := strings.Split(address, fieldPathSeparator)
	if len(declared) != len(stored) {
		return false
	}
	for index, segment := range declared {
		if segment == fieldPathWildcard {
			continue
		}
		if segment != stored[index] {
			return false
		}
	}
	return true
}
