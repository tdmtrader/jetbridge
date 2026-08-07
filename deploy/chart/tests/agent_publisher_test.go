package tests

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func publisherSettings(overrides ...string) []string {
	settings := []string{
		"artifactDaemon.enabled=true",
		"artifactDaemon.tls.enabled=true",
		"agentSnapshots.enabled=true",
		"artifactDaemon.hangar.bucket=test-hangar",
		"agentPublisher.enabled=true",
		"agentPublisher.policySecret.name=publisher-policy",
		"agentPublisher.policySecret.key=policy.json",
		"agentPublisher.credentialSecret.name=publisher-credentials",
		"agentPublisher.credentials[0].reference=github-main",
		"agentPublisher.credentials[0].key=github-main",
		"agentPublisher.credentials[0].path=github-main",
		"agentPublisher.directGit.enabled=true",
		"agentPublisher.requestTimeout=17s",
		"agentPublisher.leaseDuration=4m",
	}
	return append(settings, overrides...)
}

func TestAgentPublisherIsDisabledByDefault(t *testing.T) {
	manifests := renderChart(t)
	if strings.Contains(manifests, "agent-publisher-policy") ||
		strings.Contains(manifests, "agent-publisher-credentials") {
		t.Fatal("default chart unexpectedly renders publisher Secret mounts")
	}
	web := findDeployment(t, manifests, "-web")
	for _, argument := range web.Spec.Template.Spec.Containers[0].Args {
		if strings.HasPrefix(argument, "--agent-publisher-") {
			t.Fatalf("default web args unexpectedly configure publisher: %v", web.Spec.Template.Spec.Containers[0].Args)
		}
	}
}

func TestDisabledAgentPublisherDoesNotRenderConfiguredSecrets(t *testing.T) {
	manifests := renderChart(t,
		"agentPublisher.enabled=false",
		"agentPublisher.policySecret.name=disabled-policy",
		"agentPublisher.credentialSecret.name=disabled-credentials",
		"agentPublisher.credentials[0].reference=disabled",
		"agentPublisher.credentials[0].key=disabled",
		"agentPublisher.credentials[0].path=disabled",
	)
	for _, forbidden := range []string{"disabled-policy", "disabled-credentials"} {
		if strings.Contains(manifests, forbidden) {
			t.Fatalf("disabled publisher Secret %q leaked into rendered manifests", forbidden)
		}
	}
}

func TestAgentPublisherRendersNarrowATCOwnedConfiguration(t *testing.T) {
	manifests := renderChart(t, publisherSettings()...)
	web := findDeployment(t, manifests, "-web")
	spec := web.Spec.Template.Spec
	args := spec.Containers[0].Args

	for _, want := range []string{
		"--agent-publisher-enabled",
		"--agent-publisher-policy-file=/run/concourse-publisher/policy/policy.json",
		"--agent-publisher-credential-root=/run/concourse-publisher/credentials",
		"--agent-publisher-credential-file=github-main:/run/concourse-publisher/credentials/github-main",
		"--agent-publisher-direct-git-enabled",
		"--agent-publisher-request-timeout=17s",
		"--agent-publisher-lease-duration=4m",
	} {
		if !slices.Contains(args, want) {
			t.Errorf("web args do not contain %q: %v", want, args)
		}
	}
	for _, removed := range []string{
		"--agent-publisher-gateway-",
		"publisher-gateway",
		"--agent-publisher-token",
		"--agent-publisher-ca-",
	} {
		for _, argument := range args {
			if strings.Contains(argument, removed) {
				t.Fatalf("obsolete or secret-bearing publisher argument %q was rendered", argument)
			}
		}
	}

	mounts := map[string]string{
		"agent-publisher-policy":      "/run/concourse-publisher/policy",
		"agent-publisher-credentials": "/run/concourse-publisher/credentials",
	}
	for name, path := range mounts {
		found := false
		for _, mount := range spec.Containers[0].VolumeMounts {
			if mount.Name == name && mount.MountPath == path && mount.ReadOnly {
				found = true
			}
		}
		if !found {
			t.Errorf("concourse-web lacks read-only %s mount at %s", name, path)
		}
		for _, init := range spec.InitContainers {
			for _, mount := range init.VolumeMounts {
				if mount.Name == name {
					t.Errorf("publisher Secret mount %q leaked into init container %q", name, init.Name)
				}
			}
		}
	}

	for _, want := range []string{
		"name: agent-publisher-policy",
		`secretName: "publisher-policy"`,
		"name: agent-publisher-credentials",
		`secretName: "publisher-credentials"`,
		"defaultMode: 288",
		`key: "policy.json"`,
		"path: policy.json",
		`key: "github-main"`,
		`path: "github-main"`,
	} {
		if !strings.Contains(manifests, want) {
			t.Errorf("rendered publisher manifest lacks %q", want)
		}
	}
}

func TestAgentPublisherRejectsIncompleteOrAmbiguousConfiguration(t *testing.T) {
	for name, test := range map[string]struct {
		settings []string
		want     string
	}{
		"requires snapshots": {
			settings: publisherSettings("agentSnapshots.enabled=false"),
			want:     "agentPublisher.enabled requires agentSnapshots.enabled",
		},
		"requires policy Secret": {
			settings: publisherSettings("agentPublisher.policySecret.name="),
			want:     "agentPublisher.policySecret.name is required",
		},
		"requires credential Secret": {
			settings: publisherSettings("agentPublisher.credentialSecret.name="),
			want:     "agentPublisher.credentialSecret.name is required",
		},
		"separates policy and credential Secrets": {
			settings: publisherSettings("agentPublisher.credentialSecret.name=publisher-policy"),
			want:     "policy and credential Secrets must be distinct",
		},
		"requires explicit adapter": {
			settings: publisherSettings("agentPublisher.directGit.enabled=false"),
			want:     "agentPublisher requires at least one publisher adapter",
		},
		"requires credential mapping": {
			settings: publisherSettings("agentPublisher.credentials=[]"),
			want:     "agentPublisher.credentials must contain at least one mapping",
		},
		"rejects duplicate destination": {
			settings: publisherSettings(
				"agentPublisher.credentials[1].reference=github-secondary",
				"agentPublisher.credentials[1].key=github-secondary",
				"agentPublisher.credentials[1].path=github-main",
			),
			want: "credential paths must be unique",
		},
		"rejects duplicate reference": {
			settings: publisherSettings(
				"agentPublisher.credentials[1].reference=github-main",
				"agentPublisher.credentials[1].key=github-secondary",
				"agentPublisher.credentials[1].path=github-secondary",
			),
			want: "credential references must be unique",
		},
		"rejects traversal": {
			settings: publisherSettings("agentPublisher.credentials[0].path=../token"),
			want:     "credential paths must be clean relative paths",
		},
		"protects managed args": {
			settings: []string{"web.extraArgs[0]=--agent-publisher-enabled"},
			want:     "web.extraArgs may not override chart-managed publisher configuration",
		},
		"rejects retired gateway values": {
			settings: []string{"agentPublisherGateway.enabled=false"},
			want:     "agentPublisherGateway has been removed",
		},
		"rejects unknown publisher fields": {
			settings: publisherSettings("agentPublisher.endpoint=https://gateway.example"),
			want:     "agentPublisher contains unsupported field",
		},
		"rejects retired pull request values": {
			settings: publisherSettings("agentPublisher.pullRequests.enabled=true"),
			want:     "agentPublisher contains unsupported field",
		},
	} {
		t.Run(name, func(t *testing.T) {
			output := renderChartFailure(t, test.settings...)
			if !strings.Contains(output, test.want) {
				t.Fatalf("Helm error does not contain %q:\n%s", test.want, output)
			}
		})
	}
}

func TestAgentPublisherRejectsSecretAliasesIntoOtherContainers(t *testing.T) {
	consumers := []struct {
		name     string
		settings func(string) []string
	}{
		{
			name: "direct extra volume",
			settings: func(secretName string) []string {
				return []string{
					"web.extraVolumes[0].name=publisher-alias",
					"web.extraVolumes[0].secret.secretName=" + secretName,
					"web.extraVolumeMounts[0].name=publisher-alias",
					"web.extraVolumeMounts[0].mountPath=/leaked-publisher-secret",
					"web.extraVolumeMounts[0].readOnly=true",
				}
			},
		},
		{
			name: "projected extra volume",
			settings: func(secretName string) []string {
				return []string{
					"web.extraVolumes[0].name=publisher-alias",
					"web.extraVolumes[0].projected.sources[0].secret.name=" + secretName,
					"web.extraVolumeMounts[0].name=publisher-alias",
					"web.extraVolumeMounts[0].mountPath=/leaked-publisher-secret",
					"web.extraVolumeMounts[0].readOnly=true",
				}
			},
		},
		{
			name: "database password",
			settings: func(secretName string) []string {
				return []string{
					"postgresql.existingSecret=" + secretName,
					"postgresql.passwordSecretKey=publisher-key",
				}
			},
		},
		{
			name: "artifact daemon TLS",
			settings: func(secretName string) []string {
				return []string{"artifactDaemon.tls.existingSecret=" + secretName}
			},
		},
		{
			name: "artifact daemon resolve capability",
			settings: func(secretName string) []string {
				return []string{"artifactDaemon.resolveCapability.existingSecret=" + secretName}
			},
		},
	}

	for _, secret := range []struct {
		name  string
		value string
		field string
	}{
		{name: "policy Secret", value: "publisher-policy", field: "agentPublisher.policySecret.name"},
		{name: "credential Secret", value: "publisher-credentials", field: "agentPublisher.credentialSecret.name"},
	} {
		for _, consumer := range consumers {
			t.Run(secret.name+"/"+consumer.name, func(t *testing.T) {
				output := renderChartFailure(t, publisherSettings(consumer.settings(secret.value)...)...)
				if !strings.Contains(output, "agentPublisher Secrets may only be consumed through chart-owned publisher mounts") {
					t.Fatalf("Helm error does not describe the publisher Secret isolation boundary:\n%s", output)
				}
			})
		}

		for name, generatedSecretName := range map[string]string{
			"generated artifact daemon TLS name":                "publisher-test-artifact-daemon-tls",
			"generated artifact daemon resolve capability name": "publisher-test-artifact-daemon-resolve-capability",
		} {
			t.Run(secret.name+"/"+name, func(t *testing.T) {
				output := renderChartFailure(t, publisherSettings(
					"fullnameOverride=publisher-test",
					secret.field+"="+generatedSecretName,
				)...)
				if !strings.Contains(output, "agentPublisher Secrets may only be consumed through chart-owned publisher mounts") {
					t.Fatalf("Helm error does not describe the publisher Secret isolation boundary:\n%s", output)
				}
			})
		}
	}
}

func TestAgentPublisherRejectsSecretAliasesThroughEveryTypedConsumer(t *testing.T) {
	consumers := []struct {
		name     string
		settings func(string) []string
	}{
		{
			name: "web environment",
			settings: func(secretName string) []string {
				return []string{
					"web.env[0].name=PUBLISHER_SECRET",
					"web.env[0].valueFrom.secretKeyRef.name=" + secretName,
					"web.env[0].valueFrom.secretKeyRef.key=publisher-key",
				}
			},
		},
		{
			name: "web pod image pull",
			settings: func(secretName string) []string {
				return []string{"image.pullSecrets[0]=" + secretName}
			},
		},
		{
			name: "task pod image pull",
			settings: func(secretName string) []string {
				return []string{"kubernetes.imagePullSecrets[0]=" + secretName}
			},
		},
		{
			name: "resource image pull",
			settings: func(secretName string) []string {
				return []string{"kubernetes.imageRegistrySecret=" + secretName}
			},
		},
		{
			name: "ingress TLS",
			settings: func(secretName string) []string {
				return []string{
					"ingress.enabled=true",
					"ingress.host=publisher.example",
					"ingress.tls[0].hosts[0]=publisher.example",
					"ingress.tls[0].secretName=" + secretName,
				}
			},
		},
	}

	for _, secret := range []struct {
		name  string
		value string
	}{
		{name: "policy Secret", value: "publisher-policy"},
		{name: "credential Secret", value: "publisher-credentials"},
	} {
		for _, consumer := range consumers {
			t.Run(secret.name+"/"+consumer.name, func(t *testing.T) {
				output := renderChartFailure(t, publisherSettings(consumer.settings(secret.value)...)...)
				if !strings.Contains(output, "agentPublisher Secrets may only be consumed through chart-owned publisher mounts") {
					t.Fatalf("Helm error does not describe the publisher Secret isolation boundary:\n%s", output)
				}
			})
		}
	}
}

func TestAgentPublisherRejectsSecretReuseByOtherWebSubsystems(t *testing.T) {
	consumers := []struct {
		name     string
		settings func(string) []string
	}{
		{
			name: "web TLS",
			settings: func(secretName string) []string {
				return []string{
					"web.tls.enabled=true",
					"web.tls.secretName=" + secretName,
				}
			},
		},
		{
			name: "web signing key",
			settings: func(secretName string) []string {
				return []string{
					"secrets.create=false",
					"secrets.signingKeySecret=" + secretName,
				}
			},
		},
	}

	for _, secret := range []struct {
		name  string
		value string
	}{
		{name: "policy Secret", value: "publisher-policy"},
		{name: "credential Secret", value: "publisher-credentials"},
	} {
		for _, consumer := range consumers {
			t.Run(secret.name+"/"+consumer.name, func(t *testing.T) {
				output := renderChartFailure(t, publisherSettings(consumer.settings(secret.value)...)...)
				if !strings.Contains(output, "agentPublisher Secrets are dedicated to publisher configuration") {
					t.Fatalf("Helm error does not describe the dedicated publisher Secret contract:\n%s", output)
				}
			})
		}
	}
}

func TestAgentPublisherRejectsGeneratedSigningSecretNameCollisions(t *testing.T) {
	for name, settings := range map[string][]string{
		"policy Secret": publisherSettings(
			"fullnameOverride=publisher-test",
			"agentPublisher.policySecret.name=publisher-test-keys",
		),
		"credential Secret": publisherSettings(
			"fullnameOverride=publisher-test",
			"agentPublisher.credentialSecret.name=publisher-test-keys",
		),
	} {
		t.Run(name, func(t *testing.T) {
			output := renderChartFailure(t, settings...)
			if !strings.Contains(output, "agentPublisher Secrets may not collide with chart-generated Secrets") {
				t.Fatalf("Helm error does not describe the generated Secret collision:\n%s", output)
			}
		})
	}
}

func TestAgentPublisherRejectsCredentialManagerNamespaceCollisions(t *testing.T) {
	for name, test := range map[string]struct {
		namespace string
		settings  []string
		want      string
	}{
		"empty namespace prefix": {
			namespace: "jetbridge-system",
			settings: publisherSettings(
				"kubernetes.credentialManager.enabled=true",
				"kubernetes.credentialManager.namespacePrefix=",
			),
			want: "requires a nonempty kubernetes.credentialManager.namespacePrefix",
		},
		"default prefix contains release namespace": {
			namespace: "concourse-main",
			settings: publisherSettings(
				"kubernetes.credentialManager.enabled=true",
			),
			want: "agentPublisher Secret namespace must be outside Kubernetes credential-manager team namespaces",
		},
		"custom prefix contains release namespace": {
			namespace: "teams-engineering",
			settings: publisherSettings(
				"kubernetes.credentialManager.enabled=true",
				"kubernetes.credentialManager.namespacePrefix=teams-",
			),
			want: "agentPublisher Secret namespace must be outside Kubernetes credential-manager team namespaces",
		},
	} {
		t.Run(name, func(t *testing.T) {
			output := renderChartFailureInNamespace(t, test.namespace, test.settings...)
			if !strings.Contains(output, test.want) {
				t.Fatalf("Helm error does not contain %q:\n%s", test.want, output)
			}
		})
	}
}

func TestAgentPublisherAllowsCredentialManagerInSeparateNamespaceTopology(t *testing.T) {
	renderChartInNamespace(t, "jetbridge-system", publisherSettings(
		"kubernetes.credentialManager.enabled=true",
		"kubernetes.credentialManager.namespacePrefix=concourse-",
	)...)
}

func TestAgentPublisherRejectsMalformedAuxiliaryValueShapes(t *testing.T) {
	for name, setting := range map[string]string{
		"extra volumes must be a list":          "web.extraVolumes=volume",
		"extra volume must be a map":            "web.extraVolumes[0]=volume",
		"extra mounts must be a list":           "web.extraVolumeMounts=mount",
		"extra mount must be a map":             "web.extraVolumeMounts[0]=mount",
		"web environment must be a list":        "web.env=environment",
		"web environment item must be a map":    "web.env[0]=environment",
		"environment valueFrom must be a map":   "web.env[0].valueFrom=environment",
		"environment Secret ref must be a map":  "web.env[0].valueFrom.secretKeyRef=publisher-secret",
		"web image pulls must be a list":        "image.pullSecrets=publisher-secret",
		"web image pull item must be a string":  "image.pullSecrets[0].name=publisher-secret",
		"task image pulls must be a list":       "kubernetes.imagePullSecrets=publisher-secret",
		"task image pull item must be a string": "kubernetes.imagePullSecrets[0].name=publisher-secret",
		"ingress TLS entries must be a list":    "ingress.tls=publisher-secret",
		"ingress TLS item must be a map":        "ingress.tls[0]=publisher-secret",
	} {
		t.Run(name, func(t *testing.T) {
			output := renderChartFailure(t, publisherSettings(setting)...)
			if !strings.Contains(output, "agentPublisher auxiliary chart values must be well-formed") {
				t.Fatalf("Helm error does not return the controlled auxiliary-value error:\n%s", output)
			}
			for _, rawTemplateError := range []string{
				"can't evaluate field",
				"range can't iterate",
				"wrong type for value",
			} {
				if strings.Contains(output, rawTemplateError) {
					t.Fatalf("Helm exposed raw template error %q:\n%s", rawTemplateError, output)
				}
			}
		})
	}
}

func TestAgentPublisherRejectsExtraMountsOverlappingItsTrustBoundary(t *testing.T) {
	for name, mountPath := range map[string]string{
		"filesystem root":       "/",
		"ancestor":              "/run",
		"publisher root":        "/run/concourse-publisher",
		"policy mount":          "/run/concourse-publisher/policy",
		"credential mount":      "/run/concourse-publisher/credentials",
		"credential descendant": "/run/concourse-publisher/credentials/github-main",
		"cleaned traversal":     "/run/concourse-publisher/../concourse-publisher",
	} {
		t.Run(name, func(t *testing.T) {
			output := renderChartFailure(t, publisherSettings(
				"web.extraVolumes[0].name=publisher-shadow",
				"web.extraVolumes[0].emptyDir={}",
				"web.extraVolumeMounts[0].name=publisher-shadow",
				"web.extraVolumeMounts[0].mountPath="+mountPath,
				"web.extraVolumeMounts[0].readOnly=true",
			)...)
			if !strings.Contains(output, "web.extraVolumeMounts may not overlap the agent publisher trust boundary") {
				t.Fatalf("Helm error does not describe the publisher mount boundary:\n%s", output)
			}
		})
	}
}

func TestAgentPublisherAllowsNonOverlappingExtraMount(t *testing.T) {
	renderChart(t, publisherSettings(
		"web.extraVolumes[0].name=unrelated",
		"web.extraVolumes[0].emptyDir={}",
		"web.extraVolumeMounts[0].name=unrelated",
		"web.extraVolumeMounts[0].mountPath=/run/concourse-publisher-unrelated",
		"web.extraVolumeMounts[0].readOnly=true",
	)...)
}

func TestAgentPublisherRejectsValuesOutsideKubernetesAndRuntimeBounds(t *testing.T) {
	pathComponentsOverLimit := make([]string, 17)
	for index := range pathComponentsOverLimit {
		pathComponentsOverLimit[index] = strings.Repeat("p", 241)
	}

	for name, test := range map[string]struct {
		setting string
		want    string
	}{
		"policy Secret name is not DNS-1123": {
			setting: "agentPublisher.policySecret.name=NOT_A_DNS_NAME",
			want:    "agentPublisher.policySecret.name must be a valid Kubernetes Secret name",
		},
		"credential Secret name has empty DNS label": {
			setting: "agentPublisher.credentialSecret.name=publisher..credentials",
			want:    "agentPublisher.credentialSecret.name must be a valid Kubernetes Secret name",
		},
		"Secret name exceeds 253 bytes": {
			setting: "agentPublisher.credentialSecret.name=" + strings.Repeat("c", 254),
			want:    "agentPublisher.credentialSecret.name must be a valid Kubernetes Secret name",
		},
		"policy Secret key exceeds 253 bytes": {
			setting: "agentPublisher.policySecret.key=" + strings.Repeat("k", 254),
			want:    "agentPublisher.policySecret.key is invalid",
		},
		"credential Secret key exceeds 253 bytes": {
			setting: "agentPublisher.credentials[0].key=" + strings.Repeat("k", 254),
			want:    "agentPublisher credential Secret keys are invalid",
		},
		"credential reference exceeds policy bound": {
			setting: "agentPublisher.credentials[0].reference=" + strings.Repeat("r", 257),
			want:    "agentPublisher credential references are invalid",
		},
		"credential path component exceeds filesystem bound": {
			setting: "agentPublisher.credentials[0].path=" + strings.Repeat("p", 256),
			want:    "agentPublisher credential paths must obey Kubernetes AtomicWriter bounds",
		},
		"credential path exceeds filesystem bound": {
			setting: "agentPublisher.credentials[0].path=" + strings.Join(pathComponentsOverLimit, "/"),
			want:    "agentPublisher credential paths must obey Kubernetes AtomicWriter bounds",
		},
	} {
		t.Run(name, func(t *testing.T) {
			output := renderChartFailure(t, publisherSettings(test.setting)...)
			if !strings.Contains(output, test.want) {
				t.Fatalf("Helm error does not contain %q:\n%s", test.want, output)
			}
		})
	}
}

func TestAgentPublisherAcceptsExactKubernetesAndRuntimeBounds(t *testing.T) {
	maximumSecretName := strings.Repeat("a", 253)
	maximumPathComponents := make([]string, 17)
	for index := range maximumPathComponents {
		maximumPathComponents[index] = strings.Repeat("p", 240)
	}

	for name, setting := range map[string]string{
		"Secret name":          "agentPublisher.policySecret.name=" + maximumSecretName,
		"Secret key":           "agentPublisher.policySecret.key=" + strings.Repeat("k", 253),
		"credential reference": "agentPublisher.credentials[0].reference=" + strings.Repeat("r", 256),
		"path component":       "agentPublisher.credentials[0].path=" + strings.Repeat("p", 255),
		"path":                 "agentPublisher.credentials[0].path=" + strings.Join(maximumPathComponents, "/"),
	} {
		t.Run(name, func(t *testing.T) {
			renderChart(t, publisherSettings(setting)...)
		})
	}
}

func TestAgentPublisherOperatorDocsDescribeTheATCOwnedSurface(t *testing.T) {
	chartReadme, err := os.ReadFile("../README.md")
	if err != nil {
		t.Fatal(err)
	}
	chartDocumentation := string(chartReadme)
	normalizedChartDocumentation := strings.Join(strings.Fields(chartDocumentation), " ")
	for _, forbidden := range []string{"agentPublisherGateway", "publisher gateway", "agentPublisher.pullRequests"} {
		if strings.Contains(chartDocumentation, forbidden) {
			t.Errorf("chart README still documents retired publisher topology %q", forbidden)
		}
	}
	for _, required := range []string{
		"agentPublisher.enabled",
		"agentPublisher.policySecret",
		"agentPublisher.credentialSecret",
		"agentPublisher.credentials",
		"agentPublisher.directGit.enabled",
		"Kubernetes AtomicWriter",
		"credential-manager namespace prefix must be nonempty",
		"<namespacePrefix><team>",
	} {
		if !strings.Contains(normalizedChartDocumentation, required) {
			t.Errorf("chart README does not document %q", required)
		}
	}

	agenticReadme, err := os.ReadFile("../../../docs/agentic/README.md")
	if err != nil {
		t.Fatal(err)
	}
	agenticDocumentation := string(agenticReadme)
	for _, forbidden := range []string{"rebasing/publishing directly", "It clones only"} {
		if strings.Contains(agenticDocumentation, forbidden) {
			t.Errorf("agentic README inaccurately claims %q", forbidden)
		}
	}
	for _, required := range []string{"already-rebased", "new private bare repository"} {
		if !strings.Contains(agenticDocumentation, required) {
			t.Errorf("agentic README does not describe %q", required)
		}
	}
}

func renderChartFailureInNamespace(t *testing.T, namespace string, sets ...string) string {
	t.Helper()
	output, err := renderChartInNamespaceCommand(t, namespace, sets...)
	if err == nil {
		t.Fatal("helm template unexpectedly accepted invalid publisher configuration")
	}
	return output
}

func renderChartInNamespace(t *testing.T, namespace string, sets ...string) string {
	t.Helper()
	output, err := renderChartInNamespaceCommand(t, namespace, sets...)
	if err != nil {
		t.Fatalf("helm template failed for valid publisher configuration: %v\n%s", err, output)
	}
	return output
}

func renderChartInNamespaceCommand(t *testing.T, namespace string, sets ...string) (string, error) {
	t.Helper()
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm not on PATH; skipping chart render test")
	}
	chartDir, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("resolve chart dir: %v", err)
	}
	args := []string{
		"template", "test-release", chartDir,
		"--namespace", namespace,
		"--set-string", "kubernetes.artifactHelperImage=" + testArtifactHelperImage,
	}
	for _, set := range sets {
		args = append(args, "--set", set)
	}
	output, err := exec.Command("helm", args...).CombinedOutput()
	return string(output), err
}
