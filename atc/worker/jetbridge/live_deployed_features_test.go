//go:build live
// +build live

package jetbridge_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// liveManifest is the committed description of the deployment the live tier
// runs against: which features it is supposed to have on, and which live test
// covers each one or why none can. testdata/live_deployment.json describes the
// home cluster; another deployment supplies its own through
// K8S_LIVE_DEPLOYMENT_MANIFEST.
type liveManifest struct {
	Deployment string                         `json:"deployment"`
	Prometheus *livePrometheus                `json:"prometheus"`
	Features   map[string]liveManifestFeature `json:"features"`
}

type livePrometheus struct {
	Namespace string `json:"namespace"`
	Service   string `json:"service"`
	Port      string `json:"port"`
}

type liveManifestFeature struct {
	// Expect is the observed value the deployment must have: "off", "on", or
	// the exact value for a feature that carries one.
	Expect string `json:"expect"`
	// CoveredBy names the live tests that exercise the feature against the
	// deployment. Every name must be a test function in this package.
	CoveredBy []string `json:"coveredBy,omitempty"`
	// Uncovered says why a feature that is not off has no live test.
	Uncovered string `json:"uncovered,omitempty"`
	// Note records a standing decision about a feature that is off.
	Note string `json:"note,omitempty"`
}

func loadLiveManifest(t *testing.T) liveManifest {
	t.Helper()
	path := os.Getenv("K8S_LIVE_DEPLOYMENT_MANIFEST")
	if path == "" {
		path = filepath.Join("testdata", "live_deployment.json")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading live deployment manifest: %v", err)
	}
	var manifest liveManifest
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		t.Fatalf("decoding %s: %v", path, err)
	}
	return manifest
}

// liveFeatures observes each feature on the deployed workloads, from the
// flags the chart renders for it. An observer returns "off" when the feature
// is not enabled.
var liveFeatures = map[string]func(d *liveDeployment) string{
	"web.mcp": func(d *liveDeployment) string { return onOff(d.webFlag("enable-mcp")) },
	// The credential manager's value is the namespace prefix secrets are looked
	// up under, since that is what a pipeline author has to get right.
	"web.credentialManager": func(d *liveDeployment) string {
		if _, on := d.webFlag("kubernetes-in-cluster"); !on {
			return "off"
		}
		if prefix, ok := d.webFlag("kubernetes-namespace-prefix"); ok {
			return prefix
		}
		return "concourse-"
	},
	// The effective task cache store, resolved the way the web resolves it.
	"web.taskCache": func(d *liveDeployment) string {
		if store, ok := d.webFlag("kubernetes-cache-store"); ok && store != "" {
			return store
		}
		if path, ok := d.webFlag("kubernetes-cache-host-path"); ok && path != "" {
			return "hostpath"
		}
		return "emptydir"
	},
	"web.metrics": func(d *liveDeployment) string {
		if port, ok := d.webFlag("prometheus-bind-port"); ok {
			return port
		}
		return "off"
	},
	"web.tracing":             func(d *liveDeployment) string { return onOff(d.webFlag("tracing-otlp-address")) },
	"web.tls":                 func(d *liveDeployment) string { return onOff(d.webFlag("tls-bind-port")) },
	"web.hangar":              func(d *liveDeployment) string { return onOff(d.webFlag("kubernetes-hangar-enabled")) },
	"web.hangarOutput":        func(d *liveDeployment) string { return onOff(d.webFlag("kubernetes-hangar-output-enabled")) },
	"web.hangarOutputCapture": func(d *liveDeployment) string { return onOff(d.webFlag("kubernetes-hangar-output-capture-enabled")) },
	"web.runResults":          func(d *liveDeployment) string { return onOff(d.webFlag("run-result-scratch-dir")) },

	"daemon.mtls":              func(d *liveDeployment) string { return onOff(d.daemonFlag("tls-cert")) },
	"daemon.resolveCapability": func(d *liveDeployment) string { return onOff(d.daemonFlag("resolve-capability-key")) },
	"daemon.hangar":            func(d *liveDeployment) string { return onOff(d.daemonFlag("hangar-enabled")) },
	"daemon.preemption":        func(d *liveDeployment) string { return onOff(d.daemonFlag("preemption-watch")) },
	// The plain-HTTP listener Prometheus scrapes; without it the daemon's
	// metrics exist only behind mTLS and nothing collects them.
	"daemon.metrics": func(d *liveDeployment) string {
		if port, ok := d.daemonFlag("metrics-port"); ok && port != "" && port != "0" {
			return port
		}
		return "off"
	},
	"daemon.durableStore": func(d *liveDeployment) string {
		if store, _ := d.daemonFlag("durable-store"); store != "" {
			return store
		}
		return "off"
	},
	// Mirroring is on by default and replicates to peers; how many nodes run a
	// daemon decides whether it can do anything at all, so both are asserted.
	"daemon.mirrorReplicas": func(d *liveDeployment) string {
		if replicas, ok := d.daemonFlag("mirror-replicas"); ok {
			return replicas
		}
		return "2"
	},
	"daemon.nodes": func(d *liveDeployment) string {
		return strconv.Itoa(int(d.daemon.Status.DesiredNumberScheduled))
	},
}

func onOff(_ string, on bool) string {
	if on {
		return "on"
	}
	return "off"
}

// TestLiveDeployedFeatureSet fails when the deployment the live tier runs
// against is not the deployment the manifest says it is.
//
// The live tier's coverage is a function of the cluster's configuration: a
// feature that is off is not tested, and nothing turns red to say so. This is
// the one test that notices. Turning a feature off, turning one on, or adding a
// node changes what the tier proves, and each of those now has to be a
// committed change to the manifest -- which is also where it has to say which
// live test covers the feature, or why none can.
func TestLiveDeployedFeatureSet(t *testing.T) {
	manifest := loadLiveManifest(t)
	t.Logf("manifest describes: %s", manifest.Deployment)

	var names []string
	for name := range liveFeatures {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		want, listed := manifest.Features[name]
		got := liveFeatures[name](deployed)
		switch {
		case !listed:
			t.Errorf("%s: observed %q, but the manifest does not list it; classify it", name, got)
		case got != want.Expect:
			t.Errorf("%s: deployment has %q, manifest expects %q", name, got, want.Expect)
		default:
			t.Logf("%s = %s", name, got)
		}
	}
	for name := range manifest.Features {
		if _, known := liveFeatures[name]; !known {
			t.Errorf("manifest lists %s, which no observer in liveFeatures reads", name)
		}
	}
}

// TestLiveManifestNamesRealCoverage keeps the manifest's coverage column
// honest: every feature that is not off either names live tests that exist or
// says why it has none. Only tests under the `live` tag count -- a test in
// another tier does not run against this deployment, so it belongs in the
// reason, not the coverage.
func TestLiveManifestNamesRealCoverage(t *testing.T) {
	manifest := loadLiveManifest(t)
	tests := liveTestFunctions(t)

	for name, feature := range manifest.Features {
		if feature.Expect != "off" && len(feature.CoveredBy) == 0 && feature.Uncovered == "" {
			t.Errorf("%s is %q but the manifest names no covering test and no reason for having none", name, feature.Expect)
		}
		for _, test := range feature.CoveredBy {
			if !tests[test] {
				t.Errorf("%s claims coverage by %s, which is not a `live` test in this package", name, test)
			}
		}
	}
}

var (
	testFunc      = regexp.MustCompile(`(?m)^func (Test\w+)\(t \*testing\.T\)`)
	liveBuildLine = regexp.MustCompile(`(?m)^//go:build live$`)
)

func liveTestFunctions(t *testing.T) map[string]bool {
	t.Helper()
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]bool{}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if !liveBuildLine.Match(data) {
			continue
		}
		for _, match := range testFunc.FindAllStringSubmatch(string(data), -1) {
			tests[match[1]] = true
		}
	}
	return tests
}
