package tests

import (
	"slices"
	"strings"
	"testing"
)

func TestAgentBrokerIsDisabledByDefault(t *testing.T) {
	manifests := renderChart(t)
	if strings.Contains(manifests, "agent-broker-config") ||
		strings.Contains(manifests, "agent-broker-egress") {
		t.Fatal("default chart unexpectedly renders agent broker resources")
	}
	web := findDeployment(t, manifests, "-web")
	for _, arg := range web.Spec.Template.Spec.Containers[0].Args {
		if strings.HasPrefix(arg, "--agent-child-executions-") {
			t.Fatalf("default web args unexpectedly enable agent broker: %v", web.Spec.Template.Spec.Containers[0].Args)
		}
	}
}

func TestAgentBrokerRendersATCConfigurationWithoutAStandaloneWorkload(t *testing.T) {
	manifests := renderChart(t, agentBrokerSettings()...)
	for _, want := range []string{
		"agent-broker-config",
		`adapter_binaries`,
		`/usr/local/bin/cursor-agent`,
		`output_schemas`,
		`/opt/concourse/agent-broker/schemas/review-body.v1.json`,
		`provider-credentials`,
		`registry.example/agent-broker@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb`,
		`0.146.0`,
		`tests_unavailable`,
		`checksum/agent-broker-config:`,
	} {
		if !strings.Contains(manifests, want) {
			t.Errorf("rendered broker configuration is missing %q", want)
		}
	}
	web := findDeployment(t, manifests, "-web")
	args := web.Spec.Template.Spec.Containers[0].Args
	for _, want := range []string{
		"--agent-child-executions-enabled",
		"--agent-child-executions-broker-catalog=/run/concourse-agent-broker-config/catalog.json",
		"--agent-child-executions-broker-runtime=/run/concourse-agent-broker-config/runtime.json",
		"--agent-child-executions-capability-key=/run/concourse-agent-broker-capability/capability.key",
	} {
		if !slices.Contains(args, want) {
			t.Errorf("web args do not contain %q: %v", want, args)
		}
	}
	if strings.Contains(manifests, "kind: Deployment\nmetadata:\n  name: test-release-concourse-agent-broker") ||
		strings.Contains(manifests, "kind: Service\nmetadata:\n  name: test-release-concourse-agent-broker") ||
		strings.Contains(manifests, "kind: DaemonSet\nmetadata:\n  name: test-release-concourse-agent-broker") {
		t.Fatal("chart rendered a standalone broker workload; broker must remain a managed pod companion")
	}
}

func TestAgentBrokerNetworkPolicySelectsWholeManagedPodAndUsesExplicitEgress(t *testing.T) {
	settings := append(agentBrokerSettings(),
		"agentBroker.networkPolicy.egress[0].to[0].ipBlock.cidr=203.0.113.10/32",
		"agentBroker.networkPolicy.egress[0].ports[0].protocol=TCP",
		"agentBroker.networkPolicy.egress[0].ports[0].port=443",
	)
	policy := findNetworkPolicy(t, renderChart(t, settings...), "-agent-broker-egress")
	if policy.Spec.PodSelector.MatchLabels["concourse.ci/agent-broker"] != "true" {
		t.Fatalf("agent broker policy selector = %+v", policy.Spec.PodSelector)
	}
	if len(policy.Spec.Egress) != 1 || len(policy.Spec.Egress[0].To) != 1 ||
		policy.Spec.Egress[0].To[0].IPBlock == nil ||
		policy.Spec.Egress[0].To[0].IPBlock.CIDR != "203.0.113.10/32" {
		t.Fatalf("agent broker explicit egress = %+v", policy.Spec.Egress)
	}
}

func TestAgentBrokerNetworkPolicyDoesNotClaimContainerLevelOrIndependentIsolation(t *testing.T) {
	manifests := renderChart(t, agentBrokerSettings()...)
	for _, want := range []string{
		"applies to the complete managed task pod",
		"Same-pod loopback remains outside NetworkPolicy enforcement",
	} {
		if !strings.Contains(manifests, want) {
			t.Errorf("rendered policy does not disclose whole-pod semantics %q", want)
		}
	}
}

func TestAgentBrokerRejectsMutableOrIncompleteOperatorConfiguration(t *testing.T) {
	for _, test := range []struct {
		name string
		sets []string
		want string
	}{
		{
			name: "mutable image",
			sets: append(agentBrokerSettings(),
				"agentBroker.image=registry.example/agent-broker:latest"),
			want: "agentBroker.image must be an exact",
		},
		{
			name: "no snapshots",
			sets: append(agentBrokerSettings(), "agentSnapshots.enabled=false"),
			want: "agentBroker.enabled requires agentSnapshots.enabled",
		},
		{
			name: "missing profiles",
			sets: append(agentBrokerSettings(), "agentBroker.profiles={}"),
			want: "agentBroker.profiles must contain",
		},
		{
			name: "missing credentials",
			sets: append(agentBrokerSettings(), "agentBroker.credentials={}"),
			want: "agentBroker.credentials must contain",
		},
		{
			name: "capability and provider secret collision",
			sets: append(agentBrokerSettings(),
				"agentBroker.capabilitySecret.name=provider-credentials"),
			want: "agentBroker capability Secret must be distinct",
		},
		{
			name: "duplicate credential slots",
			sets: append(agentBrokerSettings(),
				"agentBroker.credentials[1].slot=shared",
				"agentBroker.credentials[1].secretName=second-provider-credentials",
				"agentBroker.credentials[1].key=api-key"),
			want: "agentBroker credential slots must be unique",
		},
		{
			name: "extra args override",
			sets: append(agentBrokerSettings(),
				"web.extraArgs[0]=--agent-child-executions-capability-key=/tmp/key"),
			want: "web.extraArgs may not override chart-managed agent broker configuration",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			output := renderChartFailure(t, test.sets...)
			if !strings.Contains(output, test.want) {
				t.Fatalf("render failure = %s, want %q", output, test.want)
			}
		})
	}
}

func agentBrokerSettings() []string {
	return []string{
		"artifactDaemon.tls.enabled=true",
		"artifactDaemon.hangar.bucket=test-hangar",
		"agentSnapshots.enabled=true",
		"agentBroker.enabled=true",
		"agentBroker.image=registry.example/agent-broker@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"agentBroker.authorityEndpoint=https://concourse-web.default.svc:8080",
		"agentBroker.capabilitySecret.name=agent-child-capability",
		"agentBroker.capabilitySecret.key=capability.key",
		"agentBroker.credentials[0].slot=shared",
		"agentBroker.credentials[0].secretName=provider-credentials",
		"agentBroker.credentials[0].key=api-key",
		"agentBroker.profiles[0].id=balanced-review-high",
		"agentBroker.profiles[0].revision=1",
		"agentBroker.profiles[0].selector.tier=balanced",
		"agentBroker.profiles[0].selector.effort=high",
		"agentBroker.profiles[0].tools[0]=request_review",
		"agentBroker.profiles[0].purpose=static code review",
		"agentBroker.profiles[0].worker_image=registry.example/agent-broker@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"agentBroker.profiles[0].adapter.name=codex",
		"agentBroker.profiles[0].adapter.version=0.146.0",
		"agentBroker.profiles[0].provider.name=openai",
		"agentBroker.profiles[0].provider.model=exact-model",
		"agentBroker.profiles[0].native_effort=high",
		"agentBroker.profiles[0].instructions_digest=sha256:9982a935820d5177131cf16e285ab137b0774a0d4701181aaf180358a3a6f669",
		"agentBroker.profiles[0].credential_slot=shared",
		"agentBroker.profiles[0].limits.timeout=60000000000",
		"agentBroker.profiles[0].limits.max_input_bytes=1048576",
		"agentBroker.profiles[0].limits.max_output_bytes=1048576",
		"agentBroker.profiles[0].controls.read_only_workspace=true",
		"agentBroker.profiles[0].controls.no_broker_recursion=true",
		"agentBroker.profiles[0].controls.tests_unavailable=true",
		"agentBroker.profiles[0].controls.native_output_schema=true",
		"agentBroker.profiles[0].controls.ignores_user_config=true",
	}
}
