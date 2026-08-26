package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// AC2, durable half — see TestAC2_SwappedAliasTargetIsNeverRead for the rest.
//
// In-package because it needs a CONFIGURED durable tier. POST /durable/restore
// returns 501 before it reaches the registry lookup when s.durable is nil, so
// the version of this that lived alongside the other AC2 subtests exercised
// nothing and passed with every guard removed.
func TestAC2_DurableLookupDoesNotAnswerFromASwappedAlias(t *testing.T) {
	const secret = "SECRET-OUTSIDE-THE-STORE"

	server, ts, _ := newDaemon(t, "node-a", true)
	storagePath := server.storagePath

	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "data")
	if err := os.WriteFile(outsideFile, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(storagePath, "caches", "rc-42")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("contained"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := server.registry.RegisterAlias("rc-42", target); err != nil {
		t.Fatal(err)
	}

	restore := func() (int, []byte) {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"key": "rc-42", "durable_key": "d-rc-42"})
		resp, err := http.Post(ts.URL+"/durable/restore", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		payload, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, payload
	}

	// ZERO CASE: while the alias is contained, the lookup DOES answer "already
	// here". Without this the assertion below passes on any unrelated failure.
	code, payload := restore()
	if code != http.StatusOK || !bytes.Contains(payload, []byte(`"restored":false`)) {
		t.Fatalf("the contained alias is not reported as a local hit (%d: %s) — "+
			"the escape assertion below would prove nothing", code, payload)
	}

	// The swap, as a task container can perform it through the hostPath mount.
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideFile, target); err != nil {
		t.Fatal(err)
	}

	code, payload = restore()
	if bytes.Contains(payload, []byte("SECRET")) {
		t.Fatalf("durable lookup surfaced the escaped target: %s", payload)
	}
	if code == http.StatusOK && bytes.Contains(payload, []byte(`"restored":false`)) {
		t.Fatalf("durable lookup reported the swapped alias as a local hit, so the caller "+
			"would read the escaped target itself: %s", payload)
	}
}
