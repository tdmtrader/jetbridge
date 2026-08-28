package main

// N4 — the dest-lock map must be bounded by IN-FLIGHT copies, not by every
// destination ever seen.
//
// The first version was a bare sync.Map keyed by cleaned dest, storing an entry
// before the copy ran and never pruning. 200 requests with distinct dests left
// 200 permanent entries — including for requests whose copy failed and wrote
// nothing — on the mTLS-exempt endpoint, with dest length bounded only by the
// body cap. In-package because destLocks is unexported; exposing it for a test
// would be worse than reading it here.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagertest"
)

func TestDestLocks_BoundedByInFlightCopies(t *testing.T) {
	root := t.TempDir()
	s := newServerT(t, lagertest.NewTestLogger("destlock"), root, "test-node")

	srcPath := filepath.Join(root, "steps", "leak", "out")
	if err := os.MkdirAll(srcPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcPath, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(root, "resolved")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}

	count := func() int {
		s.destLocksMu.Lock()
		defer s.destLocksMu.Unlock()
		return len(s.destLocks)
	}

	// Succeeding resolves — the lock now lives in resolveOne, so the property
	// is asserted against the path that actually takes it.
	for i := 0; i < 100; i++ {
		dest := filepath.Join(parent, "ok-"+itoa(i))
		if resp := s.resolveOne(context.Background(), "leak/out", dest); resp.Status != "ok" {
			t.Fatalf("resolve %d: %s (%s)", i, resp.Status, resp.Error)
		}
	}
	// Failing resolves — the leak was worst here, since nothing landed on disk.
	for i := 0; i < 100; i++ {
		dest := filepath.Join(root, "no-such-parent", "fail-"+itoa(i), "x")
		if resp := s.resolveOne(context.Background(), "leak/out", dest); resp.Status == "ok" {
			t.Fatalf("resolve into a missing parent reported ok")
		}
	}

	if n := count(); n != 0 {
		t.Errorf("dest-lock map retained %d entries after 200 completed resolves — unbounded growth", n)
	} else {
		t.Logf("200 distinct dests (100 ok, 100 failed): 0 retained entries")
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// R9 — the node-wide resolve bound, asserted against the HANDLERS.
//
// The previous version of this test took and released slots on s.resolveSem
// directly, which proved only that a buffered channel is a buffered channel:
// deleting the acquire/release from the handler left it green. These tests
// issue real HTTP requests, so the bound must be enforced where the requests
// arrive or they fail.
func TestResolveSem_BoundsBothResolveRoutes(t *testing.T) {
	fill := func(t *testing.T, s *Server) func() {
		t.Helper()
		for i := 0; i < cap(s.resolveSem); i++ {
			select {
			case s.resolveSem <- struct{}{}:
			default:
				t.Fatalf("could not take slot %d", i)
			}
		}
		return func() {
			for i := 0; i < cap(s.resolveSem); i++ {
				<-s.resolveSem
			}
		}
	}

	seed := func(t *testing.T) (*Server, *httptest.Server, string, string) {
		t.Helper()
		root := t.TempDir()
		s := newServerT(t, lagertest.NewTestLogger("sem"), root, "test-node")
		srcPath := filepath.Join(root, "steps", "sem", "out")
		if err := os.MkdirAll(srcPath, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(srcPath, "f.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		dest := filepath.Join(root, "resolved", "in")
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			t.Fatal(err)
		}
		ts := httptest.NewServer(s.Handler())
		t.Cleanup(ts.Close)
		return s, ts, dest, `{"key":"sem/out","dest":"` + dest + `"}`
	}

	post := func(ctx context.Context, url, body string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		return http.DefaultClient.Do(req)
	}

	for _, route := range []struct {
		name string
		path string
		body func(item string) string
	}{
		{"POST /resolve", "/resolve", func(item string) string { return item }},
		{"POST /resolve-batch", "/resolve-batch", func(item string) string { return `{"items":[` + item + `]}` }},
	} {
		t.Run(route.name, func(t *testing.T) {
			s, ts, dest, item := seed(t)

			// Saturated: the request must NOT complete. 500ms is generous —
			// with a free slot this resolve finishes in single-digit ms.
			drain := fill(t, s)
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			resp, err := post(ctx, ts.URL+route.path, route.body(item))
			if err == nil {
				resp.Body.Close()
				t.Fatalf("%s completed while every resolve slot was held — the bound does not bind", route.name)
			}
			if _, statErr := os.Stat(filepath.Join(dest, "f.txt")); statErr == nil {
				t.Error("the copy ran despite the saturated bound")
			}

			// Released: the identical request succeeds.
			drain()
			resp, err = post(context.Background(), ts.URL+route.path, route.body(item))
			if err != nil {
				t.Fatalf("after release: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("after release: got %d, want 200", resp.StatusCode)
			}
			if _, err := os.Stat(filepath.Join(dest, "f.txt")); err != nil {
				t.Errorf("resolve reported 200 but delivered nothing: %v", err)
			}
		})
	}
}

// A caller that has gone away must release its slot rather than finish a copy
// nobody will read: the copy itself checks the request context.
func TestCopyArtifact_AbortsOnDeadContext(t *testing.T) {
	root := t.TempDir()
	s := newServerT(t, lagertest.NewTestLogger("ctx"), root, "test-node")
	srcPath := filepath.Join(root, "steps", "ctx", "out")
	if err := os.MkdirAll(srcPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcPath, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, "resolved", "in")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := s.copyArtifact(ctx, "steps/ctx/out", dest)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("copy with a dead context: err = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("an aborted copy left state at the destination (err=%v)", err)
	}
}

// newServerT is the in-package equivalent of newDaemonServer.
func newServerT(t *testing.T, logger lager.Logger, storagePath, nodeName string) *Server {
	t.Helper()
	srv, err := NewServer(logger, storagePath, nodeName)
	if err != nil {
		t.Fatalf("newServerT(t, %q): %v", storagePath, err)
	}
	return srv
}
