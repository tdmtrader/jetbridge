package principals_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/api/principals"
)

func TestCreateSpecValidate(t *testing.T) {
	valid := principals.CreateSpec{Name: "ci-agent-review", Scopes: []string{principals.ScopeReviewsWrite}}
	if err := valid.Validate(); err != nil {
		t.Errorf("valid spec rejected: %v", err)
	}

	cases := map[string]struct {
		spec principals.CreateSpec
		want string
	}{
		"missing name": {
			spec: principals.CreateSpec{Scopes: []string{principals.ScopeReviewsWrite}},
			want: "name is required",
		},
		"no scopes": {
			spec: principals.CreateSpec{Name: "x"},
			want: "at least one scope is required",
		},
		"unknown scope": {
			spec: principals.CreateSpec{Name: "x", Scopes: []string{"reviews:read"}},
			want: `unknown scope "reviews:read"`,
		},
		"retired question authority": {
			spec: principals.CreateSpec{Name: "x", Scopes: []string{"questions:answer"}},
			want: `unknown scope "questions:answer"`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := tc.spec.Validate()
			if err == nil {
				t.Fatal("expected validation error")
			}
			if err.Error() != tc.want {
				t.Fatalf("validation error = %q, want %q", err, tc.want)
			}
		})
	}
}

func TestValidScopesContainsOnlyRetainedPublishingAndTicketScopes(t *testing.T) {
	want := map[string]bool{
		"reviews:write": true,
		"tickets:read":  true,
		"tickets:write": true,
		"metrics:write": true,
		"costs:write":   true,
	}
	if !reflect.DeepEqual(principals.ValidScopes, want) {
		t.Fatalf("ValidScopes = %#v, want %#v", principals.ValidScopes, want)
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
