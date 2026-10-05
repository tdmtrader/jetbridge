package tests

import (
	"os/exec"
	"testing"

	corev1 "k8s.io/api/core/v1"
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

// Every web pod mounts the one signing-key Secret the operator names, at
// /keys, where CONCOURSE_SESSION_SIGNING_KEY points. An emptyDir there renders
// and passes every other test here, and then each pod has no key at all.
// Nothing else mounts the volume: no init container may write a key into it.
func TestWebMountsTheNamedSigningKeySecretAtKeys(t *testing.T) {
	for _, secret := range []string{"test-signing-key", "operator-signing-key"} {
		t.Run(secret, func(t *testing.T) {
			spec := strictWebDeployment(t, render(t, "secrets.signingKeySecret="+secret)).Spec.Template.Spec
			var keys *corev1.Volume
			for i := range spec.Volumes {
				if spec.Volumes[i].Name == "keys" {
					keys = &spec.Volumes[i]
				}
			}
			if keys == nil {
				t.Fatal("web has no keys volume")
			}
			if keys.Secret == nil || keys.Secret.SecretName != secret || keys.EmptyDir != nil {
				t.Fatalf("the keys volume is not only Secret %s: secret=%+v emptyDir=%+v", secret, keys.Secret, keys.EmptyDir)
			}
			mounted := false
			for _, container := range spec.Containers {
				for _, mount := range container.VolumeMounts {
					mounted = mounted || (mount.Name == "keys" && mount.MountPath == "/keys")
				}
				for _, env := range container.Env {
					if env.Name == "CONCOURSE_SESSION_SIGNING_KEY" && env.Value != "/keys/session_signing_key" {
						t.Errorf("%s reads the key from %s, not /keys/session_signing_key", container.Name, env.Value)
					}
				}
			}
			if !mounted {
				t.Error("no web container mounts the keys volume at /keys")
			}
			for _, container := range spec.InitContainers {
				for _, mount := range container.VolumeMounts {
					if mount.Name == "keys" {
						t.Errorf("init container %s mounts the keys volume; nothing may write the key", container.Name)
					}
				}
			}
		})
	}
}

// migrate-db is the web Deployment's first init container in every mode.
// The self-upgrade job re-images every web container, migrate-db included,
// and home-infra's Argo Application ignores differences at
// /spec/template/spec/initContainers/0/image so selfHeal does not revert it.
// An init container inserted ahead of migrate-db would move that pointer onto
// the newcomer: selfHeal would revert migrate-db, the container that applies
// schema migrations, to the chart's image after every release.
func TestMigrateDBIsTheWebDeploymentsFirstInitContainer(t *testing.T) {
	rollout := exec.Command("helm", "template", "concourse", "deploy/chart", "--namespace", "cicd",
		"-f", "deploy/chart/tests/testdata/concourse-home-rollout.yaml")
	rollout.Dir = repoRoot(t)
	home, err := rollout.CombinedOutput()
	if err != nil {
		t.Fatalf("the concourse.home rollout values do not render: %v\n%s", err, home)
	}
	for name, manifests := range map[string]string{
		"default":        render(t),
		"output facet":   renderOutput(t),
		"concourse.home": string(home),
		"web capture":    renderOutput(t, "hangarOutput.webEnabled=true", "web.runInputSigningKeySecret=review-input-key"),
	} {
		t.Run(name, func(t *testing.T) {
			initContainers := strictWebDeployment(t, manifests).Spec.Template.Spec.InitContainers
			if len(initContainers) == 0 || initContainers[0].Name != "migrate-db" {
				var names []string
				for _, container := range initContainers {
					names = append(names, container.Name)
				}
				t.Fatalf("web's init containers are %v; migrate-db must be first, where home-infra's ignoreDifferences pointer initContainers/0/image expects it", names)
			}
		})
	}
}
