package main

import (
	"bytes"
	"encoding/json"
	"github.com/concourse/concourse/artifactwire"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"

	"github.com/concourse/concourse/artifactcap"
)

// /resolve-batch is mTLS-EXEMPT by design — the init container
// dials the daemon by node IP, which cannot be a certificate SAN — so the
// capability key is the only authentication it can have. It takes a
// caller-supplied dest that becomes a RemoveAll and a Rename.
//
// This branch had dropped the control entirely while the chart still mounted
// the secret and passed the flag, so the endpoint was open to anything that
// could reach the port.
func TestResolveCapability(t *testing.T) {
	signer, err := artifactcap.NewSigner(testResolveKey)
	if err != nil {
		t.Fatal(err)
	}

	setup := func(t *testing.T) (*httptest.Server, string, string) {
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
		ts := httptest.NewServer(s.Handler())
		t.Cleanup(ts.Close)
		// The dest is the shape the ATC actually sends — steps/<handle>/<volume>
		// — because validateResolveDest runs BEFORE the capability check and
		// refuses a shallow or structural destination outright. A fixture using
		// a one-segment dest would get a 400 where these tests mean to assert a
		// 403, testing the wrong rule. Its parent exists in production because
		// the input volume is a HostPathDirectoryOrCreate mount.
		consumer := filepath.Join(storage, "steps", "consumer-1")
		if err := os.MkdirAll(consumer, 0o755); err != nil {
			t.Fatal(err)
		}
		return ts, "build-1/out", filepath.Join(consumer, "input-0")
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
		ts, k, dest := setup(t)
		if code := post(t, ts, "/resolve-batch", artifactwire.BatchResolveRequest{Items: []artifactwire.ResolveRequest{{Key: k, Dest: dest}}}); code != http.StatusForbidden {
			t.Errorf("expected 403, got %d", code)
		}
	})

	t.Run("a valid capability is accepted", func(t *testing.T) {
		ts, k, dest := setup(t)
		code := post(t, ts, "/resolve-batch", artifactwire.BatchResolveRequest{Items: []artifactwire.ResolveRequest{{Key: k, Dest: dest, Capability: valid(t, k, dest)}}})
		if code != http.StatusOK {
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
		ts, k, dest := setup(t)
		if code := post(t, ts, "/resolve-batch", artifactwire.BatchResolveRequest{Items: []artifactwire.ResolveRequest{{
			Key: k, Dest: dest, Capability: valid(t, "other/key", dest),
		}}}); code != http.StatusForbidden {
			t.Errorf("expected 403, got %d", code)
		}
	})

	t.Run("a capability for another dest is refused", func(t *testing.T) {
		ts, k, dest := setup(t)
		if code := post(t, ts, "/resolve-batch", artifactwire.BatchResolveRequest{Items: []artifactwire.ResolveRequest{{
			Key: k, Dest: dest, Capability: valid(t, k, dest+"-elsewhere"),
		}}}); code != http.StatusForbidden {
			t.Errorf("expected 403, got %d", code)
		}
	})

	t.Run("an expired capability is refused", func(t *testing.T) {
		ts, k, dest := setup(t)
		tok, err := signer.SignResolve(k, dest, time.Now().Add(-time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if code := post(t, ts, "/resolve-batch", artifactwire.BatchResolveRequest{Items: []artifactwire.ResolveRequest{{Key: k, Dest: dest, Capability: tok}}}); code != http.StatusForbidden {
			t.Errorf("expected 403, got %d", code)
		}
	})

	// Every item is authorized BEFORE any is started, so a refused batch has no
	// side effects — the same rule the containment checks follow.
	t.Run("batch refuses if any item lacks a capability", func(t *testing.T) {
		ts, k, dest := setup(t)
		second := dest + "-2"
		code := post(t, ts, "/resolve-batch", artifactwire.BatchResolveRequest{Items: []artifactwire.ResolveRequest{
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

}

func TestResolveCapability_RejectsAShortKey(t *testing.T) {
	if _, err := NewServer(lagertest.NewTestLogger("cap"), t.TempDir(), "node", []byte("too-short")); err == nil {
		t.Fatal("a short key was accepted; the daemon would run with a weak secret")
	}
	if _, err := NewServer(lagertest.NewTestLogger("cap"), t.TempDir(), "node", nil); err == nil {
		t.Fatal("a daemon without a resolve key started; its resolve route has no authentication")
	}
}

// testResolveKey is the resolve capability key every in-package test daemon is
// started with; testCapability signs as the web would.
var testResolveKey = []byte("0123456789abcdef0123456789abcdef")

func testCapability(t testing.TB, key, dest string) string {
	t.Helper()
	signer, err := artifactcap.NewSigner(testResolveKey)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := signer.SignResolve(key, dest, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// resolveItem is one /resolve-batch item signed with testResolveKey.
func resolveItem(t testing.TB, key, dest string) string {
	t.Helper()
	item, err := json.Marshal(artifactwire.ResolveRequest{Key: key, Dest: dest, Capability: testCapability(t, key, dest)})
	if err != nil {
		t.Fatal(err)
	}
	return string(item)
}
