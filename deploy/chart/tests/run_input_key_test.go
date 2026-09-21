package tests

import (
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/yaml"
)

func TestRunInputKeyIsWebOnlyAndExplicit(t *testing.T) {
	if strings.Contains(render(t), "run-input-signing") {
		t.Fatal("default chart configured an input signing key")
	}
	manifests := renderOutput(t, "hangarOutput.webEnabled=true", "web.runInputSigningKeySecret=review-input-key")
	var web appsv1.Deployment
	for _, doc := range strings.Split(manifests, "\n---") {
		var candidate appsv1.Deployment
		if yaml.Unmarshal([]byte(doc), &candidate) == nil && candidate.Kind == "Deployment" && strings.HasSuffix(candidate.Name, "-web") {
			web = candidate
		}
	}
	if len(web.Spec.Template.Spec.Containers) == 0 {
		t.Fatal("chart has no web container")
	}
	container := web.Spec.Template.Spec.Containers[0]
	foundArg, foundMount, foundVolume := false, false, false
	for _, arg := range container.Args {
		foundArg = foundArg || arg == "--run-input-signing-key=/etc/concourse/run-input-signing/input.key"
	}
	for _, mount := range container.VolumeMounts {
		if mount.Name == "run-input-signing" {
			foundMount = mount.ReadOnly && mount.MountPath == "/etc/concourse/run-input-signing"
		}
	}
	for _, volume := range web.Spec.Template.Spec.Volumes {
		if volume.Name == "run-input-signing" {
			foundVolume = volume.Secret != nil && volume.Secret.SecretName == "review-input-key" && len(volume.Secret.Items) == 1 && volume.Secret.Items[0].Key == "input.key" && volume.Secret.Items[0].Path == "input.key"
		}
	}
	if !foundArg || !foundMount || !foundVolume || strings.Count(manifests, "secretName: review-input-key") != 1 {
		t.Fatal("Run input key did not reach only the web process through a read-only matching volume")
	}
}

func TestRunInputKeyRequiresCaptureAndSeparateSecret(t *testing.T) {
	for _, sets := range [][]string{
		{"web.runInputSigningKeySecret=review-input-key"},
		{"hangarOutput.webEnabled=true", "web.runInputSigningKeySecret=op-capability-key"},
		{"hangarOutput.webEnabled=true", "web.runInputSigningKeySecret=op-output-materialize"},
	} {
		if got := renderOutputError(t, sets...); !strings.Contains(got, "runInputSigningKeySecret") {
			t.Fatalf("wrong configuration refusal: %s", got)
		}
	}
}
