package tests

// The web refuses to start without --encryption-key, and migrate-db runs the
// migrations under the same key, so the chart names one source for it and
// hands it to both containers: secrets.encryptionKeySecret, the one source
// since web.env went (ADR-0008).

import (
	"os/exec"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func encryptionKeyEnv(t *testing.T, container corev1.Container) *corev1.EnvVar {
	t.Helper()
	var found *corev1.EnvVar
	for i := range container.Env {
		if container.Env[i].Name == "CONCOURSE_ENCRYPTION_KEY" {
			if found != nil {
				t.Fatalf("%s sets CONCOURSE_ENCRYPTION_KEY twice", container.Name)
			}
			found = &container.Env[i]
		}
	}
	return found
}

func TestTheWebAndMigrateDBReadTheEncryptionKeyFromTheNamedSecret(t *testing.T) {
	spec := strictWebDeployment(t, render(t, "secrets.encryptionKeySecret=operator-encryption-key")).Spec.Template.Spec
	seen := 0
	for _, container := range append(spec.InitContainers, spec.Containers...) {
		if container.Name != "migrate-db" && container.Name != "concourse-web" {
			continue
		}
		seen++
		env := encryptionKeyEnv(t, container)
		if env == nil || env.ValueFrom == nil || env.ValueFrom.SecretKeyRef == nil {
			t.Fatalf("%s does not read CONCOURSE_ENCRYPTION_KEY from a Secret: %+v", container.Name, env)
		}
		ref := env.ValueFrom.SecretKeyRef
		if ref.Name != "operator-encryption-key" || ref.Key != "encryption-key" {
			t.Fatalf("%s reads the key from %s/%s; want operator-encryption-key/encryption-key", container.Name, ref.Name, ref.Key)
		}
	}
	if seen != 2 {
		t.Fatalf("found %d of migrate-db and concourse-web", seen)
	}
}

func TestTheRenderNamesTheEncryptionKeyWhenNoSourceSetsIt(t *testing.T) {
	for name, sets := range map[string][]string{
		"unset": {"secrets.encryptionKeySecret="},
	} {
		t.Run(name, func(t *testing.T) {
			args := []string{"template", "jb", "deploy/chart", "-f", "deploy/chart/tests/testdata/required-values.yaml"}
			for _, s := range sets {
				args = append(args, "--set", s)
			}
			cmd := exec.Command("helm", args...)
			cmd.Dir = repoRoot(t)
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("render %v succeeded; the chart must refuse it naming secrets.encryptionKeySecret", sets)
			}
			if !strings.Contains(string(out), "secrets.encryptionKeySecret") {
				t.Fatalf("the refusal does not name secrets.encryptionKeySecret:\n%s", out)
			}
		})
	}
}
