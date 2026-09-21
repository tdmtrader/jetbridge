package tests

import (
	"sort"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

// The web ServiceAccount's cluster-scoped grant.
//
// With execution control on, web selects a node for every Run execution, for
// every result read and every input upload by LISTING nodes by readiness
// label, and the read-lease handler lists them to resolve a node's name. A
// ClusterRole that grants only `get` renders, deploys and passes every other
// check here, and then every Run start is a 403 from the API server. Off, the
// grant stays as narrow as it was: `get` alone resolves the artifact daemon's
// node IP.
func TestWebClusterRoleNodeVerbsFollowExecutionControl(t *testing.T) {
	cases := []struct {
		name  string
		sets  []string
		verbs []string
	}{
		{"default", nil, []string{"get"}},
		{"base execution control", baseControlSets, []string{"get", "list"}},
		{"output facet", outputSets, []string{"get", "list"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			role := webClusterRole(t, render(t, tc.sets...))
			got := nodeVerbs(role)
			if !equalStrings(got, tc.verbs) {
				t.Errorf("web ClusterRole nodes verbs = %v, want %v", got, tc.verbs)
			}
			// Secrets stay get-only in every mode: list would enumerate every
			// credential in the cluster.
			if grants(role, "", "secrets", "list") || !grants(role, "", "secrets", "get") {
				t.Errorf("web ClusterRole secrets rule changed: %+v", role.Rules)
			}
		})
	}
}

func webClusterRole(t *testing.T, rendered string) *rbacv1.ClusterRole {
	t.Helper()

	const name = "jb-concourse-jetbridge-web"
	var found *rbacv1.ClusterRole
	for _, chunk := range splitDocuments(rendered) {
		var head renderedObject
		if err := yaml.Unmarshal([]byte(chunk), &head); err != nil || head.Kind != "ClusterRole" || head.Metadata.Name != name {
			continue
		}
		role := &rbacv1.ClusterRole{}
		if err := yaml.UnmarshalStrict([]byte(chunk), role); err != nil {
			t.Fatalf("web ClusterRole does not decode: %v", err)
		}
		if found != nil {
			t.Fatalf("render carries two ClusterRoles named %s", name)
		}
		found = role
	}
	if found == nil {
		t.Fatalf("render lacks ClusterRole %s", name)
	}
	return found
}

// nodeVerbs is the sorted, de-duplicated union of verbs any rule grants on
// core-group nodes.
func nodeVerbs(role *rbacv1.ClusterRole) []string {
	set := map[string]bool{}
	for _, rule := range role.Rules {
		if contains(rule.APIGroups, "") && contains(rule.Resources, "nodes") {
			for _, verb := range rule.Verbs {
				set[verb] = true
			}
		}
	}
	verbs := make([]string, 0, len(set))
	for verb := range set {
		verbs = append(verbs, verb)
	}
	sort.Strings(verbs)
	return verbs
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
