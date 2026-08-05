package tests

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

type networkPolicy struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name        string            `json:"name"`
		Namespace   string            `json:"namespace"`
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
	Spec struct {
		PodSelector struct {
			MatchLabels      map[string]string `json:"matchLabels"`
			MatchExpressions []struct {
				Key      string   `json:"key"`
				Operator string   `json:"operator"`
				Values   []string `json:"values"`
			} `json:"matchExpressions"`
		} `json:"podSelector"`
		PolicyTypes []string `json:"policyTypes"`
		Ingress     []struct {
			From []struct {
				PodSelector struct {
					MatchLabels      map[string]string `json:"matchLabels"`
					MatchExpressions []struct {
						Key      string   `json:"key"`
						Operator string   `json:"operator"`
						Values   []string `json:"values"`
					} `json:"matchExpressions"`
				} `json:"podSelector"`
			} `json:"from"`
		} `json:"ingress"`
		Egress []struct {
			To []struct {
				IPBlock *struct {
					CIDR string `json:"cidr"`
				} `json:"ipBlock"`
				PodSelector *struct {
					MatchLabels map[string]string `json:"matchLabels"`
				} `json:"podSelector"`
				NamespaceSelector *struct {
					MatchLabels map[string]string `json:"matchLabels"`
				} `json:"namespaceSelector"`
			} `json:"to"`
			Ports []struct {
				Protocol string `json:"protocol"`
				Port     int    `json:"port"`
			} `json:"ports"`
		} `json:"egress"`
	} `json:"spec"`
}

func findNetworkPolicy(t *testing.T, manifests, nameSuffix string) networkPolicy {
	t.Helper()
	for _, doc := range strings.Split(manifests, "\n---") {
		if strings.TrimSpace(doc) == "" {
			continue
		}
		var policy networkPolicy
		if err := yaml.Unmarshal([]byte(doc), &policy); err != nil {
			continue
		}
		if policy.Kind == "NetworkPolicy" && strings.HasSuffix(policy.Metadata.Name, nameSuffix) {
			return policy
		}
	}
	t.Fatalf("no NetworkPolicy with name ending %q found in rendered chart", nameSuffix)
	return networkPolicy{}
}

func TestHermeticEgressPolicyIsDefaultOnAndFailClosed(t *testing.T) {
	policy := findNetworkPolicy(t, renderChart(t), "-hermetic-egress")

	if got := policy.Spec.PodSelector.MatchLabels["concourse.ci/hermetic"]; got != "true" {
		t.Fatalf("expected exact hermetic pod selector, got %q", got)
	}
	if !slices.Contains(policy.Spec.PolicyTypes, "Egress") ||
		!slices.Contains(policy.Spec.PolicyTypes, "Ingress") ||
		len(policy.Spec.PolicyTypes) != 2 {
		t.Fatalf("expected ingress and egress isolation, got %v", policy.Spec.PolicyTypes)
	}
	if len(policy.Spec.Egress) != 0 {
		t.Fatalf("default hermetic policy must have an empty allowlist, got %+v", policy.Spec.Egress)
	}
	if policy.Spec.Ingress == nil || len(policy.Spec.Ingress) != 0 {
		t.Fatalf("default hermetic policy must deny pod ingress, got %+v", policy.Spec.Ingress)
	}
}

func TestHermeticEgressPolicyAcceptsOnlyExplicitFullRules(t *testing.T) {
	policy := findNetworkPolicy(t, renderChart(t,
		"networkPolicy.hermeticEgressTo[0].to[0].ipBlock.cidr=203.0.113.10/32",
		"networkPolicy.hermeticEgressTo[0].ports[0].protocol=TCP",
		"networkPolicy.hermeticEgressTo[0].ports[0].port=443",
	), "-hermetic-egress")

	if len(policy.Spec.Egress) != 1 {
		t.Fatalf("expected one explicit hermetic egress rule, got %+v", policy.Spec.Egress)
	}
	rule := policy.Spec.Egress[0]
	if len(rule.To) != 1 || rule.To[0].IPBlock == nil || rule.To[0].IPBlock.CIDR != "203.0.113.10/32" {
		t.Fatalf("unexpected hermetic destination: %+v", rule.To)
	}
	if len(rule.Ports) != 1 || rule.Ports[0].Protocol != "TCP" || rule.Ports[0].Port != 443 {
		t.Fatalf("unexpected hermetic ports: %+v", rule.Ports)
	}
}

func TestGeneralTaskEgressPolicyExcludesHermeticPods(t *testing.T) {
	policy := findNetworkPolicy(t, renderChart(t,
		"networkPolicy.enabled=true",
		"networkPolicy.taskEgressTo[0].ipBlock.cidr=10.0.0.0/8",
	), "-task-egress")

	foundWorker := false
	foundHermeticExclusion := false
	for _, expression := range policy.Spec.PodSelector.MatchExpressions {
		if expression.Key == "concourse.ci/worker" && expression.Operator == "Exists" {
			foundWorker = true
		}
		if expression.Key == "concourse.ci/hermetic" &&
			expression.Operator == "NotIn" &&
			len(expression.Values) == 1 &&
			expression.Values[0] == "true" {
			foundHermeticExclusion = true
		}
	}
	if !foundWorker || !foundHermeticExclusion {
		t.Fatalf("general task policy selectors do not cover all runtime pods and exclude hermetic pods: %+v",
			policy.Spec.PodSelector.MatchExpressions)
	}
}

func TestArtifactDaemonNetworkPolicyHasOneIdentityUnderEveryToggleCombination(t *testing.T) {
	for _, test := range []struct {
		name         string
		global       bool
		daemon       bool
		wantPolicies int
		wantEgress   bool
	}{
		{name: "neither", wantPolicies: 0},
		{name: "global", global: true, wantPolicies: 1},
		{name: "daemon", daemon: true, wantPolicies: 1, wantEgress: true},
		{name: "both", global: true, daemon: true, wantPolicies: 1, wantEgress: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			manifests := renderChart(t,
				fmt.Sprintf("networkPolicy.enabled=%t", test.global),
				fmt.Sprintf("artifactDaemon.networkPolicy.enabled=%t", test.daemon),
			)
			identities := map[string]int{}
			daemonPolicies := 0
			for _, doc := range strings.Split(manifests, "\n---") {
				var policy networkPolicy
				if yaml.Unmarshal([]byte(doc), &policy) != nil || policy.Kind != "NetworkPolicy" {
					continue
				}
				identity := policy.APIVersion + "/" + policy.Kind + "/" +
					policy.Metadata.Namespace + "/" + policy.Metadata.Name
				identities[identity]++
				if strings.HasSuffix(policy.Metadata.Name, "-artifact-daemon") {
					daemonPolicies++
					hasEgress := slices.Contains(policy.Spec.PolicyTypes, "Egress")
					if hasEgress != test.wantEgress {
						t.Fatalf("artifact daemon egress policy type = %t, want %t: %v",
							hasEgress, test.wantEgress, policy.Spec.PolicyTypes)
					}
					foundWorker := false
					for _, ingress := range policy.Spec.Ingress {
						for _, from := range ingress.From {
							for _, expression := range from.PodSelector.MatchExpressions {
								foundWorker = foundWorker ||
									expression.Key == "concourse.ci/worker" &&
										expression.Operator == "Exists"
							}
						}
					}
					if !foundWorker {
						t.Fatalf("artifact daemon policy does not select actual runtime pods: %+v", policy.Spec)
					}
				}
			}
			for identity, count := range identities {
				if count != 1 {
					t.Fatalf("duplicate rendered identity %s appears %d times", identity, count)
				}
			}
			if daemonPolicies != test.wantPolicies {
				t.Fatalf("artifact daemon policies = %d, want %d", daemonPolicies, test.wantPolicies)
			}
		})
	}
}

// The GCP metadata endpoint was reachable only for the spot-preemption
// watcher, which went with the checkpoint subsystem. Nothing in the daemon
// dials it now, so the egress must stay closed under every values combination
// -- an open hole to the metadata service is a credential-theft path, not
// merely unused.
func TestArtifactDaemonEgressNeverAllowsMetadata(t *testing.T) {
	hasMetadataRule := func(policy networkPolicy) bool {
		for _, rule := range policy.Spec.Egress {
			if len(rule.To) != 1 || rule.To[0].IPBlock == nil ||
				rule.To[0].IPBlock.CIDR != "169.254.169.254/32" {
				continue
			}
			for _, port := range rule.Ports {
				if port.Protocol == "TCP" && port.Port == 80 {
					return true
				}
			}
		}
		return false
	}

	policy := findNetworkPolicy(t, renderChart(t,
		"artifactDaemon.networkPolicy.enabled=true",
	), "-artifact-daemon")
	if hasMetadataRule(policy) {
		t.Fatalf("artifact daemon egress must not reach the GCP metadata endpoint: %+v", policy.Spec.Egress)
	}
}

func TestArtifactDaemonEgressHasNoPortOnlyDNSOrKubernetesAPIRules(t *testing.T) {
	policy := findNetworkPolicy(t, renderChart(t,
		"artifactDaemon.networkPolicy.enabled=true",
	), "-artifact-daemon")

	for _, rule := range policy.Spec.Egress {
		for _, port := range rule.Ports {
			if (port.Port == 53 || port.Port == 443) && len(rule.To) == 0 {
				t.Fatalf("artifact daemon port %d egress must have an explicit destination: %+v",
					port.Port, rule)
			}
		}
	}
}

func TestArtifactDaemonEgressRendersExplicitDNSAndKubernetesAPIDestinations(t *testing.T) {
	policy := findNetworkPolicy(t, renderChart(t,
		"artifactDaemon.networkPolicy.enabled=true",
		"artifactDaemon.networkPolicy.dnsEgressTo[0].namespaceSelector.matchLabels.kubernetes\\.io/metadata\\.name=kube-system",
		"artifactDaemon.networkPolicy.dnsEgressTo[0].podSelector.matchLabels.k8s-app=kube-dns",
		"artifactDaemon.networkPolicy.kubernetesAPIEgressTo[0].ipBlock.cidr=10.96.0.1/32",
	), "-artifact-daemon")

	foundDNS := false
	foundAPI := false
	for _, rule := range policy.Spec.Egress {
		for _, port := range rule.Ports {
			switch port.Port {
			case 53:
				if len(rule.To) != 1 ||
					rule.To[0].NamespaceSelector == nil ||
					rule.To[0].PodSelector == nil ||
					rule.To[0].NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != "kube-system" ||
					rule.To[0].PodSelector.MatchLabels["k8s-app"] != "kube-dns" {
					t.Fatalf("DNS egress destination was not rendered exactly: %+v", rule.To)
				}
				foundDNS = true
			case 443:
				if len(rule.To) != 1 ||
					rule.To[0].IPBlock == nil ||
					rule.To[0].IPBlock.CIDR != "10.96.0.1/32" {
					t.Fatalf("Kubernetes API egress destination was not rendered exactly: %+v", rule.To)
				}
				foundAPI = true
			}
		}
	}
	if !foundDNS || !foundAPI {
		t.Fatalf("explicit daemon destinations are incomplete: dns=%t api=%t egress=%+v",
			foundDNS, foundAPI, policy.Spec.Egress)
	}
}

func TestAgentSnapshotHangarEgressIsNarrowAndRequired(t *testing.T) {
	missing := renderChartFailure(t,
		"artifactDaemon.networkPolicy.enabled=true",
		"artifactDaemon.tls.enabled=true",
		"agentSnapshots.enabled=true",
		"artifactDaemon.hangar.bucket=agent-snapshots",
	)
	if !strings.Contains(missing, "hangarEgressTo is required") {
		t.Fatalf("missing narrow Hangar egress failure: %s", missing)
	}

	policy := findNetworkPolicy(t, renderChart(t,
		"artifactDaemon.networkPolicy.enabled=true",
		"artifactDaemon.tls.enabled=true",
		"agentSnapshots.enabled=true",
		"artifactDaemon.hangar.bucket=agent-snapshots",
		"artifactDaemon.networkPolicy.hangarEgressTo[0].ipBlock.cidr=10.42.0.19/32",
	), "-artifact-daemon")
	for _, rule := range policy.Spec.Egress {
		if len(rule.Ports) != 1 || rule.Ports[0].Port != 443 || len(rule.To) != 1 ||
			rule.To[0].IPBlock == nil || rule.To[0].IPBlock.CIDR != "10.42.0.19/32" {
			continue
		}
		return
	}
	t.Fatalf("Hangar egress was not rendered as one explicit TCP/443 destination: %+v", policy.Spec.Egress)
}

func TestArtifactDaemonEgressRejectsInvalidOrAnyDestinationPeers(t *testing.T) {
	for name, sets := range map[string][]string{
		"dns peer without selector": {
			"artifactDaemon.networkPolicy.enabled=true",
			"artifactDaemon.networkPolicy.dnsEgressTo[0].unexpected=value",
		},
		"dns IPv4 all destinations": {
			"artifactDaemon.networkPolicy.enabled=true",
			"artifactDaemon.networkPolicy.dnsEgressTo[0].ipBlock.cidr=0.0.0.0/0",
		},
		"dns all destinations cannot be masked by a selector": {
			"artifactDaemon.networkPolicy.enabled=true",
			"artifactDaemon.networkPolicy.dnsEgressTo[0].ipBlock.cidr=0.0.0.0/0",
			"artifactDaemon.networkPolicy.dnsEgressTo[0].podSelector.matchLabels.k8s-app=kube-dns",
		},
		"API IPv6 all destinations": {
			"artifactDaemon.networkPolicy.enabled=true",
			"artifactDaemon.networkPolicy.kubernetesAPIEgressTo[0].ipBlock.cidr=0:0:0:0:0:0:0:0/0",
		},
	} {
		t.Run(name, func(t *testing.T) {
			output := renderChartFailure(t, sets...)
			if !strings.Contains(output, "must contain a destination-specific ipBlock, podSelector, or namespaceSelector") {
				t.Fatalf("unexpected chart validation error:\n%s", output)
			}
		})
	}
}

func TestKubernetesRuntimeNamespaceMustMatchReleaseNamespace(t *testing.T) {
	output := renderChartFailure(t, "kubernetes.namespace=jetbridge-tasks")
	if !strings.Contains(output, "kubernetes.namespace must match the Helm release namespace") {
		t.Fatalf("unexpected chart validation error:\n%s", output)
	}

	manifests := renderChart(t,
		"kubernetes.namespace=default",
		"networkPolicy.enabled=true",
		"networkPolicy.taskEgressTo[0].ipBlock.cidr=10.0.0.0/8",
	)
	for _, suffix := range []string{"-hermetic-egress", "-task-egress"} {
		policy := findNetworkPolicy(t, manifests, suffix)
		if policy.Metadata.Namespace != "default" {
			t.Fatalf("%s policy must select the release namespace, got %q",
				suffix, policy.Metadata.Namespace)
		}
	}
}
