package tests

import (
	"slices"
	"strings"
	"testing"
)

// kubernetes.stepPrivilege is a mode (ADR-0008): any, the binary's default,
// admits a step pod's privilege as its task or resource type asks; granted
// admits it only under a step pod grant that declares privileged=true. Only
// granted is passed to the web, so an any render is the render from before the
// value existed, and a chart synced ahead of its image cannot hand an older web
// a flag it refuses.

func TestTheDefaultStepPrivilegeRendersNoFlag(t *testing.T) {
	for _, sets := range [][]string{nil, {"kubernetes.stepPrivilege=any"}} {
		for _, arg := range webArgs(t, render(t, sets...)) {
			if strings.HasPrefix(arg, "--kubernetes-step-privilege") {
				t.Errorf("render with %v passes %s", sets, arg)
			}
		}
	}
	// any renders byte for byte what leaving the value out renders.
	if render(t) != render(t, "kubernetes.stepPrivilege=any") {
		t.Error("an explicit any renders differently from the default")
	}
}

func TestTheGrantedStepPrivilegeRendersItsFlagOnWeb(t *testing.T) {
	out := render(t, "kubernetes.stepPrivilege=granted")
	if !slices.Contains(webArgs(t, out), "--kubernetes-step-privilege=granted") {
		t.Errorf("the web args lack --kubernetes-step-privilege=granted: %v", webArgs(t, out))
	}
	if n := strings.Count(out, "--kubernetes-step-privilege"); n != 1 {
		t.Errorf("render passes the privilege mode %d times, want once (on web)", n)
	}
}

// granted with no privileged grant is legal: nothing may run privileged.
func TestGrantedNeedsNoPrivilegedGrant(t *testing.T) {
	render(t, "kubernetes.stepPrivilege=granted",
		"kubernetes.stepPodGrants[0].name=release",
		"kubernetes.stepPodGrants[0].owner=main/jetbridge/release",
		"kubernetes.stepPodGrants[0].serviceAccount=jetbridge-releaser")
}

// The schema's enum refuses any other mode. The failure is checked for the
// key's name only: helm v3 (CI) and v4 (local) word schema errors differently.
func TestAnUnknownStepPrivilegeFailsTheRender(t *testing.T) {
	for _, mode := range []string{"strict", "true", "Granted"} {
		out := strings.ReplaceAll(renderHangarError(t, "kubernetes.stepPrivilege="+mode), "/", ".")
		if !strings.Contains(out, "kubernetes.stepPrivilege") {
			t.Errorf("the failure for %q does not name kubernetes.stepPrivilege: %s", mode, firstLines(out, 3))
		}
	}
}
