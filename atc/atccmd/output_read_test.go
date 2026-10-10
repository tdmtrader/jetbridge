package atccmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/concourse/concourse/hangar"
)

// Result reads spool the archive and its canonical copy into scratch until the
// response is written. Without a configured directory that is os.TempDir(),
// which the chart mounts as a 64Mi in-memory emptyDir charged to web's memory.

func TestRunResultScratchIsAPrivateChildOfDiskScratch(t *testing.T) {
	// A disk-backed emptyDir is created 0777 with no sticky bit, which the
	// canonicalizer refuses as a temporary parent.
	parent := t.TempDir()
	if err := os.Chmod(parent, 0777); err != nil {
		t.Fatal(err)
	}
	if hangar.ValidateTempDir(parent) == nil {
		t.Fatal("fixture: a 0777 non-sticky parent should be refused by the canonicalizer")
	}
	stale := filepath.Join(parent, runResultScratchChild, "left-by-a-crashed-read")
	if err := os.MkdirAll(filepath.Dir(stale), 0777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("spool"), 0600); err != nil {
		t.Fatal(err)
	}

	want := filepath.Join(parent, runResultScratchChild)
	got, err := prepareRunResultScratch(parent)
	if err != nil || got != want {
		t.Fatalf("scratch = %q, %v; want %q", got, err, want)
	}
	info, err := os.Stat(want)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		t.Fatalf("scratch child = %v %v, want a 0700 directory", info, err)
	}
	if err := hangar.ValidateTempDir(want); err != nil {
		t.Fatalf("the canonicalizer refuses the prepared scratch: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("a previous process's spool survived startup: %v", err)
	}
}
