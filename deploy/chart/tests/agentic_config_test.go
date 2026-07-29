package tests

import (
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

func TestAgentSnapshotsAreDisabledByDefault(t *testing.T) {
	web := findDeployment(t, renderChart(t), "-web")
	spec := web.Spec.Template.Spec
	args := spec.Containers[0].Args
	if slices.Contains(args, "--agent-snapshot-enabled") {
		t.Fatalf("default web args unexpectedly enable durable agent snapshots: %v", args)
	}
	for _, volume := range spec.Volumes {
		if volume.Name == "snapshot-scratch" {
			t.Fatal("default web pod unexpectedly contains snapshot scratch")
		}
	}
	for _, init := range spec.InitContainers {
		if init.Name == "prepare-snapshot-scratch" {
			t.Fatal("default web pod unexpectedly prepares snapshot scratch")
		}
	}
	for _, env := range spec.Containers[0].Env {
		if env.Name == "TMPDIR" {
			t.Fatal("default web pod unexpectedly overrides TMPDIR")
		}
	}
}

func TestArtifactHelperDigestIsForwardedExactly(t *testing.T) {
	web := findDeployment(t, renderChart(t), "-web")
	want := "--kubernetes-artifact-helper-image=" + testArtifactHelperImage
	if !slices.Contains(web.Spec.Template.Spec.Containers[0].Args, want) {
		t.Fatalf("web args do not contain immutable helper %q", want)
	}
}

func TestAgentSnapshotValuesRenderExactWebArguments(t *testing.T) {
	web := findDeployment(t, renderChart(t,
		"artifactDaemon.enabled=true",
		"artifactDaemon.tls.enabled=true",
		"agentSnapshots.enabled=true",
		"artifactDaemon.hangar.bucket=test-hangar",
		"agentSnapshots.replicationFactor=3",
		"agentSnapshots.maxBytes=1048576",
		"agentSnapshots.maxFiles=1234",
		"agentSnapshots.bindingRetention=24h",
		"agentSnapshots.orphanGracePeriod=20m",
		"agentSnapshots.gcInterval=45s",
		"agentSnapshots.repairInterval=2m",
	), "-web")
	args := web.Spec.Template.Spec.Containers[0].Args

	for _, want := range []string{
		"--agent-snapshot-enabled",
		"--agent-snapshot-replication-factor=3",
		"--agent-snapshot-max-bytes=1048576",
		"--agent-snapshot-max-files=1234",
		"--agent-snapshot-binding-retention=24h",
		"--agent-snapshot-orphan-grace-period=20m",
		"--agent-snapshot-gc-interval=45s",
		"--agent-snapshot-repair-interval=2m",
		"--agent-snapshot-temp-dir=/var/concourse/snapshot-scratch/web",
	} {
		if !slices.Contains(args, want) {
			t.Errorf("web args do not contain %q: %v", want, args)
		}
	}

	foundScratchMount := false
	for _, mount := range web.Spec.Template.Spec.Containers[0].VolumeMounts {
		if mount.Name == "snapshot-scratch" &&
			mount.MountPath == "/var/concourse/snapshot-scratch" &&
			!mount.ReadOnly {
			foundScratchMount = true
		}
	}
	if !foundScratchMount {
		t.Fatal("disk-backed snapshot scratch is not mounted read-write into concourse-web")
	}
	foundTMPDIR := false
	for _, env := range web.Spec.Template.Spec.Containers[0].Env {
		if env.Name == "TMPDIR" && env.Value == "/var/concourse/snapshot-scratch/web" {
			foundTMPDIR = true
		}
	}
	if !foundTMPDIR {
		t.Fatal("concourse-web TMPDIR does not use disk-backed snapshot scratch")
	}
	foundScratchInit := false
	for _, init := range web.Spec.Template.Spec.InitContainers {
		for _, mount := range init.VolumeMounts {
			if mount.Name == "snapshot-scratch" {
				if init.Name != "prepare-snapshot-scratch" ||
					mount.MountPath != "/var/concourse/snapshot-scratch" {
					t.Fatalf("snapshot scratch leaked into unrelated init container %q", init.Name)
				}
				foundScratchInit = true
				command := strings.Join(init.Command, " ")
				if !strings.Contains(command, "mkdir -p /var/concourse/snapshot-scratch/web") ||
					!strings.Contains(command, "chmod 0700 /var/concourse/snapshot-scratch/web") {
					t.Fatalf("snapshot scratch init does not create a private child: %v", init.Command)
				}
			}
		}
	}
	if !foundScratchInit {
		t.Fatal("non-root snapshot scratch preparation init container is missing")
	}
	var scratch *podVolume
	for index := range web.Spec.Template.Spec.Volumes {
		if web.Spec.Template.Spec.Volumes[index].Name == "snapshot-scratch" {
			scratch = &web.Spec.Template.Spec.Volumes[index]
		}
	}
	if scratch == nil || scratch.EmptyDir == nil {
		t.Fatal("default snapshot scratch is not an emptyDir")
	}
	if scratch.EmptyDir.Medium == "Memory" {
		t.Fatal("snapshot scratch must be disk-backed, not tmpfs")
	}
	if scratch.EmptyDir.SizeLimit != "80Gi" {
		t.Fatalf("snapshot scratch sizeLimit = %q, want 80Gi", scratch.EmptyDir.SizeLimit)
	}
}

func TestAgentSnapshotsRequireHangarBucketAndWireDaemonGCSArguments(t *testing.T) {
	output := renderChartFailure(t,
		"artifactDaemon.enabled=true",
		"artifactDaemon.tls.enabled=true",
		"agentSnapshots.enabled=true",
	)
	if !strings.Contains(output, "artifactDaemon.hangar.bucket is required") {
		t.Fatalf("missing Hangar bucket failure: %s", output)
	}

	daemon := findDaemonSet(t, renderChart(t,
		"artifactDaemon.enabled=true",
		"artifactDaemon.tls.enabled=true",
		"agentSnapshots.enabled=true",
		"artifactDaemon.hangar.bucket=agent-snapshots",
		"artifactDaemon.hangar.endpoint=https://fake-gcs.ci.example/storage/v1/",
		"artifactDaemon.hangar.scratchPath=/var/concourse/hangar-scratch",
		"artifactDaemon.hangar.readTimeout=3m",
		"artifactDaemon.hangar.writeTimeout=4m",
		"artifactDaemon.serviceAccount.annotations.iam\\.gke\\.io/gcp-service-account=hangar@project.iam.gserviceaccount.com",
	), "-artifact-daemon")
	args := daemon.Spec.Template.Spec.Containers[0].Command
	for _, want := range []string{
		"--hangar-gcs-bucket=agent-snapshots",
		"--hangar-gcs-endpoint=https://fake-gcs.ci.example/storage/v1/",
		"--hangar-scratch-dir=/var/concourse/hangar-scratch",
		"--hangar-read-timeout=3m",
		"--hangar-write-timeout=4m",
	} {
		if !slices.Contains(args, want) {
			t.Errorf("artifact daemon command does not contain %q: %v", want, args)
		}
	}
	if !strings.Contains(renderChart(t,
		"artifactDaemon.enabled=true",
		"artifactDaemon.serviceAccount.annotations.iam\\.gke\\.io/gcp-service-account=hangar@project.iam.gserviceaccount.com",
	), "iam.gke.io/gcp-service-account: hangar@project.iam.gserviceaccount.com") {
		t.Fatal("daemon workload-identity annotation was not rendered")
	}
}

func TestHangarSnapshotFailureAlertUsesDaemonMetrics(t *testing.T) {
	manifests := renderChart(t, "alertingRules.enabled=true")
	for _, want := range []string{
		"alert: ConcourseHangarSnapshotFailures",
		"artifact_daemon_snapshot_operations_total",
		"Hangar-backed snapshot operation failures",
	} {
		if !strings.Contains(manifests, want) {
			t.Errorf("Hangar alert is missing %q", want)
		}
	}
}

// TestAgentCheckpointAlertsUseOnlyClosedFailureOutcomes ensures an operator
// gets a concrete signal for the three recovery paths which require action:
// failed captures, recoveries stopped for manual review, and failed restores.
// The outcome matchers are intentionally exact rather than regexes so each
// alert remains bounded to the dimensions the OTel instruments expose.
func TestAgentCheckpointAlertsUseOnlyClosedFailureOutcomes(t *testing.T) {
	rule := findPrometheusRule(t, renderChart(t, "alertingRules.enabled=true"))
	for _, want := range []struct {
		name        string
		expr        string
		severity    string
		description string
	}{
		{
			name:        "ConcourseAgentCheckpointCaptureFailures",
			expr:        `increase(concourse_agent_checkpoint_captures_total{outcome="failed"}[5m]) > 0`,
			severity:    "warning",
			description: "Inspect agent checkpoint capture errors and durable snapshot storage before retrying the affected run.",
		},
		{
			name:        "ConcourseAgentRecoveryManualReviewRequired",
			expr:        `increase(concourse_agent_recovery_attempts_total{outcome="manual_review_required"}[5m]) > 0`,
			severity:    "critical",
			description: "Review the interrupted agent run for ambiguous effects or an unusable checkpoint before resuming it.",
		},
		{
			name:        "ConcourseAgentCheckpointRestoreFailures",
			expr:        `increase(concourse_agent_recovery_restore_duration_count{outcome="failed"}[5m]) > 0`,
			severity:    "warning",
			description: "Inspect the checkpoint object and recovery logs before retrying restore for the affected run.",
		},
	} {
		t.Run(want.name, func(t *testing.T) {
			alert, found := rule.alert(want.name)
			if !found {
				t.Fatalf("PrometheusRule does not contain %q", want.name)
			}
			if alert.Expr != want.expr {
				t.Errorf("%s expression = %q, want exact bounded selector %q", want.name, alert.Expr, want.expr)
			}
			if alert.For != "5m" {
				t.Errorf("%s for = %q, want 5m", want.name, alert.For)
			}
			if alert.Labels["severity"] != want.severity {
				t.Errorf("%s severity = %q, want %q", want.name, alert.Labels["severity"], want.severity)
			}
			if len(alert.Labels) != 1 {
				t.Errorf("%s alert labels = %#v, want only bounded severity", want.name, alert.Labels)
			}
			if alert.Annotations["description"] != want.description {
				t.Errorf("%s description = %q, want actionable guidance %q", want.name, alert.Annotations["description"], want.description)
			}
		})
	}
}

func TestAgentSnapshotScratchSupportsExistingPVC(t *testing.T) {
	manifests := renderChart(t,
		"artifactDaemon.enabled=true",
		"artifactDaemon.tls.enabled=true",
		"agentSnapshots.enabled=true",
		"artifactDaemon.hangar.bucket=test-hangar",
		"agentSnapshots.scratch.existingClaim=snapshot-scratch-pvc",
	)
	web := findDeployment(t, manifests, "-web")
	var scratch *podVolume
	for index := range web.Spec.Template.Spec.Volumes {
		if web.Spec.Template.Spec.Volumes[index].Name == "snapshot-scratch" {
			scratch = &web.Spec.Template.Spec.Volumes[index]
		}
	}
	if scratch == nil || scratch.PersistentVolumeClaim == nil ||
		scratch.PersistentVolumeClaim.ClaimName != "snapshot-scratch-pvc" {
		t.Fatalf("snapshot scratch PVC was not rendered: %+v", scratch)
	}
	if scratch.EmptyDir != nil {
		t.Fatal("snapshot scratch rendered both PVC and emptyDir")
	}
}

func TestAgentExperimentRunnerValuesRenderExactWebArguments(t *testing.T) {
	web := findDeployment(t, renderChart(t,
		"artifactDaemon.enabled=true",
		"artifactDaemon.tls.enabled=true",
		"agentSnapshots.enabled=true",
		"artifactDaemon.hangar.bucket=test-hangar",
		"agentExperiments.runnerEnabled=true",
		"agentExperiments.runnerInterval=17s",
		"agentExperiments.runnerMaxConcurrency=7",
	), "-web")
	args := web.Spec.Template.Spec.Containers[0].Args

	for _, want := range []string{
		"--agent-experiment-runner-enabled",
		"--agent-experiment-runner-interval=17s",
		"--agent-experiment-runner-max-concurrency=7",
	} {
		if !slices.Contains(args, want) {
			t.Errorf("web args do not contain %q: %v", want, args)
		}
	}
}

func TestAgentPublisherGatewayIsDisabledByDefault(t *testing.T) {
	web := findDeployment(t, renderChart(t), "-web")
	args := web.Spec.Template.Spec.Containers[0].Args
	for _, argument := range args {
		if strings.HasPrefix(argument, "--agent-publisher-gateway-") {
			t.Fatalf("default web args unexpectedly configure publisher gateway: %v", args)
		}
	}
}

func TestDisabledPublisherConfigurationDoesNotRenderVolumesOrMounts(t *testing.T) {
	manifests := renderChart(t,
		"agentPublisherGateway.enabled=false",
		"agentPublisherGateway.volumeMounts[0].name=disabled-publisher",
		"agentPublisherGateway.volumeMounts[0].mountPath=/etc/disabled-publisher",
		"agentPublisherGateway.volumeMounts[0].readOnly=true",
		"agentPublisherGateway.volumes[0].name=disabled-publisher",
		"agentPublisherGateway.volumes[0].secret.secretName=disabled-publisher",
	)
	if strings.Contains(manifests, "disabled-publisher") {
		t.Fatal("disabled publisher volumes or mounts leaked into the web pod")
	}
}

func TestAgentPublisherGatewayRendersMountedFileConfiguration(t *testing.T) {
	manifests := renderChart(t,
		"artifactDaemon.enabled=true",
		"artifactDaemon.tls.enabled=true",
		"agentSnapshots.enabled=true",
		"artifactDaemon.hangar.bucket=test-hangar",
		"agentPublisherGateway.enabled=true",
		"agentPublisherGateway.endpoint=https://publisher-gateway.platform.svc",
		"agentPublisherGateway.policyFile=/etc/concourse/publisher/policy.json",
		"agentPublisherGateway.tokenFile=/etc/concourse/publisher/token",
		"agentPublisherGateway.caCertificateFile=/etc/concourse/publisher/ca.crt",
		"agentPublisherGateway.requestTimeout=17s",
		"agentPublisherGateway.leaseDuration=4m",
		"agentPublisherGateway.maxResponseBytes=262144",
		"agentPublisherGateway.volumeMounts[0].name=publisher-config",
		"agentPublisherGateway.volumeMounts[0].mountPath=/etc/concourse/publisher",
		"agentPublisherGateway.volumeMounts[0].readOnly=true",
		"agentPublisherGateway.volumes[0].name=publisher-config",
		"agentPublisherGateway.volumes[0].secret.secretName=publisher-gateway",
	)
	web := findDeployment(t, manifests, "-web")
	args := web.Spec.Template.Spec.Containers[0].Args
	for _, want := range []string{
		"--agent-publisher-gateway-enabled",
		"--agent-publisher-gateway-endpoint=https://publisher-gateway.platform.svc",
		"--agent-publisher-gateway-policy-file=/etc/concourse/publisher/policy.json",
		"--agent-publisher-gateway-token-file=/etc/concourse/publisher/token",
		"--agent-publisher-gateway-ca-certificate-file=/etc/concourse/publisher/ca.crt",
		"--agent-publisher-gateway-request-timeout=17s",
		"--agent-publisher-gateway-lease-duration=4m",
		"--agent-publisher-gateway-max-response-bytes=262144",
	} {
		if !slices.Contains(args, want) {
			t.Errorf("web args do not contain %q: %v", want, args)
		}
	}
	if len(web.Spec.Template.Spec.InitContainers) == 0 {
		t.Fatal("rendered deployment has no migrate-db init container")
	}
	for _, init := range web.Spec.Template.Spec.InitContainers {
		for _, mount := range init.VolumeMounts {
			if mount.Name == "publisher-config" {
				t.Fatalf("publisher secret mount leaked into init container %q", init.Name)
			}
		}
	}
	foundWebMount := false
	for _, mount := range web.Spec.Template.Spec.Containers[0].VolumeMounts {
		if mount.Name == "publisher-config" && mount.MountPath == "/etc/concourse/publisher" && mount.ReadOnly {
			foundWebMount = true
		}
	}
	if !foundWebMount {
		t.Fatal("publisher mount missing from concourse-web")
	}
	for _, want := range []string{
		"mountPath: /etc/concourse/publisher",
		"secretName: publisher-gateway",
	} {
		if !strings.Contains(manifests, want) {
			t.Errorf("rendered chart does not contain extra mounted publisher file wiring %q", want)
		}
	}
}

func TestAgentSnapshotAndExperimentDependenciesFailAtChartRender(t *testing.T) {
	for name, test := range map[string]struct {
		sets []string
		want string
	}{
		"snapshots require authenticated storage": {
			sets: []string{"artifactDaemon.enabled=true", "artifactDaemon.tls.enabled=false", "agentSnapshots.enabled=true", "artifactDaemon.hangar.bucket=test-hangar"},
			want: "agentSnapshots.enabled requires artifactDaemon.tls.enabled",
		},
		"experiments require snapshots": {
			sets: []string{"agentExperiments.runnerEnabled=true"},
			want: "agentExperiments.runnerEnabled requires agentSnapshots.enabled",
		},
		"publisher gateway requires snapshots": {
			sets: []string{
				"agentPublisherGateway.enabled=true",
				"agentPublisherGateway.endpoint=https://gateway.example",
				"agentPublisherGateway.policyFile=/policy.json",
				"agentPublisherGateway.tokenFile=/token",
			},
			want: "agentPublisherGateway.enabled requires agentSnapshots.enabled",
		},
		"publisher gateway requires HTTPS": {
			sets: []string{
				"artifactDaemon.enabled=true", "artifactDaemon.tls.enabled=true", "agentSnapshots.enabled=true", "artifactDaemon.hangar.bucket=test-hangar",
				"agentPublisherGateway.enabled=true", "agentPublisherGateway.endpoint=http://gateway.example",
				"agentPublisherGateway.policyFile=/policy.json", "agentPublisherGateway.tokenFile=/token",
			},
			want: "agentPublisherGateway.endpoint must be an HTTPS origin",
		},
		"publisher gateway requires mounted file paths": {
			sets: []string{
				"artifactDaemon.enabled=true", "artifactDaemon.tls.enabled=true", "agentSnapshots.enabled=true", "artifactDaemon.hangar.bucket=test-hangar",
				"agentPublisherGateway.enabled=true", "agentPublisherGateway.endpoint=https://gateway.example",
			},
			want: "agentPublisherGateway.policyFile and tokenFile are required",
		},
		"publisher gateway requires dedicated web-only mounts": {
			sets: []string{
				"artifactDaemon.enabled=true", "artifactDaemon.tls.enabled=true", "agentSnapshots.enabled=true", "artifactDaemon.hangar.bucket=test-hangar",
				"agentPublisherGateway.enabled=true", "agentPublisherGateway.endpoint=https://gateway.example",
				"agentPublisherGateway.policyFile=/policy.json", "agentPublisherGateway.tokenFile=/token",
			},
			want: "agentPublisherGateway.volumes and volumeMounts",
		},
		"publisher mount must be read only": {
			sets: validPublisherSettings(
				"agentPublisherGateway.volumeMounts[0].readOnly=false",
			),
			want: "agentPublisherGateway.volumeMounts must all set readOnly=true",
		},
		"publisher mount must name a volume": {
			sets: validPublisherSettings(
				"agentPublisherGateway.volumeMounts[0].name=other",
			),
			want: "has no matching volume",
		},
		"publisher volume must be mounted": {
			sets: append(validPublisherSettings(),
				"agentPublisherGateway.volumes[1].name=unused",
				"agentPublisherGateway.volumes[1].secret.secretName=unused",
			),
			want: "has no matching volumeMount",
		},
		"publisher policy must lie beneath a mount": {
			sets: validPublisherSettings(
				"agentPublisherGateway.policyFile=/outside/policy.json",
			),
			want: "agentPublisherGateway.policyFile must lie beneath",
		},
		"publisher token path traversal is rejected": {
			sets: validPublisherSettings(
				"agentPublisherGateway.tokenFile=/etc/concourse/publisher/../token",
			),
			want: "agentPublisherGateway.tokenFile must be an absolute clean path",
		},
		"publisher CA must lie beneath a mount": {
			sets: validPublisherSettings(
				"agentPublisherGateway.caCertificateFile=/outside/ca.crt",
			),
			want: "agentPublisherGateway.caCertificateFile must lie beneath",
		},
		"snapshot scratch requires bounded disk capacity": {
			sets: []string{
				"artifactDaemon.enabled=true", "artifactDaemon.tls.enabled=true",
				"agentSnapshots.enabled=true", "artifactDaemon.hangar.bucket=test-hangar", "agentSnapshots.scratch.sizeLimit=",
			},
			want: "agentSnapshots.scratch.sizeLimit is required",
		},
		"artifact helper image is required": {
			sets: []string{"kubernetes.artifactHelperImage="},
			want: "kubernetes.artifactHelperImage is required",
		},
		"artifact helper image must be immutable": {
			sets: []string{"kubernetes.artifactHelperImage=alpine:latest"},
			want: "kubernetes.artifactHelperImage must be an exact @sha256",
		},
		"artifact helper image has one digest delimiter": {
			sets: []string{
				"kubernetes.artifactHelperImage=registry.example/helper@tag@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			},
			want: "kubernetes.artifactHelperImage must be an exact @sha256",
		},
		"projected API credential name is reserved": {
			sets: []string{
				"web.extraVolumeMounts[0].name=web-kubernetes-api-access",
				"web.extraVolumeMounts[0].mountPath=/var/run/secrets/leaked",
			},
			want: "web.extraVolumeMounts may not use reserved volume web-kubernetes-api-access",
		},
		"snapshot scratch name is reserved": {
			sets: []string{
				"web.extraVolumeMounts[0].name=snapshot-scratch",
				"web.extraVolumeMounts[0].mountPath=/var/concourse/leaked",
			},
			want: "web.extraVolumeMounts may not use reserved volume snapshot-scratch",
		},
		"artifact helper argument cannot bypass digest validation": {
			sets: []string{
				"web.extraArgs[0]=--kubernetes-artifact-helper-image=alpine:latest",
			},
			want: "web.extraArgs may not override kubernetes.artifactHelperImage",
		},
		"snapshot temp argument cannot override disk scratch": {
			sets: []string{
				"artifactDaemon.enabled=true", "artifactDaemon.tls.enabled=true",
				"agentSnapshots.enabled=true", "artifactDaemon.hangar.bucket=test-hangar",
				"web.extraArgs[0]=--agent-snapshot-temp-dir=/tmp",
			},
			want: "web.extraArgs may not override the chart-managed agent snapshot temp directory",
		},
		"snapshot TMPDIR cannot override private scratch": {
			sets: []string{
				"artifactDaemon.enabled=true", "artifactDaemon.tls.enabled=true",
				"agentSnapshots.enabled=true", "artifactDaemon.hangar.bucket=test-hangar",
				"web.env[0].name=TMPDIR", "web.env[0].value=/tmp",
			},
			want: "web.env may not override TMPDIR while agentSnapshots.enabled is true",
		},
	} {
		t.Run(name, func(t *testing.T) {
			output := renderChartFailure(t, test.sets...)
			if !strings.Contains(output, test.want) {
				t.Fatalf("helm error does not contain %q:\n%s", test.want, output)
			}
		})
	}
}

func validPublisherSettings(overrides ...string) []string {
	settings := []string{
		"artifactDaemon.enabled=true",
		"artifactDaemon.tls.enabled=true",
		"agentSnapshots.enabled=true",
		"artifactDaemon.hangar.bucket=test-hangar",
		"agentPublisherGateway.enabled=true",
		"agentPublisherGateway.endpoint=https://gateway.example",
		"agentPublisherGateway.policyFile=/etc/concourse/publisher/policy.json",
		"agentPublisherGateway.tokenFile=/etc/concourse/publisher/token",
		"agentPublisherGateway.caCertificateFile=/etc/concourse/publisher/ca.crt",
		"agentPublisherGateway.volumeMounts[0].name=publisher-config",
		"agentPublisherGateway.volumeMounts[0].mountPath=/etc/concourse/publisher",
		"agentPublisherGateway.volumeMounts[0].readOnly=true",
		"agentPublisherGateway.volumes[0].name=publisher-config",
		"agentPublisherGateway.volumes[0].secret.secretName=publisher-gateway",
	}
	return append(settings, overrides...)
}

func renderChartFailure(t *testing.T, sets ...string) string {
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
	}
	helperConfigured := false
	for _, set := range sets {
		helperConfigured = helperConfigured ||
			strings.HasPrefix(set, "kubernetes.artifactHelperImage=")
	}
	if !helperConfigured {
		args = append(args,
			"--set-string", "kubernetes.artifactHelperImage="+testArtifactHelperImage)
	}
	for _, set := range sets {
		args = append(args, "--set", set)
	}
	output, err := exec.Command("helm", args...).CombinedOutput()
	if err == nil {
		t.Fatalf("helm template unexpectedly accepted invalid agentic configuration")
	}
	return string(output)
}

type prometheusRuleDoc struct {
	Kind string `json:"kind"`
	Spec struct {
		Groups []struct {
			Rules []prometheusAlertRule `json:"rules"`
		} `json:"groups"`
	} `json:"spec"`
}

type prometheusAlertRule struct {
	Alert       string            `json:"alert"`
	Expr        string            `json:"expr"`
	For         string            `json:"for"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
}

func (rule prometheusRuleDoc) alert(name string) (prometheusAlertRule, bool) {
	for _, group := range rule.Spec.Groups {
		for _, candidate := range group.Rules {
			if candidate.Alert == name {
				return candidate, true
			}
		}
	}
	return prometheusAlertRule{}, false
}

func findPrometheusRule(t *testing.T, manifests string) prometheusRuleDoc {
	t.Helper()
	for _, doc := range strings.Split(manifests, "\n---") {
		var rule prometheusRuleDoc
		if err := yaml.Unmarshal([]byte(doc), &rule); err != nil {
			continue
		}
		if rule.Kind == "PrometheusRule" {
			return rule
		}
	}
	t.Fatal("no PrometheusRule found in rendered chart")
	return prometheusRuleDoc{}
}
