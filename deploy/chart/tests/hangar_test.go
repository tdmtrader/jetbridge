package tests

import (
	"encoding/base64"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

var daemonHangarSets = []string{
	"artifactDaemon.hangar.enabled=true",
	"artifactDaemon.tls.existingSecret=operator-daemon-tls",
	"artifactDaemon.hangar.store=gcs",
	"artifactDaemon.hangar.bucket=hangar-bucket",
}

var enabledHangarSets = append(append([]string{}, daemonHangarSets...), "artifactDaemon.hangar.webEnabled=true")

func renderHangar(t *testing.T, extra ...string) string {
	t.Helper()
	sets := append(append([]string{}, enabledHangarSets...), extra...)
	return render(t, sets...)
}

func renderHangarError(t *testing.T, sets ...string) string {
	t.Helper()
	args := []string{"template", "jb", "deploy/chart", "-f", "deploy/chart/tests/testdata/required-values.yaml"}
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
		"artifactDaemon.hangar.prefix=cluster-a",
		"artifactDaemon.hangar.endpoint=http://gcs.test",
	)
	for _, want := range []string{
		"--hangar-enabled", "--hangar-scratch-dir=/private/hangar-scratch",
		"--hangar-key=/etc/concourse/daemon-tls/hangar.key",
		"--hangar-max-content-bytes=123456", "--hangar-max-entries=321",
		"--hangar-store=gcs", "--hangar-bucket=hangar-bucket", "--hangar-prefix=cluster-a",
		"--hangar-endpoint=http://gcs.test",
		"--kubernetes-hangar-enabled", "--kubernetes-hangar-key=/etc/concourse/daemon-tls/hangar.key",
		"--kubernetes-hangar-warrant-ttl=420s", "concourse.dev/hangar-v1", "name: hangar-scratch",
		// Web is named the strict-input namespace only so it can refuse one
		// shared with the output namespace at startup.
		"--kubernetes-hangar-input-bucket=hangar-bucket",
		"mountPath: /private/hangar-scratch", "emptyDir: {}",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("enabled render missing %q", want)
		}
	}
	// The daemon takes no TTL: its verifier caps every warrant at Hangar's
	// 15-minute bound. The web signs with the configured lifetime.
	if got := strings.Count(out, "--kubernetes-hangar-warrant-ttl=420s"); got != 1 {
		t.Errorf("warrant TTL was not rendered once to web: count=%d", got)
	}
	if strings.Contains(out, "--hangar-warrant-ttl") {
		t.Error("the daemon is handed a warrant TTL; it has no such flag")
	}
	// Hangar is handed its own store and nothing of the resource-cache tier's.
	if strings.Contains(out, "--durable-") {
		t.Error("enabled render passes the daemon a durable-tier flag")
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
		{"TLS disabled", []string{"artifactDaemon.tls.enabled=false", "artifactDaemon.hangar.enabled=true", "artifactDaemon.hangar.store=gcs", "artifactDaemon.hangar.bucket=b"}, "tls.enabled"},
		// The schema refuses s3 before any template runs. "must be one of" is
		// the schema's enum message under Helm 3 and Helm 4 alike, and not the
		// template's own "must be gcs or disk".
		{"S3 store", []string{"artifactDaemon.hangar.enabled=true", "artifactDaemon.hangar.store=s3", "artifactDaemon.hangar.bucket=b"}, "must be one of"},
		{"missing bucket", []string{"artifactDaemon.hangar.enabled=true", "artifactDaemon.hangar.store=gcs"}, "hangar.bucket"},
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

// Hangar names its own store. It once borrowed the durable tier's store and
// bucket when hangar.store was unset; that tier is now a removed key, which
// shape_test.go's removed-keys guard covers.
func TestHangarWithoutItsOwnStoreFails(t *testing.T) {
	out := renderHangarError(t, "artifactDaemon.hangar.enabled=true", "artifactDaemon.hangar.bucket=b")
	if !strings.Contains(out, "artifactDaemon.hangar.store") {
		t.Fatalf("error did not name artifactDaemon.hangar.store:\n%s", out)
	}
}

func TestHangarRejectsImplicitGeneratedKey(t *testing.T) {
	sets := []string{
		"artifactDaemon.tls.source=generated",
		"artifactDaemon.tls.existingSecret=",
		"artifactDaemon.hangar.enabled=true",
		"artifactDaemon.hangar.store=gcs",
		"artifactDaemon.hangar.bucket=hangar-bucket",
	}
	if out := renderHangarError(t, sets...); !strings.Contains(out, "allowGeneratedKey") {
		t.Fatalf("implicit generated key error did not name the opt-in:\n%s", out)
	}
}

func TestHangarExplicitLiveHelmGenerationContainsStrongRawKey(t *testing.T) {
	out := render(t,
		"artifactDaemon.tls.source=generated",
		"artifactDaemon.tls.existingSecret=",
		"artifactDaemon.hangar.enabled=true",
		"artifactDaemon.hangar.allowGeneratedKey=true",
		"artifactDaemon.hangar.store=gcs",
		"artifactDaemon.hangar.bucket=hangar-bucket",
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
		for _, want := range []string{"--kubernetes-hangar-warrant-ttl=" + ttl} {
			if !strings.Contains(out, want) {
				t.Errorf("TTL %s render missing %q", ttl, want)
			}
		}
	}
	defaulted := renderHangar(t)
	for _, want := range []string{"--kubernetes-hangar-warrant-ttl=900s"} {
		if !strings.Contains(defaulted, want) {
			t.Errorf("default render missing %q", want)
		}
	}
}

func TestHangarNetworkPolicyAllowsGKEWorkloadIdentityMetadata(t *testing.T) {
	out := renderHangar(t, "artifactDaemon.networkPolicy.enabled=true")
	policyFound := false
	metadataRules := map[string][]map[int]string{}
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
		policyFound = true
		for _, rule := range policy.Spec.Egress {
			if len(rule.To) != 1 || rule.To[0].IPBlock.CIDR == "" {
				continue
			}
			ports := map[int]string{}
			for _, port := range rule.Ports {
				ports[port.Port] = port.Protocol
			}
			metadataRules[rule.To[0].IPBlock.CIDR] = append(metadataRules[rule.To[0].IPBlock.CIDR], ports)
		}
	}
	if !policyFound {
		t.Fatal("enabled render contained no artifact-daemon NetworkPolicy")
	}

	wants := map[string]map[int]string{
		"169.254.169.252/32": {987: "TCP", 988: "TCP"},
		"169.254.169.254/32": {80: "TCP", 8080: "TCP"},
	}
	for cidr, want := range wants {
		got := metadataRules[cidr]
		if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
			t.Errorf("GKE metadata egress for %s = %#v, want one exact rule %#v", cidr, got, want)
		}
	}
	if got := metadataRules["127.0.0.1/32"]; len(got) != 0 {
		t.Errorf("obsolete pre-GKE-1.21 loopback metadata egress was rendered: %#v", got)
	}
}

func TestHangarNetworkPolicyAllowsWebWorkersAndPeers(t *testing.T) {
	out := renderHangar(t, "artifactDaemon.networkPolicy.enabled=true")
	var ingress []struct {
		From []struct {
			PodSelector struct {
				MatchLabels      map[string]string `json:"matchLabels"`
				MatchExpressions []struct {
					Key      string `json:"key"`
					Operator string `json:"operator"`
				} `json:"matchExpressions"`
			} `json:"podSelector"`
		} `json:"from"`
	}
	for _, doc := range strings.Split(out, "\n---") {
		var policy struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Spec struct {
				Ingress []struct {
					From []struct {
						PodSelector struct {
							MatchLabels      map[string]string `json:"matchLabels"`
							MatchExpressions []struct {
								Key      string `json:"key"`
								Operator string `json:"operator"`
							} `json:"matchExpressions"`
						} `json:"podSelector"`
					} `json:"from"`
				} `json:"ingress"`
			} `json:"spec"`
		}
		if yaml.Unmarshal([]byte(doc), &policy) == nil && policy.Kind == "NetworkPolicy" && strings.HasSuffix(policy.Metadata.Name, "-artifact-daemon") {
			ingress = policy.Spec.Ingress
			break
		}
	}
	if len(ingress) == 0 {
		t.Fatal("enabled render contained no artifact-daemon ingress rules")
	}

	web, workers, peers := false, false, false
	for _, rule := range ingress {
		for _, from := range rule.From {
			labels := from.PodSelector.MatchLabels
			if labels["app.kubernetes.io/component"] == "web" {
				web = true
			}
			if labels["app.kubernetes.io/component"] == "artifact-daemon" {
				peers = true
			}
			for _, expression := range from.PodSelector.MatchExpressions {
				if expression.Key == "concourse.ci/worker" && expression.Operator == "Exists" {
					workers = true
				}
			}
		}
	}
	if !web || !workers || !peers {
		t.Fatalf("artifact-daemon ingress web=%t workers=%t peers=%t, want all allowed", web, workers, peers)
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
	if strings.Contains(out, "--kubernetes-hangar-key=hangar.key") {
		t.Fatal("key was rendered without its private control-plane mount path")
	}
}
