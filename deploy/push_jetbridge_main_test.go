package deploy

import (
	"os/exec"
	"path/filepath"
	"testing"
)

type pushJetbridgeMainFixture struct {
	dir     string
	origin  string
	release string
}

func newPushJetbridgeMainFixture(t *testing.T) *pushJetbridgeMainFixture {
	t.Helper()
	dir := t.TempDir()
	origin := filepath.Join(dir, "origin.git")
	seed := filepath.Join(dir, "seed")
	release := filepath.Join(dir, "release")

	runGit(t, dir, "init", "--bare", "-b", "main", origin)
	runGit(t, dir, "init", "-b", "main", seed)
	writeFixtureFile(t, filepath.Join(seed, "README.md"), "M0\n")
	runGit(t, seed, "add", "README.md")
	runGit(t, seed, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "M0")
	runGit(t, seed, "remote", "add", "origin", origin)
	runGit(t, seed, "push", "origin", "HEAD:main")
	runGit(t, dir, "clone", origin, release)

	writeFixtureFile(t, filepath.Join(release, "README.md"), "R\n")
	runGit(t, release, "add", "README.md")
	runGit(t, release, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "R")

	return &pushJetbridgeMainFixture{dir: dir, origin: origin, release: release}
}

func (f *pushJetbridgeMainFixture) runHelper(t *testing.T) ([]byte, error) {
	t.Helper()
	helper, err := filepath.Abs("push-jetbridge-main.sh")
	if err != nil {
		t.Fatal(err)
	}
	return exec.Command("sh", helper, f.release).CombinedOutput()
}

func TestPushJetbridgeMain(t *testing.T) {
	t.Run("fast-forward publication advances main to the release commit", func(t *testing.T) {
		fixture := newPushJetbridgeMainFixture(t)
		releaseCommit := gitOutput(t, fixture.release, "rev-parse", "HEAD")

		if output, err := fixture.runHelper(t); err != nil {
			t.Fatalf("publish release commit: %v\n%s", err, output)
		}
		if got := gitOutput(t, fixture.origin, "rev-parse", "refs/heads/main"); got != releaseCommit {
			t.Fatalf("remote main = %s, want release commit %s", got, releaseCommit)
		}
	})

	t.Run("divergent main remains at the concurrent commit", func(t *testing.T) {
		fixture := newPushJetbridgeMainFixture(t)
		concurrent := filepath.Join(fixture.dir, "concurrent")
		runGit(t, fixture.dir, "clone", fixture.origin, concurrent)
		writeFixtureFile(t, filepath.Join(concurrent, "README.md"), "M1\n")
		runGit(t, concurrent, "add", "README.md")
		runGit(t, concurrent, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "M1")
		runGit(t, concurrent, "push", "origin", "HEAD:main")
		concurrentCommit := gitOutput(t, concurrent, "rev-parse", "HEAD")

		if output, err := fixture.runHelper(t); err == nil {
			t.Fatalf("publish unexpectedly accepted divergent main:\n%s", output)
		}
		if got := gitOutput(t, fixture.origin, "rev-parse", "refs/heads/main"); got != concurrentCommit {
			t.Fatalf("remote main = %s, want concurrent commit %s", got, concurrentCommit)
		}
	})
}
