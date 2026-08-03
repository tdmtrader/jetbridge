package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestPrepareHomeInfraWritebackRemovesCopiedLocksBeforeGitConfiguration(t *testing.T) {
	fixture := newHomeInfraFixture(t, seedRunnerImage)
	output := filepath.Join(fixture.dir, "home-infra-updated")
	if err := os.Mkdir(output, 0o755); err != nil {
		t.Fatal(err)
	}

	sourceConfigLock := filepath.Join(fixture.clone, ".git", "config.lock")
	sourceRefLock := filepath.Join(fixture.clone, ".git", "refs", "heads", "main.lock")
	sentinel := filepath.Join(fixture.clone, ".git", "writeback-sentinel")
	symlinkLockTarget := filepath.Join(fixture.dir, "symlink-lock-target")
	symlinkLock := filepath.Join(fixture.clone, ".git", "retained.lock")
	for path, contents := range map[string]string{
		sourceConfigLock: "stale config lock\n",
		sourceRefLock:    "stale ref lock\n",
		sentinel:         "preserve me\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(symlinkLockTarget, []byte("must not remove symlink target\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(symlinkLockTarget, symlinkLock); err != nil {
		t.Fatal(err)
	}

	ordinaryCopy := filepath.Join(fixture.dir, "ordinary-copy")
	if err := os.Mkdir(ordinaryCopy, 0o755); err != nil {
		t.Fatal(err)
	}
	if commandOutput, err := exec.Command("cp", "-a", fixture.clone+"/.", ordinaryCopy+"/").CombinedOutput(); err != nil {
		t.Fatalf("ordinary repository copy: %v\n%s", err, commandOutput)
	}
	if _, err := os.Stat(filepath.Join(ordinaryCopy, ".git", "config.lock")); err != nil {
		t.Fatalf("ordinary copy did not preserve stale config lock: %v", err)
	}

	if output, err := runPrepareHomeInfraWriteback(t, fixture.clone, output); err != nil {
		t.Fatalf("prepare writeback repository: %v\n%s", err, output)
	}

	for _, path := range []string{sourceConfigLock, sourceRefLock} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("source lock %s was changed: %v", path, err)
		}
	}
	for _, path := range []string{
		filepath.Join(output, ".git", "config.lock"),
		filepath.Join(output, ".git", "refs", "heads", "main.lock"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("copied lock %s remains after preparation: %v", path, err)
		}
	}
	if got, err := os.ReadFile(filepath.Join(output, ".git", "writeback-sentinel")); err != nil || string(got) != "preserve me\n" {
		t.Fatalf("non-lock Git control file = %q, %v", got, err)
	}
	if info, err := os.Lstat(filepath.Join(output, ".git", "retained.lock")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("non-regular lock entry was removed or changed: %v, %v", info, err)
	}
	if got, err := os.ReadFile(symlinkLockTarget); err != nil || string(got) != "must not remove symlink target\n" {
		t.Fatalf("symlink lock target = %q, %v", got, err)
	}
	if got, want := gitOutput(t, output, "rev-parse", "HEAD"), gitOutput(t, fixture.clone, "rev-parse", "HEAD"); got != want {
		t.Fatalf("prepared HEAD = %s, want source HEAD %s", got, want)
	}
	if output, err := exec.Command("git", "-C", output, "remote", "add", "push-target", "https://example.invalid/home-infra.git").CombinedOutput(); err != nil {
		t.Fatalf("configure prepared remote: %v\n%s", err, output)
	}
}

func TestPrepareHomeInfraWritebackRejectsMalformedInputs(t *testing.T) {
	fixture := newHomeInfraFixture(t, seedRunnerImage)
	for name, prepare := range map[string]func(*testing.T) (string, string){
		"missing_source_git": func(t *testing.T) (string, string) {
			source := filepath.Join(t.TempDir(), "not-a-repository")
			output := filepath.Join(t.TempDir(), "output")
			if err := os.Mkdir(source, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(output, 0o755); err != nil {
				t.Fatal(err)
			}
			return source, output
		},
		"source_git_symlink": func(t *testing.T) (string, string) {
			source := filepath.Join(t.TempDir(), "linked-git")
			output := filepath.Join(t.TempDir(), "output")
			if err := os.Mkdir(source, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(fixture.clone, ".git"), filepath.Join(source, ".git")); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(output, 0o755); err != nil {
				t.Fatal(err)
			}
			return source, output
		},
		"non_empty_destination": func(t *testing.T) (string, string) {
			output := filepath.Join(t.TempDir(), "output")
			if err := os.Mkdir(output, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(output, "existing"), []byte("do not overwrite\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			return fixture.clone, output
		},
	} {
		t.Run(name, func(t *testing.T) {
			source, output := prepare(t)
			if commandOutput, err := runPrepareHomeInfraWriteback(t, source, output); err == nil {
				t.Fatalf("prepare unexpectedly accepted source=%s output=%s: %s", source, output, commandOutput)
			}
		})
	}
}

func TestPrepareHomeInfraWritebackRejectsMissingArguments(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("sh", filepath.Join(root, "deploy", "prepare-home-infra-writeback.sh")).CombinedOutput(); err == nil {
		t.Fatalf("prepare unexpectedly accepted no arguments: %s", output)
	}
}

func runPrepareHomeInfraWriteback(t *testing.T, source, output string) ([]byte, error) {
	t.Helper()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	return exec.Command("sh", filepath.Join(root, "deploy", "prepare-home-infra-writeback.sh"), source, output).CombinedOutput()
}
