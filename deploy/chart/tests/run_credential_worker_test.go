package tests

import (
	"strings"
	"testing"
)

// Credential handoff delivers the owner's session credentials into the image
// the Run's result producer names, which a template author chooses. The web
// process refuses every handoff unless the operator pins the worker image by
// digest, and this is where the operator does it.

const credentialWorkerFlag = "--run-credential-worker-image="

func credentialWorkerArgs(t *testing.T, manifests string) []string {
	t.Helper()
	var images []string
	for _, arg := range strictWebDeployment(t, manifests).Spec.Template.Spec.Containers[0].Args {
		if image, ok := strings.CutPrefix(arg, credentialWorkerFlag); ok {
			images = append(images, image)
		}
	}
	return images
}

func TestRunCredentialWorkerImagesRenderIntoWebFlags(t *testing.T) {
	first := "registry.example/review-worker@sha256:" + strings.Repeat("ab", 32)
	second := "registry.example:5000/team/review-worker:v2@sha256:" + strings.Repeat("cd", 32)
	intake := []string{"hangarOutput.webEnabled=true", "web.runInputSigningKeySecret=review-input-key"}

	if got := credentialWorkerArgs(t, render(t)); len(got) != 0 {
		t.Errorf("default render pins %v", got)
	}
	if got := credentialWorkerArgs(t, renderOutput(t, intake...)); len(got) != 0 {
		t.Errorf("an unset value rendered pins %v; unset must leave handoff refused", got)
	}
	got := credentialWorkerArgs(t, renderOutput(t, append(intake,
		"web.runCredentialWorkerImages[0]="+first,
		"web.runCredentialWorkerImages[1]="+second)...))
	if !equalStrings(got, []string{first, second}) {
		t.Errorf("web pins = %v, want %v", got, []string{first, second})
	}
}

func TestRunCredentialWorkerImagesMustBeDigests(t *testing.T) {
	intake := []string{"hangarOutput.webEnabled=true", "web.runInputSigningKeySecret=review-input-key"}
	for _, image := range []string{
		"registry.example/review-worker:latest",
		"registry.example/review-worker@sha256:abc",
		"docker:///registry.example/review-worker@sha256:" + strings.Repeat("ab", 32),
	} {
		got := renderOutputError(t, append(intake, "web.runCredentialWorkerImages[0]="+image)...)
		if !strings.Contains(got, "web.runCredentialWorkerImages") {
			t.Errorf("%q: refusal does not name web.runCredentialWorkerImages: %s", image, got)
		}
	}
	// Pins without the intake key configure nothing, which would read as a
	// deployment that delivers credentials.
	got := renderOutputError(t, "web.runCredentialWorkerImages[0]=registry.example/review-worker@sha256:"+strings.Repeat("ab", 32))
	if !strings.Contains(got, "web.runCredentialWorkerImages") {
		t.Errorf("pins without intake: %s", got)
	}
}
