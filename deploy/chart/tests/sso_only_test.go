package tests

// A deployment may sign everyone in through an identity provider and keep no
// local user at all. The main team still needs an owner, which the web
// refuses to start without; the chart names that before anything deploys.

import (
	"os/exec"
	"strings"
	"testing"
)

func TestAnSSOOnlyDeploymentRendersWithoutLocalUsers(t *testing.T) {
	args := webArgs(t, render(t,
		"web.localUsers=",
		"web.mainTeamLocalUser=",
		"web.env[0].name=CONCOURSE_MAIN_TEAM_OIDC_GROUP",
		"web.env[0].value=platform-admins",
	))
	for _, arg := range args {
		if strings.HasPrefix(arg, "--add-local-user") || strings.HasPrefix(arg, "--main-team-local-user") {
			t.Errorf("an SSO-only render passes %s", arg)
		}
	}
}

func TestTheRenderFailsWhenTheMainTeamHasNoOwner(t *testing.T) {
	cmd := exec.Command("helm", "template", "jb", "deploy/chart",
		"-f", "deploy/chart/tests/testdata/required-values.yaml",
		"--set", "web.localUsers=",
		"--set", "web.mainTeamLocalUser=",
	)
	cmd.Dir = repoRoot(t)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("a render with no main team owner succeeded")
	}
	for _, want := range []string{"web.mainTeamLocalUser", "CONCOURSE_MAIN_TEAM_"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("the failure does not name %s:\n%s", want, out)
		}
	}
}
