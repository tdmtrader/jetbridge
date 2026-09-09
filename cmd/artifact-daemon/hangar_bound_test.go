package main

// R9 for the Hangar route: untrusted input must not cost unbounded work.
//
// POST /hangar/v1/materializations is mTLS-exempt, exactly like /resolve and
// /resolve-batch, and each item is a full store open, capture and verified copy
// into scratch. Core bounds its two exempt routes node-wide with resolveSem;
// this route had no bound at all, so anyone who can read a Pod spec held a
// replayable amplification token against the node's daemon.
//
// The bound is asserted through REAL concurrent HTTP requests, not by taking
// slots on the channel and looking at the channel: an earlier version of the
// resolve test did that and stayed green when the acquire was deleted from the
// handler (see destlock_test.go). Here the in-flight requests are parked inside
// the store, so the bound has to be enforced where requests arrive.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/output/ledger"
)

func TestHangarMaterializationBoundsConcurrentWorkAndCountsTheRefusal(t *testing.T) {
	raw := rawHangarTar(t, "file", "content")
	canonical, digest := canonicalHangarTree(t, t.TempDir(), raw)
	ref := hangar.TreeRef{Scope: "ci", Digest: digest, Generation: 1}

	entered := make(chan struct{}, 64)
	release := make(chan struct{})
	var opens atomic.Int64

	store := &hangarStoreStub{ensure: func(context.Context, hangar.Scope, hangar.Digest, io.Reader, int64) (hangar.TreeAttributes, bool, error) {
		panic("unexpected EnsureTree")
	}}
	store.open = func(ctx context.Context, got hangar.TreeRef, _ int64) (io.ReadCloser, hangar.TreeAttributes, error) {
		opens.Add(1)
		entered <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return nil, hangar.TreeAttributes{}, ctx.Err()
		case <-time.After(30 * time.Second):
			return nil, hangar.TreeAttributes{}, errors.New("materialization parked too long")
		}
		return io.NopCloser(bytes.NewReader(canonical)), hangar.TreeAttributes{Ref: got, LogicalBytes: int64(len(canonical))}, nil
	}

	server, _, key := newHangarTestServer(t, store)
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()
	signer, err := hangar.NewGrantSigner(key, time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 60 * time.Second}

	post := func(volume string) (int, string) {
		grant, err := signer.Sign(ref, "handle", volume)
		if err != nil {
			return 0, err.Error()
		}
		body, _ := json.Marshal(map[string]any{"items": []any{
			map[string]any{"ref": ref, "handle": "handle", "volume": volume, "grant": "Bearer " + grant},
		}})
		resp, err := client.Post(ts.URL+"/hangar/v1/materializations", "application/json", bytes.NewReader(body))
		if err != nil {
			return 0, err.Error()
		}
		defer resp.Body.Close()
		read, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, strings.TrimSpace(string(read))
	}

	bound := cap(server.hangarSem)
	if bound <= 0 {
		t.Fatalf("hangarSem has capacity %d — the route is unbounded", bound)
	}

	statuses := make(chan int, bound)
	for i := 0; i < bound; i++ {
		go func(i int) {
			code, _ := post(fmt.Sprintf("volume-%d", i))
			statuses <- code
		}(i)
	}
	for i := 0; i < bound; i++ {
		select {
		case <-entered:
		case <-time.After(30 * time.Second):
			t.Fatalf("only %d of %d materializations reached the store", i, bound)
		}
	}

	// Every slot is held by a request that is still inside Materialize.
	before := refusalCount(t, server)
	code, body := post("volume-overflow")
	if code != http.StatusServiceUnavailable {
		t.Errorf("request %d with %d in flight = %d %q, want 503 — the route is unbounded",
			bound+1, bound, code, body)
	}
	// Identical to every other Hangar 503, so the init container's existing
	// retry-on-503 loop covers overload without learning a new status, and
	// nothing about the daemon's load is disclosed to an unauthenticated
	// caller beyond "not now".
	if body != "service unavailable" {
		t.Errorf("overflow body = %q, want the same %q every Hangar 503 returns", body, "service unavailable")
	}
	if got := refusalCount(t, server) - before; got != 1 {
		t.Errorf("refusals_total rose by %v, want 1 — an overload refusal must be visible: %v",
			got, labelsOf(t, server))
	}
	if want := "POST /hangar/v1/materializations " + reasonOverloaded; !slices.Contains(labelsOf(t, server), want) {
		t.Errorf("refusal labels %v do not include %q", labelsOf(t, server), want)
	}
	if got := opens.Load(); got != int64(bound) {
		t.Errorf("store opened %d times, want %d — a refused request must do no work", got, bound)
	}

	close(release)
	for i := 0; i < bound; i++ {
		if code := <-statuses; code != http.StatusNoContent {
			t.Errorf("in-flight materialization returned %d, want 204", code)
		}
	}

	// The slots come back: the bound throttles, it does not latch.
	if code, body := post("volume-after"); code != http.StatusNoContent {
		t.Errorf("after the batch drained, materialization = %d %q, want 204", code, body)
	}
}

// The output plane's hold, seen from THIS daemon.
//
// Two daemons, two authorities over one node's disk: the output daemon owns
// which sources a capture holds, and this one owns everything else. What is
// under test is that the second respects the first -- and that it fails closed
// when it cannot read what the first said, because a delete on a guess is how a
// build's declared output disappears with no record it existed.
func TestDeleteRefusesACaptureHeldSourceAndFailsClosedOnAnUnreadableLedger(t *testing.T) {
	storage := t.TempDir()
	if err := os.MkdirAll(filepath.Join(storage, "steps", "ordinary-handle"), 0o755); err != nil {
		t.Fatalf("creating an ordinary step: %v", err)
	}
	control := filepath.Join(storage, ledger.ControlDirName)
	if err := os.MkdirAll(control, 0o700); err != nil {
		t.Fatalf("creating the control directory: %v", err)
	}

	const incarnation = "33333333-3333-4333-8333-333333333333.4/result"
	if err := os.MkdirAll(filepath.Join(storage, "steps", incarnation), 0o700); err != nil {
		t.Fatalf("creating the held source: %v", err)
	}

	server, err := NewServer(lagertest.NewTestLogger("capture-held"), storage, "node-1")
	if err != nil {
		t.Fatalf("building the server: %v", err)
	}
	handler := server.Handler()

	remove := func(key string) int {
		request := httptest.NewRequest(http.MethodDelete, "/artifacts/"+key, nil)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)

		return recorder.Code
	}

	// The control FIRST: with no hold recorded, an ordinary delete proceeds and
	// so does a delete of the incarnation path. "Everything is refused" is what
	// a broken guard looks like.
	if code := remove("steps/ordinary-handle"); code != http.StatusNoContent {
		t.Fatalf("an ordinary delete answered %d", code)
	}
	if _, err := os.Stat(filepath.Join(storage, "steps", "ordinary-handle")); err == nil {
		t.Error("the ordinary delete removed nothing")
	}

	// Now the hold.
	record, err := json.Marshal(map[string]any{
		"record_version": "hangar-output-control-record-v1",
		"checksum":       "not-read-by-the-classifier",
		"body": map[string]any{
			"state": "held",
			"incarnation": map[string]any{
				"execution_id":      "33333333-3333-4333-8333-333333333333",
				"handle_generation": 4,
				"output":            "result",
			},
		},
	})
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	if err := os.WriteFile(filepath.Join(control, "source-11111111-1111-4111-8111-111111111111.json"),
		record, 0o600); err != nil {
		t.Fatalf("writing the hold: %v", err)
	}

	if code := remove("steps/" + incarnation); code != http.StatusConflict {
		t.Errorf("a delete of a capture-held source answered %d", code)
	}
	if _, err := os.Stat(filepath.Join(storage, "steps", incarnation)); err != nil {
		t.Errorf("the held source was removed anyway: %v", err)
	}

	// An unrelated step still deletes, so the refusal is about the hold and not
	// about the daemon having given up.
	if err := os.MkdirAll(filepath.Join(storage, "steps", "another-handle"), 0o755); err != nil {
		t.Fatalf("creating: %v", err)
	}
	if code := remove("steps/another-handle"); code != http.StatusNoContent {
		t.Errorf("an unrelated delete answered %d while a hold existed", code)
	}

	// And the fail-closed half: a ledger this daemon cannot read refuses
	// EVERY delete, because the ledger is the only thing that could have said
	// which paths are held.
	if err := os.WriteFile(filepath.Join(control, "source-11111111-1111-4111-8111-111111111111.json"),
		[]byte("{not json"), 0o600); err != nil {
		t.Fatalf("corrupting: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(storage, "steps", "third-handle"), 0o755); err != nil {
		t.Fatalf("creating: %v", err)
	}
	// The classifier's cache is invalidated by the control directory's mtime,
	// which an IN-PLACE edit does not change -- the writer replaces records by
	// rename, so this corruption is something the writer would never do. The
	// TTL is the ceiling for exactly that case, and waiting it out is the test
	// asserting the daemon's behaviour rather than reaching into the cache.
	time.Sleep(1100 * time.Millisecond)
	if code := remove("steps/third-handle"); code != http.StatusConflict {
		t.Errorf("an unreadable ledger admitted a delete with %d", code)
	}
	if _, err := os.Stat(filepath.Join(storage, "steps", "third-handle")); err != nil {
		t.Errorf("a delete proceeded on a guess: %v", err)
	}
}
