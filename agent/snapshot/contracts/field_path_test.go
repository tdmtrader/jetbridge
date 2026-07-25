package contracts_test

import (
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot/contracts"
)

// The field-path grammar is frozen by the sealed record schema layer design:
// '/'-separated segments whose charset is the record identifier charset, with
// '*' legal ONLY in a declaration path and never in a stored address.
//
// These tests pin both halves of that rule, because the whole value of freezing
// one spelling is lost the moment a second one is tolerated anywhere.

func TestValidateFieldDeclarationPathAcceptsWildcardSegments(t *testing.T) {
	valid := []string{
		"type",
		"record_version",
		"body",
		"body/summary",
		"subjects/*/digest",
		"body/findings/*/severity",
		"body/findings/*/evidence/*/locator/kind",
		"body/hypotheses/*/confidence/value",
		// Dot, colon, underscore and hyphen are inside the frozen segment
		// charset (recordIdentifierPattern, record.go), so "body.summary" is one
		// legal segment. That it is not a dotted spelling of "body/summary" is
		// enforced by resolution, not by the grammar — see
		// TestADottedPathIsOneSegmentAndResolvesToNothing.
		"body.summary",
		"body/a.b/c-d/e:f",
	}
	for _, path := range valid {
		if err := contracts.ValidateFieldDeclarationPath(path); err != nil {
			t.Fatalf("ValidateFieldDeclarationPath(%q) = %v, want it accepted", path, err)
		}
	}
}

func TestValidateFieldDeclarationPathRejectsMalformedPaths(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{name: "empty", path: ""},
		{name: "leading separator", path: "/body"},
		{name: "trailing separator", path: "body/"},
		{name: "empty segment", path: "body//summary"},
		{name: "space in segment", path: "body/my field"},
		{name: "slash inside segment charset", path: "body/find*ings"},
		{name: "segment starting with punctuation", path: "body/-summary"},
		{name: "bracket spelling", path: "body/findings[0]/severity"},
		{name: "json pointer spelling", path: "#/body/summary"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := contracts.ValidateFieldDeclarationPath(test.path); err == nil {
				t.Fatalf("ValidateFieldDeclarationPath(%q) accepted a path outside the frozen grammar", test.path)
			}
		})
	}
}

func TestValidateFieldAddressRejectsWildcards(t *testing.T) {
	if err := contracts.ValidateFieldAddress("body/findings/f-001/severity"); err != nil {
		t.Fatalf("ValidateFieldAddress(stable id) = %v, want it accepted", err)
	}
	err := contracts.ValidateFieldAddress("body/findings/*/severity")
	if err == nil {
		t.Fatal("ValidateFieldAddress() accepted '*', which is legal only in a declaration path")
	}
	if !strings.Contains(err.Error(), "declaration") {
		t.Fatalf("ValidateFieldAddress() error = %v, want it to say '*' belongs only in a declaration path", err)
	}
}

func TestFieldAddressMatchesDeclaration(t *testing.T) {
	tests := []struct {
		name        string
		declaration string
		address     string
		want        bool
	}{
		{name: "exact", declaration: "body/summary", address: "body/summary", want: true},
		{name: "one wildcard", declaration: "body/findings/*/severity", address: "body/findings/f-001/severity", want: true},
		{
			name:        "two wildcards",
			declaration: "body/findings/*/evidence/*/locator/kind",
			address:     "body/findings/f-001/evidence/e-002/locator/kind",
			want:        true,
		},
		{name: "different length", declaration: "body/findings/*/severity", address: "body/findings/f-001", want: false},
		{name: "different literal", declaration: "body/findings/*/severity", address: "body/findings/f-001/title", want: false},
		{name: "wildcard spans one segment only", declaration: "body/*/severity", address: "body/findings/f-001/severity", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := contracts.FieldAddressMatchesDeclaration(test.declaration, test.address); got != test.want {
				t.Fatalf("FieldAddressMatchesDeclaration(%q, %q) = %t, want %t",
					test.declaration, test.address, got, test.want)
			}
		})
	}
}
