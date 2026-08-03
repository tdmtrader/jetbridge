package tests

import "testing"

const (
	testWebImageDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testWebSource      = "0123456789abcdef0123456789abcdef01234567"
)

func TestDigestPinnedSharedImageRendersForWebAndArtifactDaemon(t *testing.T) {
	manifests := renderChart(t,
		"image.repository=registry.home/jetbridge",
		"image.digest="+testWebImageDigest,
		"image.sourceCommit="+testWebSource,
	)
	wantImage := "registry.home/jetbridge@" + testWebImageDigest
	for _, workload := range []struct {
		name        string
		image       string
		annotations map[string]string
		template    map[string]string
	}{
		{"web", findDeployment(t, manifests, "-web").Spec.Template.Spec.Containers[0].Image, findDeployment(t, manifests, "-web").Metadata.Annotations, findDeployment(t, manifests, "-web").Spec.Template.Metadata.Annotations},
		{"artifact daemon", findDaemonSet(t, manifests, "-artifact-daemon").Spec.Template.Spec.Containers[0].Image, findDaemonSet(t, manifests, "-artifact-daemon").Metadata.Annotations, findDaemonSet(t, manifests, "-artifact-daemon").Spec.Template.Metadata.Annotations},
	} {
		if workload.image != wantImage {
			t.Errorf("%s image = %q, want immutable %q", workload.name, workload.image, wantImage)
		}
		for _, annotations := range []map[string]string{workload.annotations, workload.template} {
			if annotations["concourse.ci/source-commit"] != testWebSource || annotations["concourse.ci/image-digest"] != wantImage {
				t.Errorf("%s annotations = %#v, want source/image coupling", workload.name, annotations)
			}
			if _, exists := annotations["concourse.ci/tested-rc-image"]; exists {
				t.Errorf("%s exposes a pre-test candidate as tested authority: %#v", workload.name, annotations)
			}
		}
	}
}

func TestDigestPinnedSharedImageRejectsIncompleteCoupling(t *testing.T) {
	for _, set := range [][]string{
		{"image.repository=registry.home/jetbridge", "image.digest=" + testWebImageDigest},
		{"image.repository=registry.home/jetbridge", "image.digest=sha256:INVALID", "image.sourceCommit=" + testWebSource},
		{"image.repository=registry.home/jetbridge", "image.digest=" + testWebImageDigest, "image.sourceCommit=not-a-commit"},
	} {
		if output := renderChartFailure(t, set...); output == "" {
			t.Fatal("digest-pinned image configuration unexpectedly rendered")
		}
	}
}
