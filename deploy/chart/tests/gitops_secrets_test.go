package tests

import (
	"strings"
	"testing"
)

// externallyManagedSecrets is every value that moves a chart-generated Secret
// out of the chart. Each one exists because Helm's `lookup` — the only thing
// making the generated material stable across renders — returns nothing
// without a cluster connection, which is precisely how Argo CD renders.
var externallyManagedSecrets = []string{
	"secrets.signingKeySecret=operator-keys",
	"artifactDaemon.tls.existingSecret=operator-daemon-tls",
	"artifactDaemon.resolveCapability.existingSecret=operator-resolve-capability",
}

// TestClusterlessRendersChurnGeneratedSecrets is the reason the warning and
// the existingSecret values exist. If a future change ever makes the generated
// material deterministic, this test fails and the loud NOTES warning, the
// README caveat, and the Argo CD preamble should all be revisited together —
// rather than being left in place describing a hazard that no longer exists.
func TestClusterlessRendersChurnGeneratedSecrets(t *testing.T) {
	sets := []string{"artifactDaemon.tls.enabled=true"}
	first := renderChart(t, sets...)
	second := renderChart(t, sets...)
	if first == second {
		t.Fatal("two cluster-less renders are identical; chart-generated secrets may no longer depend on lookup")
	}

	// Name the specific Secrets that churn, so a change to only one of them
	// is visible rather than being masked by the others.
	for _, nameSuffix := range []string{
		"-keys",
		"-artifact-daemon-tls",
		"-artifact-daemon-resolve-capability",
	} {
		before := findSecret(t, first, "test-release-concourse-jetbridge"+nameSuffix)
		after := findSecret(t, second, "test-release-concourse-jetbridge"+nameSuffix)
		identical := true
		for key, value := range before.Data {
			if after.Data[key] != value {
				identical = false
			}
		}
		if identical {
			t.Errorf("Secret %s is stable across cluster-less renders; the GitOps warning may be stale", nameSuffix)
		}
	}
}

// TestExternallyManagedSecretsRenderIdentically pins the remedy: with all
// three existingSecret values set, two cluster-less renders of the whole chart
// are byte-identical, so an Argo CD sync has nothing to re-apply and no pod
// ever comes up on material its peers do not share.
func TestExternallyManagedSecretsRenderIdentically(t *testing.T) {
	sets := append([]string{"artifactDaemon.tls.enabled=true"}, externallyManagedSecrets...)
	first := renderChart(t, sets...)
	second := renderChart(t, sets...)
	if first != second {
		t.Fatal("two cluster-less renders differ despite every secret being externally managed")
	}

	// And the chart must stop generating them, not merely stop mounting them:
	// a rendered-but-unused Secret still churns in the diff and still leaks
	// private key material into any committed manifest.
	for _, nameSuffix := range []string{
		"test-release-concourse-jetbridge-keys",
		"test-release-concourse-jetbridge-artifact-daemon-tls",
		"test-release-concourse-jetbridge-artifact-daemon-resolve-capability",
	} {
		if strings.Contains(first, "name: "+nameSuffix+"\n") {
			t.Errorf("chart still generates Secret %s despite an explicit existingSecret", nameSuffix)
		}
	}

	// The operator's Secrets must actually be the ones mounted, on both the
	// web Deployment and the DaemonSet — the two workloads that must agree.
	for _, want := range []string{
		"secretName: operator-keys",
		"secretName: operator-daemon-tls",
		"secretName: operator-resolve-capability",
	} {
		if strings.Count(first, want) < 1 {
			t.Errorf("rendered manifests never mount %q", want)
		}
	}
	web := findDeployment(t, first, "-web")
	daemon := findDaemonSet(t, first, "-artifact-daemon")
	if len(web.Spec.Template.Spec.Volumes) == 0 || len(daemon.Metadata.Name) == 0 {
		t.Fatal("web Deployment or artifact-daemon DaemonSet did not render")
	}
	mounted := map[string]bool{}
	for _, volume := range web.Spec.Template.Spec.Volumes {
		if volume.Secret != nil {
			mounted[volume.Secret.SecretName] = true
		}
	}
	for _, want := range []string{"operator-keys", "operator-daemon-tls", "operator-resolve-capability"} {
		if !mounted[want] {
			t.Errorf("web pod does not mount %q: %+v", want, mounted)
		}
	}
}

// TestChartGeneratedSecretsWarnAboutRenderOnlyPipelines covers the operator's
// only chance to notice: nothing fails at sync time, and the split only shows
// up as mTLS and resolve errors after an unrelated restart.
func TestChartGeneratedSecretsWarnAboutRenderOnlyPipelines(t *testing.T) {
	notes := renderNotes(t, "artifactDaemon.tls.enabled=true")
	for _, want := range []string{
		"secrets.signingKeySecret",
		"artifactDaemon.tls.existingSecret",
		"artifactDaemon.resolveCapability.existingSecret",
		"lookup",
		"Argo CD",
		"read this material once at process start",
	} {
		if !strings.Contains(notes, want) {
			t.Errorf("NOTES does not mention %q:\n%s", want, notes)
		}
	}

	// Silence once every Secret is externally managed, so the warning keeps
	// meaning something.
	quiet := renderNotes(t, append([]string{"artifactDaemon.tls.enabled=true"}, externallyManagedSecrets...)...)
	if strings.Contains(quiet, "generated by this chart") {
		t.Errorf("NOTES still warns about generated secrets when all three are external:\n%s", quiet)
	}

	// The resolve-capability key renders under artifactDaemon.enabled alone,
	// which the Kubernetes runtime requires — so the warning must appear even
	// with TLS off, where it is easiest to assume it does not apply.
	plaintext := renderNotes(t)
	if !strings.Contains(plaintext, "artifactDaemon.resolveCapability.existingSecret") {
		t.Errorf("NOTES omits the resolve capability key with TLS disabled:\n%s", plaintext)
	}
	if strings.Contains(plaintext, "artifactDaemon.tls.existingSecret") {
		t.Errorf("NOTES names the daemon TLS secret when TLS is disabled and none is rendered:\n%s", plaintext)
	}
}

// TestGeneratedDaemonSecretsSurviveUninstall documents the narrow thing
// helm.sh/resource-policy: keep buys — a reinstall's lookup finds the same
// material — without implying it defends against re-application.
func TestGeneratedDaemonSecretsSurviveUninstall(t *testing.T) {
	manifests := renderChart(t, "artifactDaemon.tls.enabled=true")
	for _, nameSuffix := range []string{
		"test-release-concourse-jetbridge-artifact-daemon-tls",
		"test-release-concourse-jetbridge-artifact-daemon-resolve-capability",
	} {
		secret := findSecret(t, manifests, nameSuffix)
		if secret.Metadata.Annotations["helm.sh/resource-policy"] != "keep" {
			t.Errorf("Secret %s is not annotated helm.sh/resource-policy: keep: %+v",
				nameSuffix, secret.Metadata.Annotations)
		}
	}
}
