package tests

import (
	"testing"
)

// Public creation of durable pipeline runs is held until an operator turns it
// on deliberately, and the chart is where that decision is recorded. Two
// things have to be true at once: a freshly rendered chart must produce the
// refusing state, and the committed value must actually reach the binary.
//
// These use durable_store_test.go's render, which fails when helm is missing
// rather than skipping. A skip is not a pass, and this is the only test that
// watches the default.

const runCreationFlag = "--enable-pipeline-run-creation"

// webContainerArgs returns the args the -web Deployment hands its container,
// failing rather than returning nothing if either is absent -- otherwise both
// tests below would pass on a chart that rendered no web container at all.
func webContainerArgs(t *testing.T, manifests string) []string {
	t.Helper()

	web := findDeployment(t, manifests, "-web")
	if len(web.Spec.Template.Spec.Containers) == 0 {
		t.Fatal("the -web Deployment has no containers; this test is watching nothing")
	}

	args := web.Spec.Template.Spec.Containers[0].Args
	if len(args) == 0 {
		t.Fatal("the web container renders no args; this test is watching nothing")
	}
	return args
}

func TestRunCreationIsOffByDefault(t *testing.T) {
	for _, arg := range webContainerArgs(t, render(t)) {
		if arg == runCreationFlag {
			t.Errorf("the default render admits public run creation (%s); it must be an explicit operator action", runCreationFlag)
		}
	}
}

func TestRunCreationRendersWhenEnabled(t *testing.T) {
	args := webContainerArgs(t, render(t, "web.enablePipelineRunCreation=true"))

	for _, arg := range args {
		if arg == runCreationFlag {
			return
		}
	}
	t.Errorf("web.enablePipelineRunCreation=true rendered no %s; got %v", runCreationFlag, args)
}
