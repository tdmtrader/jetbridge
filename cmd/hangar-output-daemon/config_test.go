package main

import (
	"os"
	"path/filepath"
	"strings"
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

// Key separation is over the BYTES, not over the paths.
//
// Validate refuses five path pairs and two equal key ids, and every one of
// those comparisons is over names: two flags pointing at symlinks to one file
// pass all of them, and so do two Secrets holding identical material. The
// separation the plan promises is a separation of AUTHORITY -- a read warrant
// must not be signable by anything that can mint a publication receipt -- and
// authority follows the material.
func TestTwoKeyFilesHoldingOneKeyAreRefused(t *testing.T) {
	dir := t.TempDir()
	same := []byte("-----BEGIN PRIVATE KEY-----\nthe same thirty-two bytes, twice\n")
	first := filepath.Join(dir, "receipt.pem")
	second := filepath.Join(dir, "materialize.key")
	for _, file := range []string{first, second} {
		if err := os.WriteFile(file, same, 0o600); err != nil {
			t.Fatalf("writing %s: %v", file, err)
		}
	}

	config := Config{ReceiptKeyFile: first, MaterializationKeyFile: second}
	err := config.RefuseCollidingKeyMaterial()
	if err == nil {
		t.Fatal("two different files holding the SAME key material were accepted. The path " +
			"check passes -- they are different paths -- and the two domains are then signable " +
			"by one key.")
	}
	for _, fragment := range []string{"--receipt-key-file", "--materialization-key-file"} {
		if !strings.Contains(err.Error(), fragment) {
			t.Errorf("the refusal %q does not name %s", err, fragment)
		}
	}

	// A symlink is the same collision wearing a different name, and the path
	// check cannot see it either.
	linked := filepath.Join(dir, "linked.key")
	if err := os.Symlink(first, linked); err != nil {
		t.Fatalf("linking: %v", err)
	}
	if err := (Config{ReceiptKeyFile: first, ControlKeyFile: linked}).
		RefuseCollidingKeyMaterial(); err == nil {
		t.Error("two flags pointing at one file through a symlink were accepted")
	}

	// The control: genuinely different material passes.
	if err := os.WriteFile(second, []byte("a different key entirely\n"), 0o600); err != nil {
		t.Fatalf("rewriting: %v", err)
	}
	if err := config.RefuseCollidingKeyMaterial(); err != nil {
		t.Errorf("two distinct keys were refused: %v", err)
	}
}
