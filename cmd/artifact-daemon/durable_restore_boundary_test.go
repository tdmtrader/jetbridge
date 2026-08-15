package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDurableRestoreRejectsMalformedRequestAndObject(t *testing.T) {
	server, httpServer, store := newDaemon(t, "node-a", true)

	malformed := post(t, httpServer, "/durable/restore", "{")
	if malformed.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed request returned %d, want 400", malformed.StatusCode)
	}

	const durableKey = "resource-caches/corrupt"
	if err := store.Put(context.Background(), durableKey, strings.NewReader("not a tar archive")); err != nil {
		t.Fatalf("seed malformed durable object: %v", err)
	}
	resp := post(t, httpServer, "/durable/restore", `{"key":"corrupt","durable_key":"resource-caches/corrupt"}`)
	if resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("malformed durable object returned %d, want 404: %s", resp.StatusCode, body)
	}
	if _, err := os.Stat(filepath.Join(server.storagePath, "steps", "corrupt")); !os.IsNotExist(err) {
		t.Fatalf("malformed durable object left a partial local artifact: %v", err)
	}
}

// A peer fetch can land after the durable GET starts but before its atomic
// rename. Both copies name the same content-derived cache, so the first local
// winner remains authoritative and the durable restore registers that winner.
func TestDurableRestoreKeepsRacingLocalCopy(t *testing.T) {
	server, httpServer, store := newDaemon(t, "node-a", true)

	source := writeDir(t, t.TempDir(), "durable-source", map[string]string{"from-durable": "incoming"})
	var archive bytes.Buffer
	if err := server.tarDirectory(&archive, source); err != nil {
		t.Fatalf("tar durable source: %v", err)
	}
	const durableKey = "resource-caches/raced"
	if err := store.Put(context.Background(), durableKey, &archive); err != nil {
		t.Fatalf("seed durable object: %v", err)
	}

	destination := filepath.Join(server.storagePath, "steps", "raced")
	if err := os.MkdirAll(destination, 0o755); err != nil {
		t.Fatalf("create racing local copy: %v", err)
	}
	winner := filepath.Join(destination, "from-peer")
	if err := os.WriteFile(winner, []byte("winner"), 0o644); err != nil {
		t.Fatalf("write racing local copy: %v", err)
	}

	resp := post(t, httpServer, "/durable/restore", `{"key":"raced","durable_key":"resource-caches/raced"}`)
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("racing restore returned %d, want 201: %s", resp.StatusCode, body)
	}
	if body, err := os.ReadFile(winner); err != nil || string(body) != "winner" {
		t.Fatalf("durable restore replaced racing local winner: body=%q err=%v", body, err)
	}
	if _, err := os.Stat(filepath.Join(destination, "from-durable")); !os.IsNotExist(err) {
		t.Fatalf("durable restore merged bytes into racing local tree: %v", err)
	}

	// The first response registered the surviving local tree. A retry therefore
	// answers immediately from the local tier without another durable transfer.
	retry := post(t, httpServer, "/durable/restore", `{"key":"raced","durable_key":"resource-caches/raced"}`)
	if retry.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(retry.Body)
		t.Fatalf("retry after racing restore returned %d, want 200: %s", retry.StatusCode, body)
	}
	if got := retry.Header.Get(ArtifactTierHeader); got != "local" {
		t.Errorf("retry tier = %q, want local", got)
	}
}
