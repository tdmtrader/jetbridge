package tests

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// render runs `helm template` with the given --set overrides.
func render(t *testing.T, sets ...string) string {
	t.Helper()

	args := []string{"template", "jb", "deploy/chart"}
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

// The durable tier is opt-in, and off has to mean untouched: a daemon that
// silently grew a flag would change behaviour for every existing install on
// upgrade.
func TestDurableStoreIsOffByDefault(t *testing.T) {
	if got := render(t); strings.Contains(got, "--durable-") {
		t.Error("the default render passes durable flags; the tier must be opt-in")
	}
}

// --durable-max-bytes is an int64 flag, and Helm renders a large integer in
// scientific notation unless it is coerced. `flag.Int64` rejects
// "5.36870912e+09", so the daemon would exit at startup on a value the chart
// supplied itself.
func TestDurableMaxBytesRendersAsAnInteger(t *testing.T) {
	out := render(t,
		"artifactDaemon.durable.store=s3",
		"artifactDaemon.durable.bucket=b",
	)

	if strings.Contains(out, "e+") {
		t.Error("--durable-max-bytes rendered in scientific notation; flag.Int64 will reject it")
	}
	if !strings.Contains(out, "--durable-max-bytes=5368709120") {
		t.Errorf("expected an integer byte limit; got:\n%s", durableFlags(out))
	}
}

// A store named but not configured is a daemon that starts, reports healthy,
// and quietly caches nothing. Fail at render instead.
func TestIncompleteDurableConfigFailsToRender(t *testing.T) {
	for _, tc := range []struct {
		name string
		sets []string
		want string
	}{
		{
			name: "s3 without a bucket",
			sets: []string{"artifactDaemon.durable.store=s3"},
			want: "bucket is required",
		},
		{
			name: "gcs without a bucket",
			sets: []string{"artifactDaemon.durable.store=gcs"},
			want: "bucket is required",
		},
		{
			name: "filesystem without a path",
			sets: []string{"artifactDaemon.durable.store=filesystem"},
			want: "path is required",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"template", "jb", "deploy/chart"}
			for _, s := range tc.sets {
				args = append(args, "--set", s)
			}

			cmd := exec.Command("helm", args...)
			cmd.Dir = repoRoot(t)
			out, err := cmd.CombinedOutput()

			if err == nil {
				t.Fatalf("render succeeded; it should have demanded the missing value:\n%s", durableFlags(string(out)))
			}
			if !strings.Contains(string(out), tc.want) {
				t.Errorf("error did not mention %q:\n%s", tc.want, out)
			}
		})
	}
}

// Credentials must arrive as environment, never as a flag: flags land in the
// process table and in `kubectl describe pod`.
func TestDurableCredentialsAreNotPassedAsFlags(t *testing.T) {
	out := render(t,
		"artifactDaemon.durable.store=s3",
		"artifactDaemon.durable.bucket=b",
		"artifactDaemon.durable.existingSecret=bucket-creds",
	)

	for _, leak := range []string{"--durable-s3-access-key", "--durable-s3-secret", "AWS_SECRET_ACCESS_KEY: "} {
		if strings.Contains(out, leak) {
			t.Errorf("render contains %q; credentials must come from the Secret by reference", leak)
		}
	}
	if !strings.Contains(out, "name: bucket-creds") {
		t.Error("existingSecret was not referenced by the DaemonSet")
	}
}

// durableFlags extracts just the durable lines, so a failure message is
// readable rather than a whole rendered chart.
func durableFlags(out string) string {
	var kept []string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "durable") {
			kept = append(kept, strings.TrimSpace(line))
		}
	}
	if len(kept) == 0 {
		return "(no durable flags rendered)"
	}

	return strings.Join(kept, "\n")
}

// GCS is the day-0 backend, and the point of choosing it over S3 interop is
// that it needs no credential at all: Application Default Credentials on GKE
// means Workload Identity. A render that demanded a key would have thrown that
// away.
func TestGCSNeedsNoCredentialFlags(t *testing.T) {
	out := render(t,
		"artifactDaemon.durable.store=gcs",
		"artifactDaemon.durable.bucket=jb-artifacts",
	)

	if !strings.Contains(out, "--durable-store=gcs") {
		t.Fatalf("gcs backend not rendered:\n%s", durableFlags(out))
	}
	for _, unwanted := range []string{"--durable-s3-region", "secretRef", "GOOGLE_APPLICATION_CREDENTIALS"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("gcs render contains %q; it should authenticate with ADC alone", unwanted)
		}
	}
}

func TestGCSRejectsS3CredentialSecret(t *testing.T) {
	args := []string{"template", "jb", "deploy/chart", "--set", "artifactDaemon.durable.store=gcs", "--set", "artifactDaemon.durable.bucket=b", "--set", "artifactDaemon.durable.existingSecret=s3-creds"}
	cmd := exec.Command("helm", args...)
	cmd.Dir = repoRoot(t)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("GCS render accepted an S3 credential Secret:\n%s", out)
	}
	if !strings.Contains(string(out), "existingSecret") || !strings.Contains(string(out), "s3") {
		t.Fatalf("GCS credential error was unclear:\n%s", out)
	}
}

// An unset retention keeps everything forever. That is a defensible default --
// deleting by default would be far worse -- but it has to be legible, because
// the failure mode is a bill rather than an error.
func TestValuesSaysAnUnsetRetentionKeepsEverything(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(repoRoot(t), "deploy", "chart", "values.yaml"))
	if err != nil {
		t.Fatalf("read values.yaml: %v", err)
	}
	values := string(body)

	for _, want := range []string{"KEPT FOREVER", "retention:"} {
		if !strings.Contains(values, want) {
			t.Errorf("values.yaml no longer mentions %q; an operator has no way to learn nothing expires by default", want)
		}
	}
}

// Retention is off unless asked for, and the daemon must not grow a flag on
// upgrade for every existing install.
func TestRetentionIsNotRenderedByDefault(t *testing.T) {
	out := render(t,
		"artifactDaemon.durable.store=gcs",
		"artifactDaemon.durable.bucket=b",
	)

	if strings.Contains(out, "--durable-retention") {
		t.Error("a default render passes --durable-retention; retention must be opt-in")
	}
}

// Each configured class must reach the binary as its own flag. A class that
// renders but is silently dropped keeps its objects forever with no error.
func TestRetentionRendersOneFlagPerClass(t *testing.T) {
	out := render(t,
		"artifactDaemon.durable.store=gcs",
		"artifactDaemon.durable.bucket=b",
		"artifactDaemon.durable.retention.resource-caches=720h",
		"artifactDaemon.durable.retention.reviews=8760h",
	)

	for _, want := range []string{
		"--durable-retention=resource-caches=720h",
		"--durable-retention=reviews=8760h",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in the render; got:\n%s", want, durableFlags(out))
		}
	}
}

// The class name an operator writes in values.yaml must be the class the ATC
// actually stores under. A retention entry naming a class nothing produces is
// inert: it expires nothing, reports nothing, and the store grows.
func TestDocumentedPrefixMatchesTheCodesRetentionClass(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(repoRoot(t), "atc", "worker", "jetbridge", "resource_cache_key.go"))
	if err != nil {
		t.Fatalf("read resource_cache_key.go: %v", err)
	}

	m := regexp.MustCompile(`DurableClassResourceCache\s*=\s*"([^"]+)"`).FindSubmatch(body)
	if m == nil {
		t.Fatal("DurableClassResourceCache constant not found")
	}
	class := string(m[1])

	values, err := os.ReadFile(filepath.Join(repoRoot(t), "deploy", "chart", "values.yaml"))
	if err != nil {
		t.Fatalf("read values.yaml: %v", err)
	}

	// As a retention key in the documented example, which is the form an
	// operator copies.
	if !strings.Contains(string(values), class+": ") {
		t.Errorf("values.yaml never shows %q as a retention key; an operator has nothing correct to copy", class)
	}
}
