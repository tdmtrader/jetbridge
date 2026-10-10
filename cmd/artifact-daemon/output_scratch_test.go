package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/concourse/concourse/hangar"
)

func TestTheOutputScratchIsDisjointFromTheStorageRoot(t *testing.T) {
	root := t.TempDir()
	at := func(rel string) string { return filepath.Join(root, rel) }
	for name, row := range map[string]struct {
		scratch, storage string
		ok               bool
	}{
		"disjoint":                          {at("output-scratch"), at("artifacts"), true},
		"unset":                             {"", at("artifacts"), true},
		"the storage root":                  {at("artifacts"), at("artifacts"), false},
		"inside the storage root":           {at("artifacts/scratch"), at("artifacts"), false},
		"holding the storage root":          {root, at("artifacts"), false},
		"a sibling sharing a string prefix": {at("artifacts-scratch"), at("artifacts"), true},
	} {
		err := validateOutputScratch(row.scratch, row.storage)
		if (err == nil) != row.ok {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// kubelet creates an emptyDir 0777 without the sticky bit, which the
// canonicalizer refuses as a temp parent; the daemon owns the directory and
// tightens it before the plane uses it.
func TestTheOutputScratchIsMadePrivate(t *testing.T) {
	root := t.TempDir()
	scratch := filepath.Join(root, "output-scratch")
	if err := os.Mkdir(scratch, 0777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(scratch, 0777); err != nil {
		t.Fatal(err)
	}
	if err := validateOutputScratch(scratch, filepath.Join(root, "artifacts")); err != nil {
		t.Fatalf("a world-writable scratch was refused instead of tightened: %v", err)
	}
	info, err := os.Stat(scratch)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0700 {
		t.Fatalf("scratch mode = %o, want 0700", info.Mode().Perm())
	}
	if err := hangar.ValidateTempDir(scratch); err != nil {
		t.Fatalf("the tightened scratch does not validate as a temp parent: %v", err)
	}

	link := filepath.Join(root, "scratch-link")
	if err := os.Symlink(scratch, link); err != nil {
		t.Fatal(err)
	}
	if err := validateOutputScratch(link, filepath.Join(root, "artifacts")); err == nil {
		t.Fatal("a symlinked scratch directory was accepted")
	}
}
