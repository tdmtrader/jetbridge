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
