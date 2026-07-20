package principals_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/api/principals"
)

func TestCreateSpecValidate(t *testing.T) {
	valid := principals.CreateSpec{Name: "ci-agent-review", Scopes: []string{principals.ScopeReviewsWrite}}
	if err := valid.Validate(); err != nil {
		t.Errorf("valid spec rejected: %v", err)
	}

	cases := map[string]principals.CreateSpec{
		"missing name":  {Scopes: []string{principals.ScopeReviewsWrite}},
		"no scopes":     {Name: "x"},
		"unknown scope": {Name: "x", Scopes: []string{"reviews:read"}},
	}
	for name, spec := range cases {
		if err := spec.Validate(); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

func TestPrincipalHasScope(t *testing.T) {
	p := principals.Principal{Scopes: []string{principals.ScopeTicketsRead, principals.ScopeTicketsWrite}}
	if !p.HasScope(principals.ScopeTicketsRead) {
		t.Error("expected tickets:read")
	}
	if p.HasScope(principals.ScopeReviewsWrite) {
		t.Error("did not expect reviews:write")
	}
}

func TestDeriveKind(t *testing.T) {
	cases := map[string]string{
		"ci-agent-review": principals.KindOperator,
		"legacy-publish":  principals.KindOperator,
		"agent-run-1":     principals.KindRun,
		"agent-run-482":   principals.KindRun,
		// non-digit or malformed suffixes are not the dispatcher's
		// naming convention, so they stay operator rather than being
		// silently hidden from the operator-managed rows.
		"agent-run-":     principals.KindOperator,
		"agent-run-12a":  principals.KindOperator,
		"agent-runner-5": principals.KindOperator,
	}
	for name, want := range cases {
		if got := principals.DeriveKind(name); got != want {
			t.Errorf("DeriveKind(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestTokenHashNeverSerializes(t *testing.T) {
	p := principals.Principal{ID: 1, Name: "x", TokenHash: "secret-hash"}
	out, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "secret-hash") {
		t.Errorf("TokenHash leaked into JSON: %s", out)
	}
}
