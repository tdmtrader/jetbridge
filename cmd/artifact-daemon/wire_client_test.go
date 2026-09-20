package main_test

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"

	"github.com/concourse/concourse/artifactwire"
)

// The wire client meets the real daemon, not a stub of it. Every operation the
// ATC uses is driven here against Server so that a handler and the client that
// calls it are proven against each other once, in one place.
func TestWireClientAgainstTheRealDaemon(t *testing.T) {
	storage := t.TempDir()
	stepDir := filepath.Join(storage, "steps", "wire-handle", "output")
	if err := os.MkdirAll(stepDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stepDir, "hello.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	server := newDaemonServer(t, lagertest.NewTestLogger("wire"), storage, "node-wire")
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()
	host, port := splitHostPort(t, ts.Listener.Addr().String())

	client, err := artifactwire.NewClient(port, artifactwire.TLS{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	t.Run("head and stream out a step artifact", func(t *testing.T) {
		found, err := client.HeadArtifact(ctx, host, artifactwire.StepsPrefix+"wire-handle/output")
		if err != nil || !found {
			t.Fatalf("HeadArtifact: found=%v err=%v", found, err)
		}
		body, err := client.StreamOut(ctx, host, artifactwire.StepsPrefix+"wire-handle/output")
		if err != nil {
			t.Fatalf("StreamOut: %v", err)
		}
		defer body.Close()
		names := tarNames(t, body)
		if len(names) == 0 {
			t.Fatal("stream out produced an empty tar")
		}
	})

	t.Run("a missing key is ErrNotFound on stream out", func(t *testing.T) {
		_, err := client.StreamOut(ctx, host, artifactwire.StepsPrefix+"nobody/output")
		if !errors.Is(err, artifactwire.ErrNotFound) || !errors.Is(err, artifactwire.ErrRefused) {
			t.Fatalf("StreamOut of a missing key: %v", err)
		}
		found, err := client.HeadArtifact(ctx, host, artifactwire.StepsPrefix+"nobody/output")
		if err != nil || found {
			t.Fatalf("HeadArtifact of a missing key: found=%v err=%v", found, err)
		}
	})

	t.Run("register an alias, then read through it", func(t *testing.T) {
		err := client.Register(ctx, host, artifactwire.RegisterRequest{Key: "wire-alias", LocalPath: stepDir})
		if err != nil {
			t.Fatalf("Register: %v", err)
		}
		found, err := client.HeadArtifact(ctx, host, "wire-alias")
		if err != nil || !found {
			t.Fatalf("HeadArtifact via alias: found=%v err=%v", found, err)
		}
	})

	t.Run("register outside the storage root is refused", func(t *testing.T) {
		err := client.Register(ctx, host, artifactwire.RegisterRequest{Key: "escape", LocalPath: t.TempDir()})
		if !errors.Is(err, artifactwire.ErrRefused) {
			t.Fatalf("Register outside the root: %v", err)
		}
		var refusal *artifactwire.Refusal
		if !errors.As(err, &refusal) || refusal.Status != 400 {
			t.Fatalf("expected a 400 refusal, got %v", err)
		}
	})

	t.Run("stream in, head, delete, delete again", func(t *testing.T) {
		if err := client.StreamIn(ctx, host, "wire-in", wireTarOf(t, "in.txt", "streamed")); err != nil {
			t.Fatalf("StreamIn: %v", err)
		}
		found, err := client.HeadArtifact(ctx, host, artifactwire.StepsPrefix+"wire-in")
		if err != nil || !found {
			t.Fatalf("HeadArtifact after stream in: found=%v err=%v", found, err)
		}
		if err := client.Delete(ctx, host, artifactwire.StepsPrefix+"wire-in"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		// The bytes are gone either way, so a second delete is also done.
		if err := client.Delete(ctx, host, artifactwire.StepsPrefix+"wire-in"); err != nil {
			t.Fatalf("Delete of a gone key: %v", err)
		}
	})

	t.Run("mirror is accepted even with no trigger wired", func(t *testing.T) {
		if err := client.Mirror(ctx, host, "wire-handle/output"); err != nil {
			t.Fatalf("Mirror: %v", err)
		}
	})

	t.Run("resource cache probe answers on any status", func(t *testing.T) {
		probe, err := client.HeadResourceCache(ctx, host, "rc-nobody")
		if err != nil {
			t.Fatalf("HeadResourceCache: %v", err)
		}
		if probe.Found || probe.DurableCapable {
			t.Fatalf("a daemon with no durable tier and no such key answered %+v", probe)
		}
	})

	t.Run("capture class of an unmanaged step", func(t *testing.T) {
		answer, err := client.CaptureClass(ctx, host, "wire-handle")
		if err != nil {
			t.Fatalf("CaptureClass: %v", err)
		}
		if answer.Class != "unmanaged" || answer.Handle != "wire-handle" {
			t.Fatalf("CaptureClass answered %+v", answer)
		}
	})
}

func wireTarOf(t *testing.T, name, content string) io.Reader {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0644, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf
}

func tarNames(t *testing.T, r io.Reader) []string {
	t.Helper()
	var names []string
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return names
		}
		if err != nil {
			t.Fatalf("reading tar: %v", err)
		}
		names = append(names, hdr.Name)
	}
}
