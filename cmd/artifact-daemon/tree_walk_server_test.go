package main_test

// The server side of the source-tree walk's symlink rule: the routes that
// serve or copy a location refuse one reached through a symlink, and say so.

import (
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const servedVictimSecret = "VICTIM-SECRET-CONTENT"

// filesUnderContaining lists the regular files under dir, found without
// following any link, whose content includes needle.
func filesUnderContaining(t *testing.T, dir, needle string) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) && p == dir {
				return nil
			}
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if strings.Contains(string(body), needle) {
			found = append(found, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

// GET and HEAD refuse a location reached through a symlink up front, with a
// 422, instead of answering 200 and aborting the stream when the walk refuses.
// resolve reports the same location as a refusal rather than a daemon error.
func TestServer_RefusesASymlinkedLocationUpFront(t *testing.T) {
	seed := func(t *testing.T, storagePath string) {
		t.Helper()
		victim := filepath.Join(storagePath, "steps", "victim", "out")
		if err := os.MkdirAll(victim, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(victim, "secret.txt"), []byte(servedVictimSecret), 0o644); err != nil {
			t.Fatal(err)
		}
		attacker := filepath.Join(storagePath, "steps", "attacker")
		if err := os.MkdirAll(filepath.Join(attacker, "out"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("../../victim/out", filepath.Join(attacker, "out", "link")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("../victim", filepath.Join(attacker, "hop")); err != nil {
			t.Fatal(err)
		}
	}

	for _, key := range []string{"steps/attacker/out/link", "steps/attacker/hop/out"} {
		t.Run(key, func(t *testing.T) {
			ts, storagePath := setupServer(t)
			seed(t, storagePath)

			// Zero case: the real location is served.
			ok, err := http.Get(ts.URL + "/artifacts/steps/victim/out")
			if err != nil {
				t.Fatal(err)
			}
			ok.Body.Close()
			if ok.StatusCode != http.StatusOK {
				t.Fatalf("the real location answered %d, want 200", ok.StatusCode)
			}

			get, err := http.Get(ts.URL + "/artifacts/" + key)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(get.Body)
			get.Body.Close()
			if get.StatusCode != http.StatusUnprocessableEntity {
				t.Errorf("GET answered %d, want 422", get.StatusCode)
			}
			if strings.Contains(string(body), servedVictimSecret) {
				t.Error("GET served another step's file content")
			}

			head, err := http.Head(ts.URL + "/artifacts/" + key)
			if err != nil {
				t.Fatal(err)
			}
			head.Body.Close()
			if head.StatusCode != http.StatusUnprocessableEntity {
				t.Errorf("HEAD answered %d, want 422", head.StatusCode)
			}
		})
	}

	t.Run("a registered location, by resolve and the resource-cache routes", func(t *testing.T) {
		ts, storagePath := setupServer(t)
		seed(t, storagePath)

		// Registered while it was a real directory, then replaced by a link
		// that stays inside the store, so the registry's use-time containment
		// check passes it and the walk is what meets it.
		real := filepath.Join(storagePath, "steps", "attacker", "out", "real")
		if err := os.MkdirAll(real, 0o755); err != nil {
			t.Fatal(err)
		}
		reg, _ := json.Marshal(map[string]string{"key": "alias1", "local_path": real})
		rr, err := http.Post(ts.URL+"/register", "application/json", strings.NewReader(string(reg)))
		if err != nil {
			t.Fatal(err)
		}
		rr.Body.Close()
		if rr.StatusCode != http.StatusOK && rr.StatusCode != http.StatusCreated {
			t.Fatalf("register answered %d", rr.StatusCode)
		}
		if err := os.Remove(real); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("../../victim/out", real); err != nil {
			t.Fatal(err)
		}

		dest := destUnder(t, storagePath, "d1")
		reqBody, _ := json.Marshal(batchRequest{Items: []batchItem{signedItem(t, "alias1", dest)}})
		resp, err := http.Post(ts.URL+"/resolve-batch", "application/json", strings.NewReader(string(reqBody)))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var answer batchResponse
		if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusUnprocessableEntity || len(answer.Results) != 1 || answer.Results[0].Method != "refused" {
			t.Fatalf("want a 422 refusal, got %d %+v", resp.StatusCode, answer)
		}
		if leaked := filesUnderContaining(t, filepath.Join(storagePath, "resolved"), servedVictimSecret); len(leaked) > 0 {
			t.Fatalf("resolve copied another step's file content into %v", leaked)
		}

		// The resource-cache routes serve the same registered location.
		get, err := http.Get(ts.URL + "/resource-caches/alias1")
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(get.Body)
		get.Body.Close()
		if get.StatusCode != http.StatusUnprocessableEntity || strings.Contains(string(body), servedVictimSecret) {
			t.Errorf("GET /resource-caches/ answered %d, want 422 with no content", get.StatusCode)
		}
		head, err := http.Head(ts.URL + "/resource-caches/alias1")
		if err != nil {
			t.Fatal(err)
		}
		head.Body.Close()
		if head.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("HEAD /resource-caches/ answered %d, want 422", head.StatusCode)
		}
	})
}
