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

// Reclaim is a bucket lifecycle rule, not code, and that is a decision an
// operator has to know about before they turn the tier on: nothing in JetBridge
// deletes from the store, so an unconfigured bucket grows forever.
//
// The chart is where they will look. This asserts the values file still says so,
// and still names the class prefix a rule has to match — a rule written against
// the wrong prefix silently expires nothing.
func TestValuesDocumentsThatReclaimIsALifecyclePolicy(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(repoRoot(t), "deploy", "chart", "values.yaml"))
	if err != nil {
		t.Fatalf("read values.yaml: %v", err)
	}
	values := string(body)

	for _, want := range []string{
		"RECLAIM IS NOT AUTOMATIC",
		"matchesPrefix",
		"resource-caches/",
	} {
		if !strings.Contains(values, want) {
			t.Errorf("values.yaml no longer mentions %q; an operator has no way to learn reclaim is theirs to configure", want)
		}
	}
}

// The prefix the docs tell operators to write a rule against must be the prefix
// the ATC actually stores under. If these drift, every lifecycle rule in every
// deployment silently stops matching and the bucket grows without bound, with
// no error anywhere.
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

	if !strings.Contains(string(values), class+"/") {
		t.Errorf("values.yaml documents a lifecycle prefix that does not match the code's class %q/", class)
	}
}
