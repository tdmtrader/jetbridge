package tests

// kubernetes.baseResourceTypes is how a deployment points a resource type at
// its own image: a custom registry-image, or a type such as git-with-ado that
// pipelines name without declaring it under resource_types. Each entry is one
// --kubernetes-base-resource-type flag on the web.

import (
	"slices"
	"strings"
	"testing"
)

func webArgs(t *testing.T, out string) []string {
	t.Helper()
	for _, container := range strictWebDeployment(t, out).Spec.Template.Spec.Containers {
		if container.Name == "concourse-web" {
			return container.Args
		}
	}
	t.Fatal("the web Deployment has no concourse-web container")
	return nil
}

func TestEachBaseResourceTypeIsOneWebFlag(t *testing.T) {
	args := webArgs(t, render(t,
		"kubernetes.baseResourceTypes.registry-image=my-registry/registry-image-resource:1.12",
		"kubernetes.baseResourceTypes.git-with-ado=my-registry/git-resource-ado:1.4",
	))
	for _, want := range []string{
		"--kubernetes-base-resource-type=registry-image=my-registry/registry-image-resource:1.12",
		"--kubernetes-base-resource-type=git-with-ado=my-registry/git-resource-ado:1.4",
	} {
		if !slices.Contains(args, want) {
			t.Errorf("the web args lack %s: %v", want, args)
		}
	}
}

func TestNoBaseResourceTypeRendersNoFlag(t *testing.T) {
	for _, arg := range webArgs(t, render(t)) {
		if strings.HasPrefix(arg, "--kubernetes-base-resource-type") {
			t.Errorf("the default render passes %s", arg)
		}
	}
}
