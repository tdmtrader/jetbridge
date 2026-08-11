package concourse

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The version is declared in three places that no tooling keeps in sync:
//
//	VERSION                     read by the release pipeline
//	versions.go JetBridgeVersion compiled into the binary, served at /api/v1/info
//	deploy/chart/Chart.yaml      appVersion, which the chart falls back to as the
//	                             image tag when image.tag is unset
//
// Before this test all three read 0.2.80 while the pipeline cut v0.2.246 from
// git tags, so none of them described anything. A partial bump must fail on
// push rather than surface as an ImagePullBackOff for a tag nobody built.
func TestVersionDeclarationsAgree(t *testing.T) {
	want := readVersionFile(t)

	if JetBridgeVersion != want {
		t.Errorf("versions.go JetBridgeVersion = %q, VERSION = %q", JetBridgeVersion, want)
	}
	if got := readChartAppVersion(t); got != want {
		t.Errorf("Chart.yaml appVersion = %q, VERSION = %q", got, want)
	}
}

func TestVersionFileIsSemver(t *testing.T) {
	v := readVersionFile(t)
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(v) {
		t.Errorf("VERSION = %q, want MAJOR.MINOR.PATCH", v)
	}
	// The release tag carries the jb- prefix (upstream Concourse owns v0.3.0
	// and everything through v8.2.5), but the version itself must stay bare:
	// it is passed to -ldflags -X ...Version=, and fly parses that with
	// go-semi-semantic in fly/rc/target.go. A prefix there breaks fly login.
	if strings.ContainsAny(v, "v-") {
		t.Errorf("VERSION = %q must not carry a prefix or suffix", v)
	}
}

func readVersionFile(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("VERSION")
	if err != nil {
		t.Fatalf("reading VERSION: %v", err)
	}
	v := strings.TrimSpace(string(b))
	if v == "" {
		t.Fatal("VERSION is empty")
	}
	return v
}

func readChartAppVersion(t *testing.T) string {
	t.Helper()
	const path = "deploy/chart/Chart.yaml"
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	m := regexp.MustCompile(`(?m)^appVersion:\s*"?([^"\s]+)"?\s*$`).FindSubmatch(b)
	if m == nil {
		// Guards assert they matched something: an appVersion that moved or was
		// renamed must fail here rather than silently stop being checked.
		t.Fatalf("no appVersion found in %s -- this test is watching the wrong key", path)
	}
	return string(m[1])
}
