package tests

import (
	"os/exec"
	"testing"
)

// render runs `helm template` with the given --set overrides in helm's
// default release namespace.
func render(t *testing.T, sets ...string) string {
	t.Helper()
	return renderInNamespace(t, "", sets...)
}

// renderInNamespace is render with an explicit --namespace, for templates
// whose output must follow .Release.Namespace: only a name other than
// "default" can tell that apart from a hardcoded one.
func renderInNamespace(t *testing.T, namespace string, sets ...string) string {
	t.Helper()

	args := []string{"template", "jb", "deploy/chart", "-f", "deploy/chart/tests/testdata/required-values.yaml"}
	if namespace != "" {
		args = append(args, "--namespace", namespace)
	}
	for _, s := range sets {
		args = append(args, "--set", s)
	}

	cmd := exec.Command("helm", args...)
	cmd.Dir = repoRoot(t)

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helm template %v failed: %v\n%s", sets, err, out)
	}

	return string(out)
}
