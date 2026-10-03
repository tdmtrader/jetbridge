package codextest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/session"
)

// Every platform the harness provisions has a digest pinned beside the
// release version, so no download is ever trusted unverified.
func TestEveryProvisionedPlatformIsPinned(t *testing.T) {
	for _, triple := range []string{"aarch64-apple-darwin", "x86_64-apple-darwin", "aarch64-unknown-linux-musl", "x86_64-unknown-linux-musl"} {
		if sum, ok := session.CodexChecksum("codex-" + triple + ".tar.gz"); !ok || len(sum) != 64 {
			t.Errorf("%s has no pinned digest", triple)
		}
	}
}

// The provisioned binary is the pinned release, and the launcher runs
// nothing but the two invocations a session makes.
func TestLauncherRunsOnlyThePinnedRelease(t *testing.T) {
	launcher := Launcher(t, NewModel(t))
	out, err := exec.Command(launcher, "--version").Output()
	if err != nil || strings.TrimSpace(string(out)) != "codex-cli "+session.CodexVersion() {
		t.Fatalf("version %q %v", out, err)
	}
	if err := exec.Command(launcher, "login").Run(); err == nil {
		t.Fatal("the launcher ran an invocation no session makes")
	}
}

// Provisioning never trusts an executable already in the cache: bytes that
// replaced it are overwritten from the verified tarball, and a relative
// cache still yields a path that resolves from any directory.
func TestProvisionRestoresThePinnedExecutable(t *testing.T) {
	asset, err := Asset()
	if err != nil {
		t.Fatal(err)
	}
	tarball, err := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(Binary(t))), asset))
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cache", asset), tarball, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	t.Setenv(CacheVariable, "cache")
	binary, err := provision()
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(binary) || !strings.HasPrefix(binary, filepath.Join(root, "cache")) {
		t.Fatalf("provisioned %s", binary)
	}
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho codex-cli "+session.CodexVersion()+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if again, err := provision(); err != nil || again != binary {
		t.Fatalf("reprovisioned %s, %v", again, err)
	}
	if info, err := os.Stat(binary); err != nil || info.Size() < 1<<20 {
		t.Fatalf("the replaced executable was trusted: %v %v", info, err)
	}
}
