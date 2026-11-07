package tests

import (
	"slices"
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
			// With the credential manager off, nothing in web reads a Secret.
			if grants(role, "", "secrets", "get") || grants(role, "", "secrets", "list") {
				t.Errorf("web ClusterRole grants secrets with the credential manager off: %+v", role.Rules)
			}
		})
	}
}

// The Kubernetes credential manager is web's only reader of Secrets. It reads
// <namespacePrefix><team>, one namespace per team, so its grant is
// cluster-wide; it is get-only, since list would enumerate every credential in
// the cluster. A deployment that predates kubernetes.credentialManager turns
// the manager on through web.env, and keeps the grant: dropping it would turn
// every ((var)) its pipelines resolve into a 403.
func TestWebClusterRoleReadsSecretsOnlyForTheCredentialManager(t *testing.T) {
	cases := []struct {
		name  string
		sets  []string
		reads bool
	}{
		{"default", nil, false},
		{"credential manager", []string{"kubernetes.credentialManager.enabled=true"}, true},
		{"in-cluster through web.env", []string{"web.env[0].name=CONCOURSE_KUBERNETES_IN_CLUSTER", "web.env[0].value=true"}, true},
		{"kubeconfig through web.env", []string{"web.env[0].name=CONCOURSE_KUBERNETES_CONFIG_PATH", "web.env[0].value=/etc/kube/config"}, true},
		{"unrelated web.env", []string{"web.env[0].name=CONCOURSE_LOG_DB_QUERIES", "web.env[0].value=true"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			role := webClusterRole(t, render(t, tc.sets...))
			if got := grants(role, "", "secrets", "get"); got != tc.reads {
				t.Errorf("web ClusterRole secrets get = %v, want %v: %+v", got, tc.reads, role.Rules)
			}
			if grants(role, "", "secrets", "list") || grants(role, "", "secrets", "watch") {
				t.Errorf("web ClusterRole can enumerate secrets: %+v", role.Rules)
			}
		})
	}
}

// Web calls neither the Deployments nor the DaemonSets API: the worker
// registrar lists pods, and the self-upgrade pipeline's kubectl rollout runs
// as its own step pod grant's ServiceAccount.
func TestWebPodManagerRoleGrantsNoWorkloadWrites(t *testing.T) {
	for _, chunk := range splitDocuments(render(t)) {
		var head renderedObject
		if err := yaml.Unmarshal([]byte(chunk), &head); err != nil || head.Kind != "Role" || head.Metadata.Name != "jb-concourse-jetbridge-pod-manager" {
			continue
		}
		role := &rbacv1.Role{}
		if err := yaml.UnmarshalStrict([]byte(chunk), role); err != nil {
			t.Fatalf("pod-manager Role does not decode: %v", err)
		}
		for _, rule := range role.Rules {
			if contains(rule.APIGroups, "apps") {
				t.Errorf("pod-manager Role grants apps resources: %+v", rule)
			}
		}
		if !slices.ContainsFunc(role.Rules, func(rule rbacv1.PolicyRule) bool {
			return contains(rule.Resources, "pods") && contains(rule.Verbs, "create")
		}) {
			t.Errorf("pod-manager Role no longer creates pods: %+v", role.Rules)
		}
		return
	}
	t.Fatal("render lacks the pod-manager Role")
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
	return slices.Equal(a, b)
}
