package tests

import (
	"encoding/base64"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/yaml"
)

// hangarKeySets turn on exact execution control with the Hangar key held in
// the daemon TLS Secret rather than its own (baseControlSets, in
// hangar_output_test.go, name artifactDaemon.hangar.keySecret instead). Every
// Run input is a managed read of the output plane, so this is the one switch
// the key, its TTL and the tree limits ride on.
var hangarKeySets = []string{
	"hangarOutput.executionControl.enabled=true",
	"artifactDaemon.tls.existingSecret=operator-daemon-tls",
	"artifactDaemon.outputScratch.sizeLimit=32Gi",
}

func renderHangar(t *testing.T, extra ...string) string {
	t.Helper()
	sets := append(append([]string{}, hangarKeySets...), extra...)
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
	for _, unexpected := range []string{"--hangar-", "--kubernetes-hangar-", "hangar.key", "hangar-scratch", "concourse.dev/hangar-", "--output-max-"} {
		if strings.Contains(out, unexpected) {
			t.Errorf("default render contains %q", unexpected)
		}
	}
}

// The artifact daemon's container args, typed-decoded from the DaemonSet.
func daemonArgs(t *testing.T, manifests string) []string {
	t.Helper()
	var daemon appsv1.DaemonSet
	if err := yaml.UnmarshalStrict([]byte(daemonSetDocument(t, manifests)), &daemon); err != nil {
		t.Fatal(err)
	}
	if len(daemon.Spec.Template.Spec.Containers) != 1 {
		t.Fatalf("artifact-daemon containers=%d, want 1", len(daemon.Spec.Template.Spec.Containers))
	}
	c := daemon.Spec.Template.Spec.Containers[0]
	return append(append([]string{}, c.Command...), c.Args...)
}

// With exact execution control on, the key, the web's warrant TTL and the one
// set of tree limits (artifactDaemon.outputScratch) all render. The daemon's
// only --hangar-* flag is the key, and the web's only --kubernetes-hangar-*
// flags are the key and its TTL.
func TestHangarKeyRendersWithExecutionControl(t *testing.T) {
	out := renderHangar(t,
		"artifactDaemon.hangar.capabilityTTL=420s",
		"artifactDaemon.outputScratch.maxContentBytes=123456",
		"artifactDaemon.outputScratch.maxEntries=321",
	)
	for _, want := range []string{
		"--hangar-key=/etc/concourse/daemon-tls/hangar.key",
		"--kubernetes-hangar-key=/etc/concourse/daemon-tls/hangar.key",
		"--kubernetes-hangar-warrant-ttl=420s",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("enabled render missing %q", want)
		}
	}
	args := daemonArgs(t, out)
	for _, want := range []string{
		"--execution-control",
		"--output-max-content-bytes=123456",
		"--output-max-entries=321",
	} {
		found := false
		for _, arg := range args {
			if arg == want {
				found = true
			}
		}
		if !found {
			t.Errorf("the artifact daemon is not handed %q: %v", want, args)
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
	for _, arg := range args {
		if strings.HasPrefix(arg, "--hangar-") && !strings.HasPrefix(arg, "--hangar-key=") {
			t.Errorf("the artifact daemon is handed %q; its only Hangar flag is the key", arg)
		}
		if strings.HasPrefix(arg, "--durable-") || strings.Contains(arg, "input-bucket") {
			t.Errorf("the artifact daemon is handed %q", arg)
		}
	}
	for _, arg := range strictWebDeployment(t, out).Spec.Template.Spec.Containers[0].Args {
		if strings.HasPrefix(arg, "--kubernetes-hangar-") && !strings.HasPrefix(arg, "--kubernetes-hangar-key=") && !strings.HasPrefix(arg, "--kubernetes-hangar-warrant-ttl=") && !strings.HasPrefix(arg, "--kubernetes-hangar-output-") {
			t.Errorf("web is handed %q; its only Hangar flags are the key, its TTL and the output plane's", arg)
		}
	}
	for _, gone := range []string{"name: hangar-scratch", "hangar-disk-client"} {
		if strings.Contains(out, gone) {
			t.Errorf("enabled render carries %q", gone)
		}
	}
	for _, unwanted := range []string{"GOOGLE_APPLICATION_CREDENTIALS", "credentials.json", "artifactDaemon.hangar.existingSecret"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("render contains credential surface %q", unwanted)
		}
	}
}

func TestHangarRejectsInvalidPrerequisitesAtRender(t *testing.T) {
	withKey := func(extra ...string) []string {
		return append(append([]string{}, hangarKeySets...), extra...)
	}
	for _, tc := range []struct {
		name string
		sets []string
		want string
	}{
		{"daemon disabled", withKey("artifactDaemon.enabled=false"), "artifactDaemon.enabled"},
		{"TLS disabled", withKey("artifactDaemon.tls.enabled=false"), "tls.enabled"},
		{"zero bytes", withKey("artifactDaemon.outputScratch.maxContentBytes=0"), "artifactDaemon.outputScratch"},
		{"zero entries", withKey("artifactDaemon.outputScratch.maxEntries=0"), "artifactDaemon.outputScratch"},
		{"TTL not whole seconds", withKey("artifactDaemon.hangar.capabilityTTL=15m"), "whole seconds"},
		{"nanosecond over max", withKey("artifactDaemon.hangar.capabilityTTL=15m1ns"), "whole seconds"},
		{"fractional max", withKey("artifactDaemon.hangar.capabilityTTL=15m500ms"), "whole seconds"},
		{"milliseconds over max", withKey("artifactDaemon.hangar.capabilityTTL=900001ms"), "whole seconds"},
		{"subsecond", withKey("artifactDaemon.hangar.capabilityTTL=999ms"), "whole seconds"},
		{"TTL above maximum", withKey("artifactDaemon.hangar.capabilityTTL=901s"), "900s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if out := renderHangarError(t, tc.sets...); !strings.Contains(out, tc.want) {
				t.Fatalf("error missing %q:\n%s", tc.want, out)
			}
		})
	}
}

// The TTL is checked only where the key is: a deployment without exact
// execution control signs no warrant, so a bad TTL there is inert.
func TestHangarTTLIsNotCheckedWithoutExecutionControl(t *testing.T) {
	render(t, "artifactDaemon.hangar.capabilityTTL=15m")
}

func TestHangarRejectsImplicitGeneratedKey(t *testing.T) {
	sets := []string{
		"artifactDaemon.tls.source=generated",
		"artifactDaemon.tls.existingSecret=",
		"hangarOutput.executionControl.enabled=true",
		"artifactDaemon.outputScratch.sizeLimit=32Gi",
	}
	if out := renderHangarError(t, sets...); !strings.Contains(out, "allowGeneratedKey") {
		t.Fatalf("implicit generated key error did not name the opt-in:\n%s", out)
	}
}

func TestHangarExplicitLiveHelmGenerationContainsStrongRawKey(t *testing.T) {
	out := render(t,
		"artifactDaemon.tls.source=generated",
		"artifactDaemon.tls.existingSecret=",
		"hangarOutput.executionControl.enabled=true",
		"artifactDaemon.outputScratch.sizeLimit=32Gi",
		"artifactDaemon.hangar.allowGeneratedKey=true",
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

// A generated TLS Secret without exact execution control holds no Hangar key:
// nothing would read it.
func TestGeneratedTLSSecretHoldsNoHangarKeyWithoutExecutionControl(t *testing.T) {
	out := render(t,
		"artifactDaemon.tls.source=generated",
		"artifactDaemon.tls.existingSecret=",
	)
	if strings.Contains(out, "hangar.key") {
		t.Fatal("the generated TLS Secret carries hangar.key with nothing to sign or verify")
	}
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
	if strings.Count(out, "mountPath: /var/concourse/hangar-output-scratch") != 1 {
		t.Fatalf("the one canonicalization scratch should mount only in artifact-daemon")
	}
	if strings.Contains(out, "--kubernetes-hangar-key=hangar.key") {
		t.Fatal("key was rendered without its private control-plane mount path")
	}
}
