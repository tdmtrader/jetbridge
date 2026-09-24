package tests

import (
	"strings"
	"testing"
)

// The Run contract has its own activation marker, and the chart turns it on at
// every deploy: each web node writes web.pipelineRunActivationEpoch into that
// marker at startup. The epoch is the Run contract's own, so it must reach the
// binary on its own flag, without the Hangar output plane.
//
// These use durable_store_test.go's render, which fails when helm is missing
// rather than skipping. A skip is not a pass, and this is the only test that
// watches the default.

const runActivationFlag = "--pipeline-run-activation-epoch"

// webContainerArgs returns the args the -web Deployment hands its container,
// failing rather than returning nothing if either is absent -- otherwise the
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

func runActivationArgs(args []string) []string {
	var found []string
	for _, arg := range args {
		if arg == runActivationFlag || strings.HasPrefix(arg, runActivationFlag+"=") {
			found = append(found, arg)
		}
	}
	return found
}

// Every deploy turns the Run contract on, and the default render needs no
// output plane to do it. Decoded strictly, so a whitespace chomp that broke
// the Deployment cannot pass.
func TestRunActivationIsOnAtEveryDeploy(t *testing.T) {
	web := strictWebDeployment(t, render(t))
	args := web.Spec.Template.Spec.Containers[0].Args
	got := runActivationArgs(args)
	if len(got) != 1 || got[0] != runActivationFlag+"=1" {
		t.Errorf("the default render must activate the Run contract at epoch 1 exactly once, got %v", got)
	}
	for _, arg := range args {
		if strings.HasPrefix(arg, "--kubernetes-hangar-output-activation-epoch") {
			t.Errorf("the default render activated the Run contract through the Hangar epoch: %s", arg)
		}
	}
}

// The Run epoch and the Hangar epoch are rendered from their own values: an
// operator rotating one does not move the other.
func TestRunActivationEpochIsIndependentOfTheHangarEpoch(t *testing.T) {
	web := strictWebDeployment(t, renderOutput(t, "hangarOutput.webEnabled=true", "web.pipelineRunActivationEpoch=3"))
	args := web.Spec.Template.Spec.Containers[0].Args
	for _, want := range []string{
		runActivationFlag + "=3",
		"--kubernetes-hangar-output-enabled",
		"--kubernetes-hangar-output-capture-enabled",
		"--kubernetes-hangar-output-activation-epoch=7",
	} {
		found := false
		for _, arg := range args {
			found = found || arg == want
		}
		if !found {
			t.Errorf("web args lack %s: %v", want, args)
		}
	}
}

// Zero is the one supported way to stop admission; it renders no flag.
func TestRunActivationZeroRendersNoFlag(t *testing.T) {
	if got := runActivationArgs(webContainerArgs(t, render(t, "web.pipelineRunActivationEpoch=0"))); len(got) != 0 {
		t.Errorf("web.pipelineRunActivationEpoch=0 must render no activation flag, got %v", got)
	}
}

func TestRunActivationRefusesANegativeEpoch(t *testing.T) {
	got := renderHangarError(t, "web.pipelineRunActivationEpoch=-1")
	if !strings.Contains(got, "web.pipelineRunActivationEpoch must be zero") {
		t.Errorf("a negative epoch failed for the wrong reason: %s", got)
	}
}

// The retired switch is refused, not ignored: a values file still setting it
// would otherwise deploy with admission on at the default epoch, whatever the
// operator meant by it. Either value fails and names the replacement.
func TestRetiredRunCreationSwitchFailsTheRender(t *testing.T) {
	for _, set := range []string{"web.enablePipelineRunCreation=true", "web.enablePipelineRunCreation=false"} {
		t.Run(set, func(t *testing.T) {
			got := renderHangarError(t, set)
			for _, want := range []string{"web.enablePipelineRunCreation has been removed", "web.pipelineRunActivationEpoch"} {
				if !strings.Contains(got, want) {
					t.Errorf("want %q in the refusal: %s", want, got)
				}
			}
		})
	}
}
