package tests

import (
	"encoding/base64"
	"os/exec"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

var daemonHangarSets = []string{
	"artifactDaemon.hangar.enabled=true",
	"artifactDaemon.tls.enabled=true",
	"artifactDaemon.tls.existingSecret=operator-daemon-tls",
	"artifactDaemon.durable.store=gcs",
	"artifactDaemon.durable.bucket=hangar-bucket",
}

var enabledHangarSets = append(append([]string{}, daemonHangarSets...), "artifactDaemon.hangar.webEnabled=true")

func renderHangar(t *testing.T, extra ...string) string {
	t.Helper()
	sets := append(append([]string{}, enabledHangarSets...), extra...)
	return render(t, sets...)
}

func renderHangarError(t *testing.T, sets ...string) string {
	t.Helper()
	args := []string{"template", "jb", "deploy/chart"}
	for _, set := range sets {
		args = append(args, "--set", set)
	}
	cmd := exec.Command("helm", args...)
	cmd.Dir = repoRoot(t)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("helm template unexpectedly accepted %v", sets)
	}
	return string(out)
}

func TestHangarIsOffWithoutAnyRenderedSurfaceByDefault(t *testing.T) {
	out := render(t)
	for _, unexpected := range []string{"--hangar-", "--kubernetes-hangar-", "hangar.key", "hangar-scratch", "concourse.dev/hangar-v1"} {
		if strings.Contains(out, unexpected) {
			t.Errorf("default render contains %q", unexpected)
		}
	}
}

func TestHangarEnabledRendersSharedBoundedConfiguration(t *testing.T) {
	out := renderHangar(t,
		"artifactDaemon.hangar.scratchPath=/private/hangar-scratch",
		"artifactDaemon.hangar.maxContentBytes=123456",
		"artifactDaemon.hangar.maxEntries=321",
		"artifactDaemon.hangar.capabilityTTL=420s",
		"artifactDaemon.durable.prefix=cluster-a",
		"artifactDaemon.durable.endpoint=http://gcs.test",
		"artifactDaemon.durable.timeout=45s",
	)
	for _, want := range []string{
		"--hangar-enabled", "--hangar-scratch-dir=/private/hangar-scratch",
		"--hangar-capability-key=/etc/concourse/daemon-tls/hangar.key",
		"--hangar-capability-ttl=420s", "--hangar-max-content-bytes=123456", "--hangar-max-entries=321",
		"--durable-store=gcs", "--durable-bucket=hangar-bucket", "--durable-prefix=cluster-a",
		"--durable-endpoint=http://gcs.test", "--durable-timeout=45s",
		"--kubernetes-hangar-enabled", "--kubernetes-hangar-capability-key=/etc/concourse/daemon-tls/hangar.key",
		"--kubernetes-hangar-capability-ttl=420s", "concourse.dev/hangar-v1", "name: hangar-scratch",
		"mountPath: /private/hangar-scratch", "emptyDir: {}",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("enabled render missing %q", want)
		}
	}
	if got := strings.Count(out, "--hangar-capability-ttl=420s") + strings.Count(out, "--kubernetes-hangar-capability-ttl=420s"); got != 2 {
		t.Errorf("capability TTL was not rendered once to each binary: count=%d", got)
	}
	for _, unwanted := range []string{"GOOGLE_APPLICATION_CREDENTIALS", "credentials.json", "artifactDaemon.hangar.existingSecret"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("GCS Hangar render contains credential surface %q", unwanted)
		}
	}
}

func TestHangarRejectsInvalidPrerequisitesAtRender(t *testing.T) {
	for _, tc := range []struct {
		name string
		sets []string
		want string
	}{
		{"daemon disabled", []string{"artifactDaemon.enabled=false", "artifactDaemon.hangar.enabled=true"}, "artifactDaemon.enabled"},
		{"web without daemon support", []string{"artifactDaemon.hangar.webEnabled=true"}, "hangar.enabled"},
		{"TLS disabled", []string{"artifactDaemon.hangar.enabled=true", "artifactDaemon.durable.store=gcs", "artifactDaemon.durable.bucket=b"}, "tls.enabled"},
		{"non-GCS", []string{"artifactDaemon.hangar.enabled=true", "artifactDaemon.tls.enabled=true", "artifactDaemon.durable.store=s3", "artifactDaemon.durable.bucket=b"}, "durable.store must be gcs"},
		{"missing bucket", []string{"artifactDaemon.hangar.enabled=true", "artifactDaemon.tls.enabled=true", "artifactDaemon.durable.store=gcs"}, "durable.bucket"},
		{"relative scratch", append(append([]string{}, enabledHangarSets...), "artifactDaemon.hangar.scratchPath=relative"), "absolute"},
		{"scratch below artifacts", append(append([]string{}, enabledHangarSets...), "artifactDaemon.hangar.scratchPath=/var/concourse/artifacts/scratch"), "disjoint"},
		{"artifacts below scratch", append(append([]string{}, enabledHangarSets...), "artifactDaemon.hangar.scratchPath=/private/hangar-scratch", "artifactDaemon.hostPath=/private/hangar-scratch/artifacts"), "disjoint"},
		{"zero bytes", append(append([]string{}, enabledHangarSets...), "artifactDaemon.hangar.maxContentBytes=0"), "positive"},
		{"zero entries", append(append([]string{}, enabledHangarSets...), "artifactDaemon.hangar.maxEntries=0"), "positive"},
		{"zero TTL", append(append([]string{}, enabledHangarSets...), "artifactDaemon.hangar.capabilityTTL=0s"), "whole seconds"},
		{"negative TTL", append(append([]string{}, enabledHangarSets...), "artifactDaemon.hangar.capabilityTTL=-1s"), "whole seconds"},
		{"minutes", append(append([]string{}, enabledHangarSets...), "artifactDaemon.hangar.capabilityTTL=15m"), "whole seconds"},
		{"nanosecond over max", append(append([]string{}, enabledHangarSets...), "artifactDaemon.hangar.capabilityTTL=15m1ns"), "whole seconds"},
		{"fractional max", append(append([]string{}, enabledHangarSets...), "artifactDaemon.hangar.capabilityTTL=15m500ms"), "whole seconds"},
		{"milliseconds over max", append(append([]string{}, enabledHangarSets...), "artifactDaemon.hangar.capabilityTTL=900001ms"), "whole seconds"},
		{"subsecond", append(append([]string{}, enabledHangarSets...), "artifactDaemon.hangar.capabilityTTL=999ms"), "whole seconds"},
		{"TTL above maximum", append(append([]string{}, enabledHangarSets...), "artifactDaemon.hangar.capabilityTTL=901s"), "900s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if out := renderHangarError(t, tc.sets...); !strings.Contains(out, tc.want) {
				t.Fatalf("error missing %q:\n%s", tc.want, out)
			}
		})
	}
}

func TestHangarRejectsImplicitGeneratedKey(t *testing.T) {
	sets := []string{
		"artifactDaemon.hangar.enabled=true",
		"artifactDaemon.tls.enabled=true",
		"artifactDaemon.durable.store=gcs",
		"artifactDaemon.durable.bucket=hangar-bucket",
	}
	if out := renderHangarError(t, sets...); !strings.Contains(out, "allowGeneratedKey") {
		t.Fatalf("implicit generated key error did not name the opt-in:\n%s", out)
	}
}

func TestHangarExplicitLiveHelmGenerationContainsStrongRawKey(t *testing.T) {
	out := render(t,
		"artifactDaemon.hangar.enabled=true",
		"artifactDaemon.hangar.allowGeneratedKey=true",
		"artifactDaemon.tls.enabled=true",
		"artifactDaemon.durable.store=gcs",
		"artifactDaemon.durable.bucket=hangar-bucket",
	)
	for _, doc := range strings.Split(out, "\n---") {
		var secret struct {
			Kind string            `json:"kind"`
			Data map[string]string `json:"data"`
		}
		if yaml.Unmarshal([]byte(doc), &secret) != nil || secret.Kind != "Secret" || secret.Data["hangar.key"] == "" {
			continue
		}
		key, err := base64.StdEncoding.DecodeString(secret.Data["hangar.key"])
		if err != nil || len(key) != 32 {
			t.Fatalf("generated hangar.key decodes to %d bytes, error=%v", len(key), err)
		}
		return
	}
	t.Fatal("auto-generated artifact-daemon Secret had no hangar.key")
}

func TestHangarExistingSecretIsSelectedWithoutParallelSecret(t *testing.T) {
	out := renderHangar(t)
	if !strings.Contains(out, "secretName: operator-daemon-tls") || !strings.Contains(out, "key: hangar.key") {
		t.Fatal("existing TLS Secret or required hangar.key selection was not rendered")
	}
	if strings.Contains(out, "ca.key:") {
		t.Fatal("chart generated a parallel TLS Secret while existingSecret was selected")
	}
}

func TestHangarExistingSecretRenderIsDeterministic(t *testing.T) {
	first := renderHangar(t)
	second := renderHangar(t)
	if first != second {
		t.Fatal("existing-secret Hangar render changed without an input change")
	}
}

func TestHangarStagesDaemonSupportBeforeWebEmission(t *testing.T) {
	daemonOnly := render(t, daemonHangarSets...)
	for _, want := range []string{"--hangar-enabled", "hangar.key", "concourse.dev/hangar-v1"} {
		if !strings.Contains(daemonOnly, want) {
			t.Errorf("daemon-only rollout missing %q", want)
		}
	}
	if strings.Contains(daemonOnly, "--kubernetes-hangar-") {
		t.Fatal("daemon-only rollout enabled web Hangar emission")
	}

	full := renderHangar(t)
	if !strings.Contains(full, "--kubernetes-hangar-enabled") {
		t.Fatal("full rollout did not enable web Hangar emission")
	}
	if daemonSetDocument(t, daemonOnly) != daemonSetDocument(t, full) {
		t.Fatal("changing only hangar.webEnabled changed the artifact DaemonSet")
	}
}

func TestHangarAcceptsWholeSecondCapabilityTTLBoundaries(t *testing.T) {
	for _, ttl := range []string{"1s", "900s"} {
		out := renderHangar(t, "artifactDaemon.hangar.capabilityTTL="+ttl)
		for _, want := range []string{"--hangar-capability-ttl=" + ttl, "--kubernetes-hangar-capability-ttl=" + ttl} {
			if !strings.Contains(out, want) {
				t.Errorf("TTL %s render missing %q", ttl, want)
			}
		}
	}
	defaulted := renderHangar(t)
	for _, want := range []string{"--hangar-capability-ttl=900s", "--kubernetes-hangar-capability-ttl=900s"} {
		if !strings.Contains(defaulted, want) {
			t.Errorf("default render missing %q", want)
		}
	}
}

func TestHangarNetworkPolicyAllowsGKEWorkloadIdentityMetadata(t *testing.T) {
	out := renderHangar(t, "artifactDaemon.networkPolicy.enabled=true")
	var found bool
	for _, doc := range strings.Split(out, "\n---") {
		var policy struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Spec struct {
				Egress []struct {
					To []struct {
						IPBlock struct {
							CIDR string `json:"cidr"`
						} `json:"ipBlock"`
					} `json:"to"`
					Ports []struct {
						Port     int    `json:"port"`
						Protocol string `json:"protocol"`
					} `json:"ports"`
				} `json:"egress"`
			} `json:"spec"`
		}
		if yaml.Unmarshal([]byte(doc), &policy) != nil || policy.Kind != "NetworkPolicy" || !strings.HasSuffix(policy.Metadata.Name, "-artifact-daemon") {
			continue
		}
		for _, rule := range policy.Spec.Egress {
			if len(rule.To) != 1 || rule.To[0].IPBlock.CIDR != "169.254.169.254/32" {
				continue
			}
			ports := map[int]string{}
			for _, port := range rule.Ports {
				ports[port.Port] = port.Protocol
			}
			if ports[80] == "TCP" && ports[988] == "TCP" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("enabled artifact-daemon NetworkPolicy omitted GKE metadata TCP 80/988 egress")
	}
}

func daemonSetDocument(t *testing.T, manifests string) string {
	t.Helper()
	for _, doc := range strings.Split(manifests, "\n---") {
		var object struct {
			Kind string `json:"kind"`
		}
		if yaml.Unmarshal([]byte(doc), &object) == nil && object.Kind == "DaemonSet" {
			return strings.TrimSpace(doc)
		}
	}
	t.Fatal("render contained no DaemonSet")
	return ""
}

func TestHangarKeyAndScratchRemainPrivateToControlPlanePods(t *testing.T) {
	out := renderHangar(t)
	if strings.Count(out, "mountPath: /etc/concourse/daemon-tls") != 2 {
		t.Fatalf("daemon TLS/key Secret should mount only in web and artifact-daemon")
	}
	if strings.Count(out, "mountPath: /var/concourse/hangar-scratch") != 1 {
		t.Fatalf("private scratch should mount only in artifact-daemon")
	}
	if strings.Contains(out, "--kubernetes-hangar-capability-key=hangar.key") {
		t.Fatal("key was rendered without its private control-plane mount path")
	}
}
