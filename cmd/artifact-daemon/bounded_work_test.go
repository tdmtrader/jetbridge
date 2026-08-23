package main_test

// R9/R10: untrusted input must not cost unbounded work, and two resolves
// naming the same dest must not interleave.
//
// /resolve-batch is the least authenticated way into resolveOne — it is
// mTLS-exempt. The daemon already bounds its AUTHENTICATED twin with uploadSem
// ("unbounded promotion is a way to run the node out of disk"); the
// unauthenticated one had no such bound.

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

// R9 — the JSON body cap. Asserted as a discriminating PAIR with the same
// valid request shape either side of the limit, because a single oversized
// request also fails validation and would pass this test for the wrong reason.
func TestBoundedWork_JSONBodyCapDiscriminates(t *testing.T) {
	ts, storagePath := setupServer(t)

	src := filepath.Join(storagePath, "steps", "cap", "out")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	post := func(padKiB int) int {
		body, _ := json.Marshal(map[string]string{
			"key":  "cap/out",
			"dest": destUnder(t, storagePath, fmt.Sprintf("cap-%d", padKiB)),
			"pad":  string(bytes.Repeat([]byte("a"), padKiB<<10)),
		})
		resp, err := http.Post(ts.URL+"/resolve", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("POST /resolve: %v", err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	under := post(256) // well under 1 MiB
	over := post(4096) // 4 MiB, comfortably over

	t.Logf("under cap -> %d, over cap -> %d", under, over)
	if under != http.StatusOK {
		t.Errorf("a valid request under the cap was refused (%d) — the cap is too tight", under)
	}
	if over == http.StatusOK {
		t.Errorf("a request over the cap was accepted (%d) — no cap in effect", over)
	}
}

// F2 guard: the artifact-STREAMING endpoints must NOT be capped. Every mirror
// push and ATC upload goes through them, and a body cap there breaks delivery.
func TestBoundedWork_ArtifactStreamingIsNotCapped(t *testing.T) {
	ts, storagePath := setupServer(t)

	// An artifact comfortably larger than the JSON cap.
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	payload := bytes.Repeat([]byte("z"), 4<<20) // 4 MiB
	if err := tw.WriteHeader(&tar.Header{
		Name: "big.bin", Typeflag: tar.TypeReg, Size: int64(len(payload)), Mode: 0o644,
	}); err != nil {
		t.Fatal(err)
	}
	tw.Write(payload)
	tw.Close()

	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/stream-in/bigbuild/out", &buf)
	req.Header.Set("Content-Type", "application/x-tar")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT /stream-in: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("a 4 MiB artifact was refused (%d) — the JSON cap leaked onto the streaming path", resp.StatusCode)
	}
	got, err := os.ReadFile(filepath.Join(storagePath, "steps", "bigbuild", "out", "big.bin"))
	if err != nil || len(got) != len(payload) {
		t.Errorf("artifact not delivered intact: %d bytes, err %v", len(got), err)
	}
}

// R9 zero-case — an ordinary batch still succeeds and preserves per-item order.
func TestBoundedWork_OrdinaryBatchStillWorks(t *testing.T) {
	ts, storagePath := setupServer(t)

	type item struct{ Key, Dest string }
	var list []item
	for i := 0; i < 4; i++ {
		src := filepath.Join(storagePath, "steps", fmt.Sprintf("ok-%d", i), "out")
		if err := os.MkdirAll(src, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(src, "f.txt"), []byte(fmt.Sprint(i)), 0o644); err != nil {
			t.Fatal(err)
		}
		list = append(list, item{fmt.Sprintf("ok-%d/out", i), destUnder(t, storagePath, fmt.Sprintf("ok-dest-%d", i))})
	}

	body, _ := json.Marshal(map[string]any{"items": list})
	resp, err := http.Post(ts.URL+"/resolve-batch", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /resolve-batch: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for an ordinary batch, got %d", resp.StatusCode)
	}
	for i := 0; i < 4; i++ {
		p := filepath.Join(storagePath, "resolved", fmt.Sprintf("ok-dest-%d", i), "f.txt")
		got, err := os.ReadFile(p)
		if err != nil || string(got) != fmt.Sprint(i) {
			t.Errorf("item %d: got %q err %v — ordering or delivery broken", i, got, err)
		}
	}
}

// R10 — concurrent resolves naming the same dest must all SUCCEED.
//
// The first version of this test asserted the final tree was intact, and it
// passed with and without the fix — temp-dir-plus-rename means the last writer
// always leaves one complete tree, so that property was never at risk. The
// property that IS at risk is the caller's result: copyArtifact does
// os.RemoveAll(dest) then os.Rename(tmp, dest), and a rename onto a directory
// another copy just populated fails. Unserialised, a legitimate resolve reports
// an error it should not.
func TestBoundedWork_ConcurrentResolvesToSameDestAllSucceed(t *testing.T) {
	ts, storagePath := setupServer(t)

	const sources = 8
	for i := 0; i < sources; i++ {
		src := filepath.Join(storagePath, "steps", fmt.Sprintf("race-%d", i), "out")
		if err := os.MkdirAll(src, 0o755); err != nil {
			t.Fatal(err)
		}
		// Enough content that the copy window is wide enough to overlap.
		for j := 0; j < 40; j++ {
			if err := os.WriteFile(filepath.Join(src, fmt.Sprintf("f-%d", j)),
				bytes.Repeat([]byte("x"), 4096), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	dest := destUnder(t, storagePath, "shared-dest")

	var wg sync.WaitGroup
	var failures int64
	for i := 0; i < sources; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body, _ := json.Marshal(map[string]string{
				"key": fmt.Sprintf("race-%d/out", i), "dest": dest,
			})
			resp, err := http.Post(ts.URL+"/resolve", "application/json", bytes.NewReader(body))
			if err != nil {
				atomic.AddInt64(&failures, 1)
				return
			}
			defer resp.Body.Close()
			var out resolveResponse
			json.NewDecoder(resp.Body).Decode(&out)
			if resp.StatusCode != http.StatusOK || out.Status != "ok" {
				atomic.AddInt64(&failures, 1)
				t.Logf("resolve %d failed: status=%d body=%+v", i, resp.StatusCode, out)
			}
		}(i)
	}
	wg.Wait()

	if n := atomic.LoadInt64(&failures); n > 0 {
		t.Errorf("%d/%d concurrent resolves to a shared dest failed — copies are not serialised on dest", n, sources)
	}
}
