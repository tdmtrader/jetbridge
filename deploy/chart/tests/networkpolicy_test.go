package tests

import (
	"sort"
	"strings"
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

// A NetworkPolicy name is not decoration. Two policies with the same
// metadata.name in one namespace are ONE object, and which of the two survives
// is decided by the order `kubectl apply`/Helm happens to send them in --
// silently, with no error and no event. The loser's ingress and egress rules
// are simply not in the cluster.
//
// This is not hypothetical here. deploy/chart/templates/networkpolicy.yaml and
// deploy/chart/templates/artifact-daemon-networkpolicy.yaml both rendered
// `<fullname>-artifact-daemon`, and the two disagreed about which pods may
// reach the daemon: the copy in networkpolicy.yaml selected task pods by
// `concourse.ci/pipeline Exists`, which one-off builds and checks do not carry,
// while the dedicated template selects `concourse.ci/worker Exists`, which they
// do. So the collision was also a correctness fork -- whichever copy won
// decided whether a one-off build could fetch an artifact.
//
// The output plane adds four more workloads with four more policies, so the
// rule is written once here, over the WHOLE render rather than over one
// template, and it runs in every supported combination of the two independent
// NetworkPolicy switches.

// networkPolicyModes are the supported renders. The two switches are
// independent values with independent defaults, so the product is the surface.
func networkPolicyModes() []struct {
	name string
	sets []string
} {
	return []struct {
		name string
		sets []string
	}{
		{name: "defaults", sets: nil},
		{
			name: "cluster policies on",
			sets: []string{"networkPolicy.enabled=true"},
		},
		{
			name: "daemon policy on",
			sets: []string{"artifactDaemon.networkPolicy.enabled=true"},
		},
		{
			name: "both on",
			sets: []string{
				"networkPolicy.enabled=true",
				"artifactDaemon.networkPolicy.enabled=true",
			},
		},
		{
			name: "both on with task egress",
			sets: []string{
				"networkPolicy.enabled=true",
				"artifactDaemon.networkPolicy.enabled=true",
				"networkPolicy.taskEgressTo[0].ipBlock.cidr=10.0.0.0/8",
			},
		},
		{
			name: "both on with postgresql",
			sets: []string{
				"networkPolicy.enabled=true",
				"artifactDaemon.networkPolicy.enabled=true",
				"postgresql.enabled=true",
			},
		},
	}
}

// renderedObject is the little of a manifest this file needs.
type renderedObject struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
}

// splitDocuments splits a helm render into YAML documents, keeping the source
// comment line helm emits before each one so a failure can name the template.
func splitDocuments(out string) []string {
	var documents []string
	for _, chunk := range strings.Split(out, "\n---") {
		if strings.TrimSpace(chunk) == "" {
			continue
		}
		documents = append(documents, chunk)
	}

	return documents
}

// sourceOf reads the `# Source: chart/templates/x.yaml` line helm writes ahead
// of each document.
func sourceOf(document string) string {
	for _, line := range strings.Split(document, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "# Source:") {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, "# Source:"))
		}
	}

	return "(no # Source: line)"
}

// objectsOfKind returns every rendered object of one kind, with the template
// each came from.
func objectsOfKind(t *testing.T, out, kind string) map[string][]string {
	t.Helper()

	byName := map[string][]string{}
	for _, document := range splitDocuments(out) {
		var object renderedObject
		if err := yaml.Unmarshal([]byte(document), &object); err != nil {
			// Not every document is a single mapping (helm emits comments and
			// empty documents); a document that does not parse as an object
			// carries no name to collide.
			continue
		}
		if object.Kind != kind || object.Metadata.Name == "" {
			continue
		}
		byName[object.Metadata.Name] = append(byName[object.Metadata.Name], sourceOf(document))
	}

	return byName
}

func TestNetworkPolicyNamesAreUniqueInEverySupportedMode(t *testing.T) {
	scanned := 0

	for _, mode := range networkPolicyModes() {
		t.Run(mode.name, func(t *testing.T) {
			out := render(t, mode.sets...)
			policies := objectsOfKind(t, out, "NetworkPolicy")

			for name, sources := range policies {
				scanned++
				if len(sources) > 1 {
					t.Errorf("%d NetworkPolicies are all named %q, from %s.\n\n"+
						"One namespace holds ONE object per name: applying both leaves "+
						"whichever arrived last, with no error, and the other one's rules "+
						"are not in the cluster. Two policies that disagree about which "+
						"pods may reach a daemon therefore decide connectivity by apply "+
						"order.",
						len(sources), name, strings.Join(sources, " and "))
				}
			}
		})
	}

	// Vacuity floor. Every assertion above is over a map that is empty in a
	// render with no policies at all, and four of the six modes turn them on.
	if scanned < 4 {
		t.Fatalf("only %d NetworkPolicies were found across %d modes; the render or the "+
			"split failed and this rule would pass vacuously",
			scanned, len(networkPolicyModes()))
	}
}

// No policy in the render selects task pods by a label only some of them carry.
//
// `concourse.ci/worker` is set unconditionally on every pod the K8s runtime
// builds (atc/worker/jetbridge/container.go:856). `concourse.ci/pipeline` is
// added only when the value is non-empty, so a one-off build -- `fly execute`,
// with no pipeline -- carries the first and not the second. A policy selecting
// the second therefore silently does not apply to those pods, and what that
// means depends on which direction the policy runs in:
//
//   - the artifact-daemon INGRESS policy, selecting pipeline, refused a one-off
//     build its artifact fetch. That copy was deleted.
//   - the -task-egress policy, selecting pipeline, did the opposite and worse:
//     a pod selected by no NetworkPolicy is UNRESTRICTED, so a one-off build
//     escaped egress confinement entirely while the operator believed egress
//     was restricted. That is the same defect, in the policy whose whole job is
//     to confine task pods.
//
// So the rule is over the WHOLE render in every mode, not over one policy.
// Asserting the absence only from the daemon's policy is what let the second
// one survive the first fix.
func TestNoRenderedPolicySelectsTaskPodsByPipelineLabel(t *testing.T) {
	scanned, selectors := 0, 0

	for _, mode := range networkPolicyModes() {
		out := render(t, mode.sets...)

		for _, document := range splitDocuments(out) {
			var head renderedObject
			if err := yaml.Unmarshal([]byte(document), &head); err != nil {
				continue
			}
			if head.Kind != "NetworkPolicy" {
				continue
			}
			scanned++

			// Decoded, not substring-matched. The template explains this defect
			// in a YAML comment that helm renders into the output, so a
			// substring rule would fail on the explanation. The selector is
			// what the rule is about, so the selector is what it reads.
			var policy networkingv1.NetworkPolicy
			if err := yaml.UnmarshalStrict([]byte(document), &policy); err != nil {
				t.Errorf("in mode %q, NetworkPolicy %s does not decode: %v",
					mode.name, head.Metadata.Name, err)

				continue
			}

			for _, key := range selectorKeys(policy) {
				switch key {
				case "concourse.ci/worker":
					selectors++
				case "concourse.ci/pipeline":
					t.Errorf("in mode %q, NetworkPolicy %s selects by the pipeline label.\n\n"+
						"The runtime sets it only when the build HAS a pipeline, so a one-off "+
						"build's pod does not match. For an ingress policy that silently "+
						"refuses the pod; for an egress policy it silently EXEMPTS it, because "+
						"a pod selected by no NetworkPolicy is unrestricted. Select the worker "+
						"label, which every pod the runtime builds carries.",
						mode.name, head.Metadata.Name)
				}
			}
		}
	}

	if scanned < 4 {
		t.Fatalf("only %d NetworkPolicies were seen across %d modes; the render or the split "+
			"failed and this rule would pass vacuously", scanned, len(networkPolicyModes()))
	}
	if selectors < 2 {
		t.Fatalf("only %d rendered policies select by concourse.ci/worker. The task-pod "+
			"policies are the subject of this rule; a render where none of them selects a "+
			"worker pod is one where the label moved and this rule is guarding a string",
			selectors)
	}
}

// And the positive half for the daemon's own policy, which is the one a
// "restore the missing rule" change would be most likely to rewrite.
func TestTheArtifactDaemonPolicySelectsEveryWorkerPodAndNotOnlyPipelineOnes(t *testing.T) {
	out := render(t,
		"networkPolicy.enabled=true",
		"artifactDaemon.networkPolicy.enabled=true",
	)

	var daemonPolicy string
	for _, document := range splitDocuments(out) {
		var object renderedObject
		if err := yaml.Unmarshal([]byte(document), &object); err != nil {
			continue
		}
		if object.Kind == "NetworkPolicy" && strings.HasSuffix(object.Metadata.Name, "-artifact-daemon") {
			daemonPolicy = document
		}
	}
	if daemonPolicy == "" {
		t.Fatal("no artifact-daemon NetworkPolicy was rendered; this rule would pass vacuously")
	}

	if !strings.Contains(daemonPolicy, "concourse.ci/worker") {
		t.Error("the artifact-daemon NetworkPolicy does not select pods by concourse.ci/worker.\n\n" +
			"That is the label the K8s runtime puts on every task and check pod it builds. " +
			"concourse.ci/pipeline is not: a one-off build has no pipeline, so a policy " +
			"selecting it silently refuses those pods their artifact fetch.")
	}
}

// The consequence of deleting the duplicate, stated as a check rather than as a
// comment in a template.
//
// `networkPolicy.enabled` used to emit a daemon policy of its OWN, under the
// same name as the dedicated template's: one object in the namespace, and apply
// order decided which rules it carried. There is now exactly one template that
// emits it, and it emits it under EITHER switch -- because gating it on the new
// switch alone silently removed the policy from every install that had set only
// the cluster one. Both halves are asserted: one object, from one template, in
// every mode that produces it.
func TestTheDaemonPolicyComesFromExactlyOneTemplate(t *testing.T) {
	for _, mode := range []struct {
		name string
		sets []string
	}{
		{name: "cluster switch only", sets: []string{"networkPolicy.enabled=true"}},
		{name: "daemon switch only", sets: []string{"artifactDaemon.networkPolicy.enabled=true"}},
		{
			name: "both",
			sets: []string{"networkPolicy.enabled=true", "artifactDaemon.networkPolicy.enabled=true"},
		},
	} {
		t.Run(mode.name, func(t *testing.T) {
			out := render(t, mode.sets...)

			var sources []string
			for _, chunk := range splitDocuments(out) {
				var head renderedObject
				if err := yaml.Unmarshal([]byte(chunk), &head); err != nil {
					continue
				}
				if head.Kind != "NetworkPolicy" ||
					!strings.HasSuffix(head.Metadata.Name, "-artifact-daemon") {
					continue
				}
				sources = append(sources, sourceOf(chunk))
			}

			switch len(sources) {
			case 1:
				if !strings.HasSuffix(sources[0], "artifact-daemon-networkpolicy.yaml") {
					t.Errorf("the artifact daemon's NetworkPolicy came from %s; it has one "+
						"template, and a second one emitting the same name is one object "+
						"whose rules are decided by apply order", sources[0])
				}
			case 0:
				t.Errorf("no artifact-daemon NetworkPolicy was rendered. Either switch has "+
					"to render it: an install that set only %v HAS this policy today, and a "+
					"pod selected by none is unrestricted rather than default-deny", mode.sets)
			default:
				t.Errorf("%d artifact-daemon NetworkPolicies were rendered, from %v. Two "+
					"objects with one name are ONE object in the namespace, and which rules "+
					"it ends up with is decided by the order the apply happens to send them",
					len(sources), sources)
			}
		})
	}
}

// selectorKeys is every label key any selector in one NetworkPolicy matches on
// -- the pod selector itself and the pod selectors of every ingress and egress
// peer.
func selectorKeys(policy networkingv1.NetworkPolicy) []string {
	var keys []string

	collect := func(selector *metav1.LabelSelector) {
		if selector == nil {
			return
		}
		for key := range selector.MatchLabels {
			keys = append(keys, key)
		}
		for _, expression := range selector.MatchExpressions {
			keys = append(keys, expression.Key)
		}
	}

	collect(&policy.Spec.PodSelector)
	for _, rule := range policy.Spec.Ingress {
		for _, peer := range rule.From {
			collect(peer.PodSelector)
			collect(peer.NamespaceSelector)
		}
	}
	for _, rule := range policy.Spec.Egress {
		for _, peer := range rule.To {
			collect(peer.PodSelector)
			collect(peer.NamespaceSelector)
		}
	}

	return keys
}

// An upgrade does not take a running cluster's policies away.
//
// The de-duplication above was correct and its DEFAULT was not. At base,
// networkpolicy.yaml rendered `<fullname>-artifact-daemon` whenever
// `networkPolicy.enabled && artifactDaemon.enabled`, independent of the new
// `artifactDaemon.networkPolicy.enabled`, which defaults to FALSE. Deleting the
// duplicate therefore deleted the policy for the realistic existing
// configuration -- one that sets `networkPolicy.enabled` and has never heard of
// the new switch. A pod selected by no NetworkPolicy is unrestricted, so daemon
// port 7780 went from "web and pipeline pods" to "every pod in the namespace",
// silently; and this estate renders the chart live from the branch through
// ArgoCD, so with auto-prune the object is deleted and without it it lingers
// out of sync.
//
// This is the only finding in the whole review that can hurt a cluster that is
// already running, so the rule is the upgrade case itself: the values an
// existing install has are the values the policy has to survive.
func TestAnExistingInstallKeepsItsArtifactDaemonPolicyOnUpgrade(t *testing.T) {
	// Exactly what an existing install sets: the cluster switch, nothing else.
	// artifactDaemon.enabled is true by default and required for the K8s
	// runtime, so this is the whole of it.
	out := render(t, "networkPolicy.enabled=true")

	names := map[string]bool{}
	for _, chunk := range splitDocuments(out) {
		var head renderedObject
		if err := yaml.Unmarshal([]byte(chunk), &head); err != nil || head.Kind != "NetworkPolicy" {
			continue
		}
		names[head.Metadata.Name] = true
	}

	// Non-vacuity: the web policy is the one this switch has always rendered.
	if !names["jb-concourse-jetbridge-web"] {
		t.Fatalf("networkPolicy.enabled rendered no web policy; this render is not the one "+
			"this rule is about (found %v)", sortedNames(names))
	}
	if !names["jb-concourse-jetbridge-artifact-daemon"] {
		t.Errorf("networkPolicy.enabled=true renders no artifact-daemon NetworkPolicy "+
			"(found %v).\n\n"+
			"An install that has this switch set today HAS that policy. Removing it on "+
			"upgrade leaves the daemon selected by no policy at all, which is not a default "+
			"deny -- it is unrestricted: port 7780 goes from web and pipeline pods to every "+
			"pod in the namespace, with no error and no event.",
			sortedNames(names))
	}
}

func sortedNames(names map[string]bool) []string {
	var out []string
	for name := range names {
		out = append(out, name)
	}
	sort.Strings(out)

	return out
}

// The daemon's policy lists every port it admits, so a metrics listener the
// policy does not name is a scrape the policy drops: Prometheus reports the
// target down and nothing says the policy was the cause. Prometheus lives in
// another namespace under labels this chart cannot know, so the port is
// admitted from anywhere -- it answers /metrics and nothing else.
func TestTheDaemonPolicyAdmitsTheMetricsListener(t *testing.T) {
	out := render(t,
		"artifactDaemon.networkPolicy.enabled=true",
		"artifactDaemon.metrics.port=9392",
	)

	var policy *networkingv1.NetworkPolicy
	for _, document := range splitDocuments(out) {
		var candidate networkingv1.NetworkPolicy
		if err := yaml.Unmarshal([]byte(document), &candidate); err != nil {
			continue
		}
		if candidate.Kind == "NetworkPolicy" && strings.HasSuffix(candidate.Name, "-artifact-daemon") {
			policy = &candidate
		}
	}
	if policy == nil {
		t.Fatal("no artifact-daemon NetworkPolicy was rendered; this rule would pass vacuously")
	}

	for _, rule := range policy.Spec.Ingress {
		for _, port := range rule.Ports {
			if port.Port != nil && port.Port.IntValue() == 9392 {
				if len(rule.From) != 0 {
					t.Errorf("the metrics port is admitted only from %v; Prometheus runs in "+
						"its own namespace under labels this chart does not set", rule.From)
				}
				return
			}
		}
	}
	t.Error("artifactDaemon.metrics.port=9392 but the daemon's NetworkPolicy admits no " +
		"ingress on 9392: every scrape is dropped and the target reads as down")
}

// The web's policy lists the ports it admits, and the ATC's Prometheus
// listener was not one of them: with networkPolicy.enabled every scrape of the
// web was dropped, the target read as down, and every rule in the chart's
// PrometheusRule evaluated against no data. Prometheus runs in another
// namespace under labels this chart cannot know, so the port is admitted from
// anywhere -- the listener serves metrics and nothing else.
func TestTheWebPolicyAdmitsTheMetricsListener(t *testing.T) {
	// ingressFrom is set so that folding the metrics port into the UI's rule,
	// which it restricts, fails here rather than in a scrape.
	out := render(t,
		"networkPolicy.enabled=true",
		"networkPolicy.ingressFrom[0].podSelector.matchLabels.app=ingress",
		"metrics.enabled=true",
		"metrics.port=9391",
	)

	var policy *networkingv1.NetworkPolicy
	for _, document := range splitDocuments(out) {
		var candidate networkingv1.NetworkPolicy
		if err := yaml.Unmarshal([]byte(document), &candidate); err != nil {
			continue
		}
		if candidate.Kind == "NetworkPolicy" && strings.HasSuffix(candidate.Name, "-web") {
			policy = &candidate
		}
	}
	if policy == nil {
		t.Fatal("no web NetworkPolicy was rendered; this rule would pass vacuously")
	}

	for _, rule := range policy.Spec.Ingress {
		for _, port := range rule.Ports {
			if port.Port != nil && port.Port.IntValue() == 9391 {
				if len(rule.From) != 0 {
					t.Errorf("the metrics port is admitted only from %v; Prometheus runs in "+
						"its own namespace under labels this chart does not set", rule.From)
				}
				return
			}
		}
	}
	t.Error("metrics.port=9391 but the web's NetworkPolicy admits no ingress on 9391: " +
		"every scrape is dropped, the target reads as down, and every alerting rule " +
		"evaluates against no data")
}
