package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"

	"github.com/concourse/concourse/artifactcap"
)

// /resolve and /resolve-batch are mTLS-EXEMPT by design — the init container
// dials the daemon by node IP, which cannot be a certificate SAN — so the
// capability key is the only authentication they can have. They take a
// caller-supplied dest that becomes a RemoveAll and a Rename.
//
// This branch had dropped the control entirely while the chart still mounted
// the secret and passed the flag, so the endpoint was open to anything that
// could reach the port.
func TestResolveCapability(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	signer, err := artifactcap.NewSigner(key)
	if err != nil {
		t.Fatal(err)
	}

	setup := func(t *testing.T, withKey bool) (*httptest.Server, string, string) {
		t.Helper()
		storage := t.TempDir()
		s := newServerT(t, lagertest.NewTestLogger("cap"), storage, "node")
		src := filepath.Join(storage, "steps", "build-1", "out")
		if err := os.MkdirAll(src, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(src, "f.txt"), []byte("payload"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := s.registry.Register("build-1/out", src); err != nil {
			t.Fatal(err)
		}
		if withKey {
			if err := s.SetResolveCapabilityKey(key); err != nil {
				t.Fatal(err)
			}
		}
		ts := httptest.NewServer(s.Handler())
		t.Cleanup(ts.Close)
		// dest must be inside the storage root: validateContainedPath runs first.
		return ts, "build-1/out", filepath.Join(storage, "delivered")
	}

	post := func(t *testing.T, ts *httptest.Server, path string, body any) int {
		t.Helper()
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.Post(ts.URL+path, "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	valid := func(t *testing.T, k, dest string) string {
		t.Helper()
		tok, err := signer.SignResolve(k, dest, time.Now().Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}

	t.Run("no capability is refused", func(t *testing.T) {
		ts, k, dest := setup(t, true)
		if code := post(t, ts, "/resolve", resolveRequest{Key: k, Dest: dest}); code != http.StatusForbidden {
			t.Errorf("expected 403, got %d", code)
		}
	})

	t.Run("a valid capability is accepted", func(t *testing.T) {
		ts, k, dest := setup(t, true)
		code := post(t, ts, "/resolve", resolveRequest{Key: k, Dest: dest, Capability: valid(t, k, dest)})
		if code == http.StatusForbidden {
			t.Fatal("a validly signed capability was refused — the rule is satisfied by refusing everything")
		}
		if _, err := os.Stat(filepath.Join(dest, "f.txt")); err != nil {
			t.Errorf("resolve authorized but delivered nothing: %v", err)
		}
	})

	// The capability binds to its exact key AND dest. Without this it would be
	// a bearer token: one legitimate capability would authorize a copy of any
	// artifact to any destination.
	t.Run("a capability for another key is refused", func(t *testing.T) {
		ts, k, dest := setup(t, true)
		if code := post(t, ts, "/resolve", resolveRequest{
			Key: k, Dest: dest, Capability: valid(t, "other/key", dest),
		}); code != http.StatusForbidden {
			t.Errorf("expected 403, got %d", code)
		}
	})

	t.Run("a capability for another dest is refused", func(t *testing.T) {
		ts, k, dest := setup(t, true)
		if code := post(t, ts, "/resolve", resolveRequest{
			Key: k, Dest: dest, Capability: valid(t, k, dest+"-elsewhere"),
		}); code != http.StatusForbidden {
			t.Errorf("expected 403, got %d", code)
		}
	})

	t.Run("an expired capability is refused", func(t *testing.T) {
		ts, k, dest := setup(t, true)
		tok, err := signer.SignResolve(k, dest, time.Now().Add(-time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if code := post(t, ts, "/resolve", resolveRequest{Key: k, Dest: dest, Capability: tok}); code != http.StatusForbidden {
			t.Errorf("expected 403, got %d", code)
		}
	})

	// Every item is authorized BEFORE any is started, so a refused batch has no
	// side effects — the same rule the containment checks follow.
	t.Run("batch refuses if any item lacks a capability", func(t *testing.T) {
		ts, k, dest := setup(t, true)
		second := dest + "-2"
		code := post(t, ts, "/resolve-batch", batchResolveRequest{Items: []resolveRequest{
			{Key: k, Dest: dest, Capability: valid(t, k, dest)},
			{Key: k, Dest: second}, // unauthorized
		}})
		if code != http.StatusForbidden {
			t.Fatalf("expected 403, got %d", code)
		}
		if _, err := os.Stat(filepath.Join(dest, "f.txt")); err == nil {
			t.Error("the authorized item ran anyway — a refused batch must have no side effects")
		}
	})

	// Compatibility: a daemon started without the flag behaves exactly as this
	// branch does today. main.go says so loudly at startup.
	t.Run("no key configured leaves the route open", func(t *testing.T) {
		ts, k, dest := setup(t, false)
		if code := post(t, ts, "/resolve", resolveRequest{Key: k, Dest: dest}); code == http.StatusForbidden {
			t.Error("an unconfigured daemon refused a resolve; that breaks every existing deployment")
		}
	})
}

func TestResolveCapability_RejectsAShortKey(t *testing.T) {
	s := newServerT(t, lagertest.NewTestLogger("cap"), t.TempDir(), "node")
	if err := s.SetResolveCapabilityKey([]byte("too-short")); err == nil {
		t.Fatal("a short key was accepted; the daemon would run with a weak secret")
	}
	if s.resolveVerifier != nil {
		t.Error("a rejected key still left a verifier installed")
	}
}
