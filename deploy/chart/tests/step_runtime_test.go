package tests

import (
	networkingv1 "k8s.io/api/networking/v1"
	"os/exec"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

// kubernetes.stepRuntime is a mode (ADR-0008): exec, today's pause pod the
// web execs the command into, or job, one suspended batch/v1 Job per step
// with the command in the pod spec. Only job is passed to the web: exec is the
// binary's default, and a flag an older web does not know would refuse to
// start it (the chart tracks core; the image lags it by one pipeline run).
// job adds the launcher image and the envelope URL the step pod fetches its
// request from, and the grants that runtime needs. pods/exec stays in both
// modes: a Run-owned exact execution still uses exec in this release.
//
// batch/jobs list and delete are granted in both modes: a web switched from
// job back to exec still has step Jobs to collect, and nothing else removes
// them. Creating or changing a Job stays job-only.

func TestTheDefaultStepRuntimeIsExecAndMayOnlyCollectStepJobs(t *testing.T) {
	out := render(t)
	for _, flag := range []string{"--kubernetes-step-runtime", "--kubernetes-launcher-image", "--kubernetes-step-envelope-url", "CONCOURSE_KUBERNETES_WEB_POD_NAME"} {
		if strings.Contains(out, flag) {
			t.Errorf("the default render passes %s", flag)
		}
	}
	role := podManagerRole(t, out)
	for _, verb := range []string{"list", "delete"} {
		if !rulesGrant(role.Rules, "batch", "jobs", verb) {
			t.Errorf("the exec runtime cannot %s leftover step Jobs: %+v", verb, role.Rules)
		}
	}
	for _, verb := range []string{"get", "watch", "create", "update", "patch"} {
		if rulesGrant(role.Rules, "batch", "jobs", verb) {
			t.Errorf("the exec runtime grants batch/jobs %s: %+v", verb, role.Rules)
		}
	}
	if !rulesGrant(role.Rules, "", "pods/exec", "create") {
		t.Errorf("the exec runtime lacks pods/exec create: %+v", role.Rules)
	}
	if grants(webClusterRole(t, out), "authentication.k8s.io", "tokenreviews", "create") {
		t.Error("the exec runtime grants tokenreviews create on the web ClusterRole")
	}
}

func TestTheJobStepRuntimeRendersItsFlagsAndGrants(t *testing.T) {
	out := renderInNamespace(t, "cicd", "kubernetes.stepRuntime=job")
	deployment := strictWebDeployment(t, out)
	var web *corev1.Container
	for i := range deployment.Spec.Template.Spec.Containers {
		if deployment.Spec.Template.Spec.Containers[i].Name == "concourse-web" {
			web = &deployment.Spec.Template.Spec.Containers[i]
		}
	}
	if web == nil {
		t.Fatal("the web Deployment has no concourse-web container")
	}
	args := strings.Join(web.Args, "\n")
	for _, want := range []string{
		"--kubernetes-step-runtime=job",
		"--kubernetes-step-envelope-url=http://jb-concourse-jetbridge-web.cicd.svc:8080/runtime/v1/step-envelope",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("web lacks %q", want)
		}
	}
	// The launcher image is not a chart value: a tag rendered here would lag
	// the image the pipeline's self-upgrade sets on the running pod. The web
	// reads its own pod instead, named by the downward API.
	if strings.Contains(args, "--kubernetes-launcher-image") {
		t.Error("the chart renders a launcher image; the web reads its own pod's")
	}
	fields := map[string]string{}
	for _, env := range web.Env {
		if env.ValueFrom != nil && env.ValueFrom.FieldRef != nil {
			fields[env.Name] = env.ValueFrom.FieldRef.FieldPath
		}
	}
	if fields["CONCOURSE_KUBERNETES_WEB_POD_NAME"] != "metadata.name" || fields["CONCOURSE_KUBERNETES_WEB_POD_NAMESPACE"] != "metadata.namespace" {
		t.Errorf("the web is not told its own pod: %v", fields)
	}

	role := podManagerRole(t, out)
	for _, verb := range []string{"get", "list", "watch", "create", "update", "patch", "delete"} {
		if !rulesGrant(role.Rules, "batch", "jobs", verb) {
			t.Errorf("the job runtime does not grant batch/jobs %s: %+v", verb, role.Rules)
		}
	}
	if !rulesGrant(role.Rules, "", "pods/exec", "create") {
		t.Errorf("the job runtime dropped pods/exec create, which a Run-owned exact execution still needs: %+v", role.Rules)
	}
	if !grants(webClusterRole(t, out), "authentication.k8s.io", "tokenreviews", "create") {
		t.Error("the job runtime does not grant tokenreviews create on the web ClusterRole")
	}
}

// With web TLS on, the step pod reaches the web over its HTTPS port.
// The envelope URL is the plain HTTP port even with web TLS on: the route is
// served there too, and a step pod trusts no CA the web's certificate needs.
func TestTheJobStepRuntimeEnvelopeURLStaysHTTPWithWebTLS(t *testing.T) {
	// An explicit namespace: without one helm takes the ambient namespace,
	// which in CI is the runner pod's.
	web := webDeployment(t, renderInNamespace(t, "cicd", "kubernetes.stepRuntime=job", "web.tls.enabled=true", "web.tls.bindPort=8443"))
	want := "- --kubernetes-step-envelope-url=http://jb-concourse-jetbridge-web.cicd.svc:8080/runtime/v1/step-envelope\n"
	if !strings.Contains(web, want) {
		t.Errorf("web lacks %q", strings.TrimSpace(want))
	}
	if strings.Contains(web, "step-envelope-url=https://") {
		t.Error("the envelope URL follows web TLS")
	}
}

// With the policies on, a step pod may reach the web's HTTP port for its
// envelope and the web admits it, in either namespace arrangement.
func TestTheJobStepRuntimeOpensTheEnvelopePathInBothNetworkPolicies(t *testing.T) {
	rendered := render(t, "kubernetes.stepRuntime=job", "networkPolicy.enabled=true",
		"networkPolicy.taskEgressTo[0].ipBlock.cidr=10.0.0.0/8")
	policies := networkPolicies(t, rendered)
	web, egress := policies["jb-concourse-jetbridge-web"], policies["jb-concourse-jetbridge-task-egress"]
	if web == nil || egress == nil {
		t.Fatalf("policies rendered: %v", policies)
	}
	admitsWorkers := false
	for _, rule := range web.Spec.Ingress {
		for _, from := range rule.From {
			if from.PodSelector != nil && len(from.PodSelector.MatchExpressions) == 1 && from.PodSelector.MatchExpressions[0].Key == "concourse.ci/worker" && from.NamespaceSelector != nil {
				admitsWorkers = true
			}
		}
	}
	if !admitsWorkers {
		t.Error("the web policy does not admit step pods on the envelope port")
	}
	reachesWeb := false
	for _, rule := range egress.Spec.Egress {
		for _, to := range rule.To {
			if to.PodSelector != nil && to.PodSelector.MatchLabels["app.kubernetes.io/component"] == "web" && to.NamespaceSelector != nil {
				reachesWeb = true
			}
		}
	}
	if !reachesWeb {
		t.Error("the task egress policy does not reach the web")
	}

	exec := networkPolicies(t, render(t, "networkPolicy.enabled=true", "networkPolicy.taskEgressTo[0].ipBlock.cidr=10.0.0.0/8"))
	if len(exec["jb-concourse-jetbridge-web"].Spec.Ingress) != len(web.Spec.Ingress)-1 {
		t.Error("the exec runtime renders the envelope ingress rule")
	}
}

func TestTheJobStepRuntimeNeedsNativeSidecars(t *testing.T) {
	out := renderError(t, "--set", "kubernetes.stepRuntime=job", "--kube-version", "1.28.0")
	if !strings.Contains(out, "1.29") {
		t.Fatalf("a 1.28 cluster renders the job runtime: %s", out)
	}
}

func TestAnUnknownStepRuntimeFailsTheRender(t *testing.T) {
	out := strings.ReplaceAll(renderHangarError(t, "kubernetes.stepRuntime=bogus"), "/", ".")
	if !strings.Contains(out, "kubernetes.stepRuntime") {
		t.Fatalf("the failure does not name kubernetes.stepRuntime: %s", out)
	}
}

func podManagerRole(t *testing.T, rendered string) *rbacv1.Role {
	t.Helper()

	const name = "jb-concourse-jetbridge-pod-manager"
	var found *rbacv1.Role
	for _, chunk := range splitDocuments(rendered) {
		var head renderedObject
		if err := yaml.Unmarshal([]byte(chunk), &head); err != nil || head.Kind != "Role" || head.Metadata.Name != name {
			continue
		}
		role := &rbacv1.Role{}
		if err := yaml.UnmarshalStrict([]byte(chunk), role); err != nil {
			t.Fatalf("pod-manager Role does not decode: %v", err)
		}
		if found != nil {
			t.Fatalf("render carries two Roles named %s", name)
		}
		found = role
	}
	if found == nil {
		t.Fatalf("render lacks Role %s", name)
	}
	return found
}

// rulesGrant is grants for a namespaced Role's rules.
func rulesGrant(rules []rbacv1.PolicyRule, group, resource, verb string) bool {
	for _, rule := range rules {
		if !contains(rule.APIGroups, group) || !contains(rule.Resources, resource) {
			continue
		}
		if contains(rule.Verbs, verb) || contains(rule.Verbs, "*") {
			return true
		}
	}
	return false
}

// networkPolicies decodes every NetworkPolicy in a render, by name.
func networkPolicies(t *testing.T, rendered string) map[string]*networkingv1.NetworkPolicy {
	t.Helper()
	policies := map[string]*networkingv1.NetworkPolicy{}
	for _, document := range splitDocuments(rendered) {
		var candidate networkingv1.NetworkPolicy
		if err := yaml.Unmarshal([]byte(document), &candidate); err != nil || candidate.Kind != "NetworkPolicy" {
			continue
		}
		policy := candidate
		policies[policy.Name] = &policy
	}
	return policies
}

// renderError renders with raw helm arguments and returns the failure text.
func renderError(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("helm", append([]string{"template", "jb", "deploy/chart", "-f", "deploy/chart/tests/testdata/required-values.yaml"}, args...)...)
	cmd.Dir = repoRoot(t)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("helm template %v succeeded, want a failure", args)
	}
	return string(out)
}
