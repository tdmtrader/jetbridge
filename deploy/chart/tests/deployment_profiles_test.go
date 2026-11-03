package tests

// Every other render test here renders one deployment's shape, and the web
// ignores an environment variable that names no flag. So 0.4.0 removed the
// auth connectors, the vendor credential managers and idtoken while every test
// passed: a deployment that configured them through web.env would have started
// without them. A deployment profile is the values of one real deployment shape
// this chart is meant to serve. Each must render, and every CONCOURSE_*
// variable and flag its web and migrate containers receive must be one the
// binary accepts, asked of the binary itself as flag_drift_test.go does.
// Between them the profiles set every auth connector and credential manager
// the 0.4.1 release notes restored.

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// expectedDeploymentProfiles is the floor. Removing a profile drops a shape
// some deployment runs from every check below, so it is a number somebody
// lowers on purpose, not a file that quietly goes.
const expectedDeploymentProfiles = 11

// deploymentProfiles renders each profile under testdata/profiles over the
// required values, and concourse.home's rollout values the way the other
// tests render them: alone, in its own namespace.
func deploymentProfiles(t *testing.T, root string) map[string]func(*testing.T) string {
	t.Helper()
	profiles := map[string]func(*testing.T) string{
		"concourse-home": renderRollout,
	}
	files, err := filepath.Glob(filepath.Join(root, "deploy/chart/tests/testdata/profiles/*.yaml"))
	if err != nil {
		t.Fatalf("list profiles: %v", err)
	}
	for _, file := range files {
		values, err := filepath.Rel(root, file)
		if err != nil {
			t.Fatalf("profile path: %v", err)
		}
		profiles[strings.TrimSuffix(filepath.Base(file), ".yaml")] = func(t *testing.T) string {
			t.Helper()
			cmd := exec.Command("helm", "template", "jb", "deploy/chart",
				"-f", "deploy/chart/tests/testdata/required-values.yaml",
				"-f", values,
			)
			cmd.Dir = root
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("helm template -f %s failed: %v\n%s", values, err, out)
			}
			return string(out)
		}
	}
	return profiles
}

// containerSubcommand is the concourse subcommand a container runs: its first
// argument that is not a flag.
func containerSubcommand(container corev1.Container) string {
	for _, arg := range append(append([]string{}, container.Command...), container.Args...) {
		if !strings.HasPrefix(arg, "-") && !strings.Contains(arg, "/") {
			return arg
		}
	}
	return ""
}

// acceptedSettings is every flag the subcommand accepts and the variable the
// web reads for it: twentythousandtonnesofcrudeoil gives each flag exactly
// CONCOURSE_ and its name upper-cased with dashes as underscores.
func acceptedSettings(flags map[string]bool) map[string]bool {
	accepted := map[string]bool{}
	for flag := range flags {
		accepted["--"+flag] = true
		accepted["CONCOURSE_"+strings.ToUpper(strings.ReplaceAll(flag, "-", "_"))] = true
	}
	return accepted
}

// unacceptedSettings names each CONCOURSE_* variable and each flag the
// container receives that the subcommand does not accept.
func unacceptedSettings(container corev1.Container, accepted map[string]bool) []string {
	var unknown []string
	for _, env := range container.Env {
		if strings.HasPrefix(env.Name, "CONCOURSE_") && !accepted[env.Name] {
			unknown = append(unknown, env.Name)
		}
	}
	for _, arg := range container.Args {
		if !strings.HasPrefix(arg, "--") {
			continue
		}
		if flag, _, _ := strings.Cut(arg, "="); !accepted[flag] {
			unknown = append(unknown, arg)
		}
	}
	sort.Strings(unknown)
	return unknown
}

func TestEveryDeploymentProfileRendersSettingsTheBinaryAccepts(t *testing.T) {
	root := repoRoot(t)
	profiles := deploymentProfiles(t, root)
	if len(profiles) < expectedDeploymentProfiles {
		t.Fatalf("%d deployment profiles, expected at least %d. Each is the shape of a deployment "+
			"this chart serves; one that goes stops guarding that deployment's settings. Lower "+
			"expectedDeploymentProfiles only on purpose.", len(profiles), expectedDeploymentProfiles)
	}

	binary := filepath.Join(t.TempDir(), "concourse")
	build := exec.Command("go", "build", "-o", binary, "./cmd/concourse")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build concourse: %v\n%s", err, out)
	}
	accepted := map[string]map[string]bool{
		"web":     acceptedSettings(binaryFlags(t, binary, "web")),
		"migrate": acceptedSettings(binaryFlags(t, binary, "migrate")),
	}

	names := make([]string, 0, len(profiles))
	for name := range profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			spec := strictWebDeployment(t, profiles[name](t)).Spec.Template.Spec
			containers := append(append([]corev1.Container{}, spec.InitContainers...), spec.Containers...)
			checked := map[string]int{}
			for _, container := range containers {
				subcommand := containerSubcommand(container)
				if accepted[subcommand] == nil {
					continue
				}
				checked[subcommand]++
				if unknown := unacceptedSettings(container, accepted[subcommand]); len(unknown) > 0 {
					t.Errorf("profile %s hands container %s settings `concourse %s` does not accept: %s\n"+
						"The web ignores a variable that names no flag, so a deployment configured this "+
						"way starts without the setting. Restore the flag, or drop the setting from the "+
						"chart and this profile on purpose.",
						name, container.Name, subcommand, strings.Join(unknown, " "))
				}
			}
			if checked["web"] != 1 || checked["migrate"] != 1 {
				t.Fatalf("profile %s rendered %d web and %d migrate containers, want one of each",
					name, checked["web"], checked["migrate"])
			}
		})
	}
}

// A profile under testdata/profiles that the test cannot find is no profile.
func TestDeploymentProfilesAreValuesFiles(t *testing.T) {
	root := repoRoot(t)
	entries, err := os.ReadDir(filepath.Join(root, "deploy/chart/tests/testdata/profiles"))
	if err != nil {
		t.Fatalf("read profiles: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			t.Errorf("testdata/profiles/%s is not a .yaml values file; the profile check skips it", entry.Name())
		}
	}
}
