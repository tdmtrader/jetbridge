package atccmd

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/output"
)

// Result reads spool the archive and its canonical copy into scratch until the
// response is written. Without a configured directory that is os.TempDir(),
// which the chart mounts as a 64Mi in-memory emptyDir charged to web's memory.

func TestRunResultScratchIsAPrivateChildOfTheConfiguredDirectory(t *testing.T) {
	// A disk-backed emptyDir is created 0777 with no sticky bit, which the
	// canonicalizer refuses as a temporary parent.
	parent := t.TempDir()
	if err := os.Chmod(parent, 0777); err != nil {
		t.Fatal(err)
	}
	if hangar.ValidateTempDir(parent) == nil {
		t.Fatal("fixture: a 0777 non-sticky parent should be refused by the canonicalizer")
	}
	signer, err := output.NewReadWarrantSigner(bytes.Repeat([]byte{0x61}, output.ReadWarrantKeyBytes))
	if err != nil {
		t.Fatal(err)
	}
	cmd := &RunCommand{RunResultScratchDir: parent, RunResultReadConcurrency: 3}
	cmd.outputReadSigner = signer
	if err := cmd.validateRunResultReads(); err != nil {
		t.Fatalf("valid configuration refused: %v", err)
	}

	stale := filepath.Join(parent, runResultScratchChild, "left-by-a-crashed-read")
	if err := os.MkdirAll(filepath.Dir(stale), 0777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("spool"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := cmd.configureOutputReads(nil, nil); err != nil {
		t.Fatalf("configure: %v", err)
	}
	want := filepath.Join(parent, runResultScratchChild)
	if cmd.runResultReader == nil || cmd.runResultReader.Scratch != want {
		t.Fatalf("result reader scratch = %+v, want %s", cmd.runResultReader, want)
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
	if got := cmd.pipelineRunServices().ResultReadConcurrency; got != 3 {
		t.Fatalf("services result-read bound = %d, want 3", got)
	}
}

func TestRunResultScratchDefaultsToTheProcessTempDir(t *testing.T) {
	signer, err := output.NewReadWarrantSigner(bytes.Repeat([]byte{0x62}, output.ReadWarrantKeyBytes))
	if err != nil {
		t.Fatal(err)
	}
	cmd := &RunCommand{RunResultReadConcurrency: 2}
	cmd.outputReadSigner = signer
	if err := cmd.configureOutputReads(nil, nil); err != nil {
		t.Fatal(err)
	}
	if cmd.runResultReader.Scratch != "" {
		t.Fatalf("unset flag produced scratch %q", cmd.runResultReader.Scratch)
	}
}

func TestRunResultReadConfigurationIsValidated(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for name, cmd := range map[string]*RunCommand{
		"zero concurrency":     {RunResultReadConcurrency: 0},
		"relative scratch":     {RunResultReadConcurrency: 2, RunResultScratchDir: "scratch"},
		"missing scratch":      {RunResultReadConcurrency: 2, RunResultScratchDir: filepath.Join(t.TempDir(), "absent")},
		"scratch is not a dir": {RunResultReadConcurrency: 2, RunResultScratchDir: file},
	} {
		t.Run(name, func(t *testing.T) {
			if err := cmd.validateRunResultReads(); err == nil {
				t.Fatal("invalid result-read configuration accepted")
			}
		})
	}
}
