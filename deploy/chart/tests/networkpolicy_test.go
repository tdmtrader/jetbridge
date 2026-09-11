package tests

import (
	"strings"
	"testing"

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

// The daemon policy has to select the task pods that actually exist.
//
// This is the other half of the collision: the copy that was deleted selected
// `concourse.ci/pipeline Exists`, and a one-off build's pod carries no
// pipeline. Asserting the surviving selector keeps the deletion from being
// reversed by a later "restore the missing rule" change that restores the
// wrong one.
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
	if strings.Contains(daemonPolicy, "concourse.ci/pipeline") {
		t.Errorf("the artifact-daemon NetworkPolicy selects pods by concourse.ci/pipeline, "+
			"which one-off builds and checks do not carry:\n\n%s", daemonPolicy)
	}
}

// The consequence of deleting the duplicate, stated as a check rather than as a
// comment in a template.
//
// `networkPolicy.enabled` used to emit a daemon policy as a side effect. It no
// longer does: the daemon's policy has one switch and one template. That is a
// visible change for an install that set only the cluster switch, so it is
// asserted in both directions -- the cluster switch alone emits no daemon
// policy, and the daemon switch alone does.
func TestTheDaemonPolicyHasExactlyOneSwitch(t *testing.T) {
	clusterOnly := objectsOfKind(t, render(t, "networkPolicy.enabled=true"), "NetworkPolicy")
	daemonPolicies := 0
	for name := range clusterOnly {
		if strings.HasSuffix(name, "-artifact-daemon") {
			daemonPolicies++
		}
	}
	if len(clusterOnly) == 0 {
		t.Fatal("networkPolicy.enabled=true rendered no NetworkPolicy at all; this rule " +
			"would pass vacuously")
	}
	if daemonPolicies != 0 {
		t.Errorf("networkPolicy.enabled=true rendered %d artifact-daemon NetworkPolicies. "+
			"The daemon's policy is artifactDaemon.networkPolicy.enabled's; emitting one "+
			"from the cluster switch too is how the name collision happened.", daemonPolicies)
	}

	daemonOnly := objectsOfKind(t, render(t, "artifactDaemon.networkPolicy.enabled=true"), "NetworkPolicy")
	found := false
	for name := range daemonOnly {
		if strings.HasSuffix(name, "-artifact-daemon") {
			found = true
		}
	}
	if !found {
		t.Error("artifactDaemon.networkPolicy.enabled=true rendered no artifact-daemon " +
			"NetworkPolicy; the daemon's only switch does not work")
	}
}
