package tests

import (
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

// The brine live tier's grant is cluster-wide and lands on the ServiceAccount
// every task pod runs as. Off has to mean absent, and on has to bind the
// task SA -- not the web SA, which is what every other binding in the chart
// names -- with the one verb that cannot be namespace-scoped.
func TestBrineLiveRBACIsOffByDefault(t *testing.T) {
	if got := render(t); strings.Contains(got, "-brine-live") {
		t.Error("the default render carries the brine live ClusterRole; it must be opt-in")
	}
}

func TestBrineLiveRBACBindsTheTaskServiceAccount(t *testing.T) {
	cases := []struct {
		name    string
		sets    []string
		subject string
	}{
		{"the default task SA", []string{"rbac.brineLive=true"}, "default"},
		{"the configured task SA", []string{"rbac.brineLive=true", "kubernetes.serviceAccount=brine-live"}, "brine-live"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A namespace other than "default" so the binding's namespace
			// is provably .Release.Namespace and not a literal, and the
			// default SA name "default" cannot mask a swapped field.
			const releaseNamespace = "brine-rbac-ns"
			role, binding := brineLiveObjects(t, renderInNamespace(t, releaseNamespace, tc.sets...))
			if role == nil || binding == nil {
				t.Fatalf("render lacks the ClusterRole (%v) or ClusterRoleBinding (%v)", role != nil, binding != nil)
			}
			if !grants(role, "", "namespaces", "create") || !grants(role, "", "namespaces", "delete") {
				t.Errorf("ClusterRole does not grant namespaces create+delete: %+v", role.Rules)
			}
			if !grants(role, "", "pods/exec", "create") || !grants(role, "discovery.k8s.io", "endpointslices", "create") {
				t.Errorf("ClusterRole lacks pods/exec or endpointslices create: %+v", role.Rules)
			}
			// The object-store fixture port-forwards into its pod, and the
			// mirroring scenario grants its daemon a Role; both 403'd in CI
			// (build 872565) before these were here.
			if !grants(role, "", "pods/portforward", "create") {
				t.Errorf("ClusterRole lacks pods/portforward create: %+v", role.Rules)
			}
			if !grants(role, "rbac.authorization.k8s.io", "roles", "create") || !grants(role, "rbac.authorization.k8s.io", "rolebindings", "create") {
				t.Errorf("ClusterRole lacks roles/rolebindings create: %+v", role.Rules)
			}
			// The pod-access scenarios update their Role in place to revoke and
			// restore a permission (build 875225).
			if !grants(role, "rbac.authorization.k8s.io", "roles", "update") {
				t.Errorf("ClusterRole lacks roles update: %+v", role.Rules)
			}
			// The pod-access scenarios act as a made-up user (build 874205).
			if !grants(role, "", "users", "impersonate") || !grants(role, "", "groups", "impersonate") {
				t.Errorf("ClusterRole lacks users/groups impersonate: %+v", role.Rules)
			}
			if len(binding.Subjects) != 1 || binding.Subjects[0].Kind != "ServiceAccount" ||
				binding.Subjects[0].Name != tc.subject || binding.Subjects[0].Namespace != releaseNamespace {
				t.Errorf("binding subjects = %+v, want ServiceAccount %s in %s", binding.Subjects, tc.subject, releaseNamespace)
			}
			if binding.RoleRef.Kind != "ClusterRole" || binding.RoleRef.Name != role.Name {
				t.Errorf("binding roleRef = %+v, want ClusterRole %s", binding.RoleRef, role.Name)
			}
		})
	}
}

func brineLiveObjects(t *testing.T, rendered string) (*rbacv1.ClusterRole, *rbacv1.ClusterRoleBinding) {
	t.Helper()
	var role *rbacv1.ClusterRole
	var binding *rbacv1.ClusterRoleBinding
	for _, chunk := range strings.Split(rendered, "\n---") {
		if !strings.Contains(chunk, "-brine-live") {
			continue
		}
		var head struct{ Kind string }
		if err := yaml.Unmarshal([]byte(chunk), &head); err != nil {
			t.Fatalf("decode head: %v", err)
		}
		switch head.Kind {
		case "ClusterRole":
			role = &rbacv1.ClusterRole{}
			if err := yaml.UnmarshalStrict([]byte(chunk), role); err != nil {
				t.Fatalf("ClusterRole does not decode: %v", err)
			}
		case "ClusterRoleBinding":
			binding = &rbacv1.ClusterRoleBinding{}
			if err := yaml.UnmarshalStrict([]byte(chunk), binding); err != nil {
				t.Fatalf("ClusterRoleBinding does not decode: %v", err)
			}
		}
	}
	return role, binding
}

func grants(role *rbacv1.ClusterRole, group, resource, verb string) bool {
	for _, rule := range role.Rules {
		if !contains(rule.APIGroups, group) || !contains(rule.Resources, resource) {
			continue
		}
		if contains(rule.Verbs, verb) || contains(rule.Verbs, "*") {
			return true
		}
	}
	return false
}
