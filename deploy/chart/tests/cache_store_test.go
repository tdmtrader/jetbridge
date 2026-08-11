package tests

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/concourse/concourse/atc/worker/jetbridge"
)

// values.yaml documented `cacheStore` as accepting "artifact, pvc, hostpath,
// emptydir". The binary accepts two: jetbridge.ValidCacheStores is
// {hostpath, emptydir}, and atc/atccmd validates against it at startup. So
// `cacheStore: pvc` -- a value the chart told the operator to use -- is not a
// warning or a fallback. It is a web node that will not boot.
//
// The two extra values were real once, when task caches could live on the
// artifact PVC. That backend was removed and the chart's documentation was
// not, which is the same drift TestChartRendersOnlyFlagsTheBinaryAccepts
// exists to catch one level up: the chart describing a binary that no longer
// agrees.
//
// Rather than restate the valid set here -- two hardcodings that agree with
// each other prove nothing -- import it. This test package is in the same
// module, so the map itself is available.
func TestChartDocumentsOnlyCacheStoresTheBinaryAccepts(t *testing.T) {
	root := repoRoot(t)

	values, err := os.ReadFile(filepath.Join(root, "deploy", "chart", "values.yaml"))
	if err != nil {
		t.Fatalf("reading chart values: %v", err)
	}

	// `# Valid values: hostpath, emptydir.` -- stopping at the first period, so
	// prose that follows on the same line is not read as another value. My
	// first pattern anchored on `$` and duly reported "emptydir. See
	// ValidCacheStores in" as an undocumented backend.
	re := regexp.MustCompile(`(?m)^#\s*Valid values:\s*([^.\n]+)\.`)
	match := re.FindSubmatch(values)
	if match == nil {
		t.Fatal("values.yaml no longer contains a `# Valid values: ...` line for " +
			"cacheStore. If the comment moved, move this test with it -- silently " +
			"finding nothing is how the drift got here.")
	}

	var documented []string
	for _, v := range strings.Split(string(match[1]), ",") {
		v = strings.TrimSpace(strings.Trim(strings.TrimSpace(v), "`\"'"))
		if v != "" {
			documented = append(documented, v)
		}
	}
	sort.Strings(documented)

	accepted := make([]string, 0, len(jetbridge.ValidCacheStores))
	for v := range jetbridge.ValidCacheStores {
		accepted = append(accepted, v)
	}
	sort.Strings(accepted)

	if len(accepted) == 0 {
		t.Fatal("jetbridge.ValidCacheStores is empty; this test would pass vacuously")
	}

	acceptedSet := map[string]bool{}
	for _, v := range accepted {
		acceptedSet[v] = true
	}
	documentedSet := map[string]bool{}
	for _, v := range documented {
		documentedSet[v] = true
	}

	for _, v := range documented {
		if !acceptedSet[v] {
			t.Errorf("values.yaml documents cacheStore value %q, which the binary "+
				"rejects (jetbridge.ValidCacheStores = %v). An operator who follows "+
				"the chart gets a web node that fails to start.",
				v, accepted)
		}
	}

	for _, v := range accepted {
		if !documentedSet[v] {
			t.Errorf("the binary accepts cacheStore value %q but values.yaml does "+
				"not document it (documented: %v).", v, documented)
		}
	}
}
