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
