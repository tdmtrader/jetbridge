package tests

// One artifact daemon key, secrets.artifactDaemonKeySecret, signs every
// resolve capability and every Hangar warrant; the two MACs are
// domain-separated, so one Secret feeds both. It is required on every install.
//
// The chart never generates it. `lookup` needs a live cluster and returns
// empty under `helm template`, which is Argo CD's render mode, so a generated
// key is fresh on EVERY reconcile; neither process re-reads the file, so the
// rotation is invisible until the first asymmetric restart, after which each
// side rejects the other's tokens and every input fetch 403s. The operator
// creates the Secret once, out of band, and the chart only references it.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	daemonKeyPath = "/etc/concourse/artifact-daemon-key/artifact-daemon.key"
	daemonKeyName = "test-artifact-daemon-key" // testdata/required-values.yaml
)

// Nothing in the chart may mint key material, with or without the output
// plane, and with the chart generating the daemon's TLS Secret.
func TestChartNeverGeneratesTheArtifactDaemonKey(t *testing.T) {
	for _, sets := range [][]string{
		nil,
		{"artifactDaemon.tls.source=generated", "artifactDaemon.tls.existingSecret="},
		append([]string{"artifactDaemon.tls.source=generated", "artifactDaemon.tls.existingSecret="}, baseControlSets...),
	} {
		out := render(t, sets...)
		for _, data := range []string{"artifact-daemon.key:", "resolve.key:", "hangar.key"} {
			if strings.Contains(out, data) {
				t.Errorf("render %v carries %q; the key must be created out of band, not rendered", sets, data)
			}
		}
	}
}

func TestTheArtifactDaemonKeyIsRequired(t *testing.T) {
	out := renderHangarError(t, "secrets.artifactDaemonKeySecret=")
	if !strings.Contains(out, "secrets.artifactDaemonKeySecret is required") {
		t.Fatalf("missing actionable prerequisite error: %s", out)
	}
}

// Web and daemon read the one Secret, mounted once each. Without the output
// plane each is handed only the resolve flag: the daemon refuses --hangar-key
// without --execution-control (ADR-0010). With it, each is handed the same
// file as the Hangar key too.
func TestTheOneKeyFeedsResolveAndWarrants(t *testing.T) {
	for _, tc := range []struct {
		name   string
		sets   []string
		hangar bool
	}{
		{"no output plane", nil, false},
		{"execution control", baseControlSets, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := render(t, tc.sets...)
			daemon := objectNamed(t, out, "DaemonSet", "-"+outputDaemonComponent)
			web := objectNamed(t, out, "Deployment", "-"+webComponent)
			for _, side := range []struct {
				name, body, resolve, hangar string
			}{
				{"daemon", daemon.body, "--resolve-capability-key=", "--hangar-key="},
				{"web", web.body, "--kubernetes-artifact-daemon-resolve-capability-key=", "--kubernetes-hangar-key="},
			} {
				if strings.Count(side.body, side.resolve+daemonKeyPath) != 1 {
					t.Errorf("%s is not handed %s%s exactly once", side.name, side.resolve, daemonKeyPath)
				}
				want := 0
				if tc.hangar {
					want = 1
				}
				if got := strings.Count(side.body, side.hangar); got != want || (tc.hangar && !strings.Contains(side.body, side.hangar+daemonKeyPath)) {
					t.Errorf("%s is handed %s %d times, want %d, from %s", side.name, side.hangar, got, want, daemonKeyPath)
				}
				if strings.Count(side.body, "mountPath: /etc/concourse/artifact-daemon-key\n") != 1 ||
					strings.Count(side.body, "secretName: "+daemonKeyName) != 1 {
					t.Errorf("%s does not mount the artifact daemon key Secret exactly once", side.name)
				}
			}
		})
	}
}

// Each removed key source fails the render by name (the closed schema), so a
// values file still carrying one cannot silently drop it.
func TestTheRemovedKeySourcesAreRefused(t *testing.T) {
	for removed, name := range map[string]string{
		"artifactDaemon.resolveCapability.existingSecret=old-resolve-key": "resolveCapability",
		"artifactDaemon.hangar.keySecret=old-hangar-key":                  "keySecret",
		"artifactDaemon.hangar.allowGeneratedKey=true":                    "allowGeneratedKey",
	} {
		if out := renderHangarError(t, removed); !strings.Contains(out, name) {
			t.Errorf("%s was refused without naming %s:\n%s", removed, name, firstLines(out, 5))
		}
	}
}

func TestTheResolveCapabilityTTLIsNotRendered(t *testing.T) {
	if strings.Contains(render(t), "--kubernetes-artifact-daemon-resolve-capability-ttl") {
		t.Fatal("the chart renders a TTL flag the web no longer parses")
	}
}

// The cutover removes artifactDaemon.hangar's keySecret. A values file left
// with a bare `hangar:` is null, and Helm drops the whole mapping, defaults
// included; the chart must read that as the default TTL, not dereference nil.
func TestAnEmptiedHangarMappingRendersTheDefaultTTL(t *testing.T) {
	values := filepath.Join(t.TempDir(), "hangar-null.yaml")
	if err := os.WriteFile(values, []byte("artifactDaemon:\n  hangar:\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"template", "jb", "deploy/chart",
		"-f", "deploy/chart/tests/testdata/required-values.yaml", "-f", values}
	for _, s := range baseControlSets {
		args = append(args, "--set", s)
	}
	cmd := exec.Command("helm", args...)
	cmd.Dir = repoRoot(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("a null artifactDaemon.hangar failed the render: %v\n%s", err, firstLines(string(out), 5))
	}
	web := objectNamed(t, string(out), "Deployment", "-"+webComponent)
	if !strings.Contains(web.body, "--kubernetes-hangar-warrant-ttl=900s\n") {
		t.Fatal("a null artifactDaemon.hangar does not render the default 900s warrant TTL")
	}
}
