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

// A task step's params reach its pod through a Secret of its own,
// step-env-<handle>, which web creates, points at its owner and deletes. The
// rule is the same in every permutation: a Run-owned exact execution runs on
// exec under either step runtime, and the credential manager has nothing to
// do with it. The Role grants no get, list or watch on Secrets, so web
// cannot enumerate them; patch and delete still reach any Secret it can name
// in the step namespace (a patch returns the Secret's body), which is why the
// rule stays exactly these three verbs.
func TestWebPodManagerRoleWritesButNeverReadsSecrets(t *testing.T) {
	cases := []struct {
		name string
		sets []string
	}{
		{"default", nil},
		{"job step runtime", []string{"kubernetes.stepRuntime=job"}},
		{"credential manager", []string{"kubernetes.credentialManager.enabled=true"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			role := podManagerRole(t, render(t, tc.sets...))
			for _, verb := range []string{"create", "patch", "delete"} {
				if !rulesGrant(role.Rules, "", "secrets", verb) {
					t.Errorf("pod-manager Role lacks secrets %s: %+v", verb, role.Rules)
				}
			}
			for _, verb := range []string{"get", "list", "watch"} {
				if rulesGrant(role.Rules, "", "secrets", verb) {
					t.Errorf("pod-manager Role grants secrets %s: %+v", verb, role.Rules)
				}
			}
		})
	}
}

// Web creates, execs into, watches and deletes step pods, step Jobs and their
// params Secrets in the step namespace (kubernetes.namespace), so the
// pod-manager Role and its binding are rendered there. What web still reads
// in the release namespace, where the artifact daemon runs -- its
// EndpointSlices, and under the job step runtime web's own pod -- is granted
// by a Role there. With the two namespaces equal, the pod-manager Role holds
// both and nothing else is rendered.
func TestWebPodManagerRoleFollowsTheStepNamespace(t *testing.T) {
	t.Run("split", func(t *testing.T) {
		out := renderInNamespace(t, "jbns", "kubernetes.namespace=tasks", "kubernetes.stepRuntime=job")
		roles := namespacedRoles(t, out)
		bindings := namespacedRoleBindings(t, out)

		pm, ok := roles["jb-concourse-jetbridge-pod-manager"]
		if !ok {
			t.Fatal("render lacks the pod-manager Role")
		}
		if pm.Namespace != "tasks" {
			t.Errorf("pod-manager Role in namespace %q, want the step namespace %q", pm.Namespace, "tasks")
		}
		for _, want := range []struct{ resource, verb string }{{"pods", "create"}, {"pods/exec", "create"}, {"secrets", "create"}} {
			if !rulesGrant(pm.Rules, "", want.resource, want.verb) {
				t.Errorf("pod-manager Role lacks %s %s: %+v", want.resource, want.verb, pm.Rules)
			}
		}
		if !rulesGrant(pm.Rules, "batch", "jobs", "create") {
			t.Errorf("pod-manager Role lacks batch/jobs create under job: %+v", pm.Rules)
		}
		pmb, ok := bindings["jb-concourse-jetbridge-pod-manager"]
		if !ok || pmb.Namespace != "tasks" || pmb.RoleRef.Name != pm.Name {
			t.Errorf("pod-manager RoleBinding = %+v, want one in %q bound to the pod-manager Role", pmb, "tasks")
		} else if len(pmb.Subjects) != 1 || pmb.Subjects[0].Namespace != "jbns" {
			t.Errorf("pod-manager RoleBinding subjects = %+v, want the web ServiceAccount in %q", pmb.Subjects, "jbns")
		}

		rel, ok := roles["jb-concourse-jetbridge-web-release"]
		if !ok {
			t.Fatal("render lacks the release-namespace Role for web")
		}
		if rel.Namespace != "jbns" {
			t.Errorf("release-namespace Role in %q, want %q", rel.Namespace, "jbns")
		}
		if !rulesGrant(rel.Rules, "discovery.k8s.io", "endpointslices", "list") {
			t.Errorf("release-namespace Role cannot list the daemon's EndpointSlices: %+v", rel.Rules)
		}
		if !rulesGrant(rel.Rules, "", "pods", "get") {
			t.Errorf("release-namespace Role cannot read web's own pod under job: %+v", rel.Rules)
		}
		for _, verb := range []string{"create", "delete", "list"} {
			if rulesGrant(rel.Rules, "", "pods", verb) {
				t.Errorf("release-namespace Role grants pods %s: %+v", verb, rel.Rules)
			}
		}
		if rulesGrant(rel.Rules, "", "secrets", "create") || rulesGrant(rel.Rules, "", "secrets", "get") {
			t.Errorf("release-namespace Role touches Secrets: %+v", rel.Rules)
		}
		if b, ok := bindings["jb-concourse-jetbridge-web-release"]; !ok || b.Namespace != "jbns" || b.RoleRef.Name != rel.Name {
			t.Errorf("release-namespace RoleBinding = %+v", b)
		}
	})

	t.Run("same namespace", func(t *testing.T) {
		out := renderInNamespace(t, "jbns")
		roles := namespacedRoles(t, out)
		if _, ok := roles["jb-concourse-jetbridge-web-release"]; ok {
			t.Error("a second web Role is rendered when the step namespace is the release namespace")
		}
		pm := roles["jb-concourse-jetbridge-pod-manager"]
		if pm == nil || pm.Namespace != "jbns" {
			t.Fatalf("pod-manager Role = %+v, want one in %q", pm, "jbns")
		}
		if !rulesGrant(pm.Rules, "discovery.k8s.io", "endpointslices", "list") {
			t.Errorf("pod-manager Role cannot list the daemon's EndpointSlices: %+v", pm.Rules)
		}
	})
}

func namespacedRoles(t *testing.T, rendered string) map[string]*rbacv1.Role {
	t.Helper()
	roles := map[string]*rbacv1.Role{}
	for _, chunk := range splitDocuments(rendered) {
		var head renderedObject
		if err := yaml.Unmarshal([]byte(chunk), &head); err != nil || head.Kind != "Role" {
			continue
		}
		role := &rbacv1.Role{}
		if err := yaml.UnmarshalStrict([]byte(chunk), role); err != nil {
			t.Fatalf("Role %s does not decode: %v", head.Metadata.Name, err)
		}
		roles[role.Name] = role
	}
	return roles
}

func namespacedRoleBindings(t *testing.T, rendered string) map[string]*rbacv1.RoleBinding {
	t.Helper()
	bindings := map[string]*rbacv1.RoleBinding{}
	for _, chunk := range splitDocuments(rendered) {
		var head renderedObject
		if err := yaml.Unmarshal([]byte(chunk), &head); err != nil || head.Kind != "RoleBinding" {
			continue
		}
		binding := &rbacv1.RoleBinding{}
		if err := yaml.UnmarshalStrict([]byte(chunk), binding); err != nil {
			t.Fatalf("RoleBinding %s does not decode: %v", head.Metadata.Name, err)
		}
		bindings[binding.Name] = binding
	}
	return bindings
}
