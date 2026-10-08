package outputplane

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/concourse/concourse/hangar"
)

// A KILLED capture leaves the assembled canonical.tar in the scratch volume,
// and the restart path has to sweep it.
//
// Two things are wrong if it does not. canonical.tar is the PLAINTEXT of a
// durable output, outliving its capture on the node with no record that it is
// there -- and an emptyDir is per-Pod rather than per-container, so a container
// crash-loop keeps every one of them. And it invalidates the size arithmetic
// the daemon and the chart both reason from: the scratch ceiling is concurrency
// times the content limit, `concourse.hangarOutput.validateScratch` renders and
// checks exactly that product, and both assume the directory starts empty. The
// failure mode under a crash-loop is the eviction the bound exists to prevent.
func TestPrepareScratchSweepsWhatAKilledCanonicalizationLeft(t *testing.T) {
	scratch := filepath.Join(t.TempDir(), "scratch")
	residue := filepath.Join(scratch, hangar.CanonicalizerTempPrefix+"214689794")
	if err := os.MkdirAll(filepath.Join(residue, "root"), 0o700); err != nil {
		t.Fatalf("planting the residue: %v", err)
	}
	if err := os.WriteFile(filepath.Join(residue, "canonical.tar"),
		[]byte("the plaintext of a durable output"), 0o600); err != nil {
		t.Fatalf("planting the archive: %v", err)
	}
	// And something that is not this canonicalizer's, which must survive: a
	// misconfigured --scratch-dir must not turn a restart into a deletion of
	// somebody else's data.
	if err := os.WriteFile(filepath.Join(scratch, "not-ours"), []byte("x"), 0o600); err != nil {
		t.Fatalf("planting the neighbour: %v", err)
	}

	config := Config{ScratchDir: scratch}
	if err := config.PrepareScratch(); err != nil {
		t.Fatalf("preparing the scratch directory: %v", err)
	}

	if _, err := os.Stat(residue); !os.IsNotExist(err) {
		t.Errorf("%s survived the restart path (stat err %v). The output plaintext of a killed "+
			"capture outlives its capture on the node, and the scratch volume does not start "+
			"empty, which is what the chart's sizeLimit arithmetic assumes.", residue, err)
	}
	if _, err := os.Stat(filepath.Join(scratch, "not-ours")); err != nil {
		t.Errorf("the sweep removed something that is not a canonicalization directory: %v", err)
	}
}
