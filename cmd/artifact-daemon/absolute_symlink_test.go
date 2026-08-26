package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"
)

// An absolute symlink is refused on EGRESS as well as ingest.
//
// The extractor has always refused absolute targets, but the producer emitted
// them, so a node could hold an artifact it was structurally incapable of
// handing to a peer — and the failure surfaced later, attributed to the peer.
//
// Absolute targets name the producing machine's filesystem. Delivered into a
// consumer they resolve against ITS namespace, so `creds -> /var/run/secrets/...`
// reads the consumer's secret, not the producer's.
//
// This is a KNOWN BUILD-BREAKING change: `python -m venv` writes
// venv/bin/python -> /usr/local/bin/python3.11 by default, so an artifact
// carrying a venv now fails rather than delivering a link that only worked when
// producer and consumer happened to share an image. Chosen deliberately over a
// warning: a silently wrong artifact is the failure mode this package exists to
// remove.
func TestAbsoluteSymlink_RefusedOnEveryPath(t *testing.T) {
	seed := func(t *testing.T) (*Server, string) {
		t.Helper()
		root := t.TempDir()
		s := newServerT(t, lagertest.NewTestLogger("abs"), root, "node")
		out := filepath.Join(root, "steps", "build-1", "out")
		if err := os.MkdirAll(filepath.Join(out, "venv", "bin"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, "app.py"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		// Exactly what `python -m venv` writes.
		if err := os.Symlink("/usr/local/bin/python3.11", filepath.Join(out, "venv", "bin", "python")); err != nil {
			t.Fatal(err)
		}
		return s, root
	}

	// Names the offending entry, so an operator can act without reading code.
	wantNamed := func(t *testing.T, err error) {
		t.Helper()
		if err == nil {
			t.Fatal("an absolute symlink was accepted")
		}
		if !strings.Contains(err.Error(), "venv/bin/python") {
			t.Errorf("error does not name the entry: %v", err)
		}
	}

	t.Run("tar egress (GET, mirror, durable store)", func(t *testing.T) {
		s, _ := seed(t)
		wantNamed(t, s.tarDirectory(io.Discard, "steps/build-1/out"))
	})

	t.Run("copy to a container mount (resolve)", func(t *testing.T) {
		s, _ := seed(t)
		wantNamed(t, s.copyArtifact("steps/build-1/out", filepath.Join(t.TempDir(), "delivered")))
	})

	// The zero case. Without it every assertion above is satisfied by a daemon
	// that refuses everything.
	t.Run("a relative symlink still travels", func(t *testing.T) {
		root := t.TempDir()
		s := newServerT(t, lagertest.NewTestLogger("abs"), root, "node")
		out := filepath.Join(root, "steps", "build-2", "out")
		if err := os.MkdirAll(filepath.Join(out, "bin"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, "real.txt"), []byte("payload"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("../real.txt", filepath.Join(out, "bin", "link")); err != nil {
			t.Fatal(err)
		}

		var buf bytes.Buffer
		if err := s.tarDirectory(&buf, "steps/build-2/out"); err != nil {
			t.Fatalf("a contained relative symlink was refused: %v", err)
		}
		if !bytes.Contains(buf.Bytes(), []byte("../real.txt")) {
			t.Error("the relative link was dropped from the archive rather than carried")
		}

		dest := filepath.Join(t.TempDir(), "delivered")
		if err := s.copyArtifact("steps/build-2/out", dest); err != nil {
			t.Fatalf("copy refused a contained relative symlink: %v", err)
		}
		if target, err := os.Readlink(filepath.Join(dest, "bin", "link")); err != nil || target != "../real.txt" {
			t.Errorf("link not preserved through the copy: %q %v", target, err)
		}
	})
}

// The daemon must never emit an archive its own extractor would reject.
//
// Stated one-directionally on purpose. The biconditional ("produce fails iff
// consume fails") is wrong and the first version of this test asserted it: when
// the producer refuses it writes nothing, so the extractor is handed an empty
// stream and cleanly extracts zero entries. That is not a disagreement — there
// is simply no archive. What matters is the other direction.
func TestAbsoluteSymlink_ProducerNeverEmitsWhatExtractionRejects(t *testing.T) {
	for _, tc := range []struct {
		name       string
		target     string
		mustTravel bool
	}{
		{"absolute", "/etc/passwd", false},
		{"relative and contained", "real.txt", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			s := newServerT(t, lagertest.NewTestLogger("agree"), root, "node")
			out := filepath.Join(root, "steps", "b", "o")
			if err := os.MkdirAll(out, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(out, "real.txt"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(tc.target, filepath.Join(out, "link")); err != nil {
				t.Fatal(err)
			}

			var buf bytes.Buffer
			produceErr := s.tarDirectory(&buf, "steps/b/o")

			if !tc.mustTravel {
				if produceErr == nil {
					t.Fatal("the producer emitted an absolute symlink")
				}
				return
			}
			if produceErr != nil {
				t.Fatalf("a contained link was refused on egress: %v", produceErr)
			}

			// It produced an archive, so the extractor must accept it.
			dstRoot, err := os.OpenRoot(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer dstRoot.Close()
			if err := extractTarToRoot(bytes.NewReader(buf.Bytes()), dstRoot); err != nil {
				t.Fatalf("the daemon emitted an archive its own extractor rejects: %v", err)
			}
		})
	}
}
