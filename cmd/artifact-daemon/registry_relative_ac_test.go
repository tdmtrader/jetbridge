package main_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"

	daemon "github.com/concourse/concourse/cmd/artifact-daemon"
)

// AC3 — an out-of-root alias is not expressible.
//
// POST /register carries a client-supplied local_path. It is the only Register
// call whose input is not derived from the daemon's own tree, and therefore the
// only place an escaping value could previously be introduced through the API.
func TestAC3_RegisterRefusesAnOutOfRootLocalPath(t *testing.T) {
	ts, storagePath, _ := setupServerWithRegistry(t)

	outside := t.TempDir() // a sibling of storagePath, not under it
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("s"), 0o600); err != nil {
		t.Fatal(err)
	}

	post := func(t *testing.T, key, localPath string) int {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"key": key, "local_path": localPath})
		resp, err := http.Post(ts.URL+"/register", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	for _, tc := range []struct {
		name string
		path string
	}{
		{"a directory outside the root", outside},
		{"a traversal out of the root", filepath.Join(storagePath, "steps", "..", "..")},
		{"the storage root itself", storagePath},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if code := post(t, "vol-escape", tc.path); code != http.StatusBadRequest {
				t.Errorf("expected 400 for %q, got %d", tc.path, code)
			}
			// Not merely rejected at the edge — not stored either.
			resp, err := http.Get(ts.URL + "/artifacts/vol-escape")
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("a refused registration is still resolvable: GET -> %d", resp.StatusCode)
			}
		})
	}

	// Zero case: a contained local_path is accepted and retrievable. Without
	// this the criterion is satisfiable by refusing everything.
	inside := filepath.Join(storagePath, "steps", "ok", "out")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inside, "f.txt"), []byte("contained"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := post(t, "vol-ok", inside); code != http.StatusCreated {
		t.Fatalf("a contained local_path was refused: %d", code)
	}
	resp, err := http.Get(ts.URL + "/artifacts/vol-ok")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("a contained registration is not retrievable: %d", resp.StatusCode)
	}
}

// A registry alias resolved, then swapped for an outward symlink before use,
// must yield no read of the swapped target.
//
// Written as a SEQUENCE, not a race, so it cannot be flaky. The route is real:
// task containers write into the store through the hostPath mount, so a symlink
// can appear without the daemon being involved — which is why validating at
// registration is not enough.
//
// The swapped target is a DIRECTORY, because an artifact is one: the artifact daemon
// serves nothing else, so a symlink to a file reaches no read at all. The
// directory read goes through the root handle, which refuses an escaping
// link on its own, so the status pins what this test is really about:
// lookupRegistry's use-time check evicts the swapped alias, and the answer is
// a 404 -- not a 500 from the handle refusing a path the registry still
// believed in. Deleting the use-time check turns the 404 into a 500 and
// reddens this; the no-bytes assertion is the floor beneath it.
func TestAC2_SwappedAliasTargetIsNeverRead(t *testing.T) {
	const secret = "SECRET-OUTSIDE-THE-STORE"
	const contained = "legitimately-contained-content"

	// setup registers a legitimate alias to a real directory inside the store
	// and returns the base URL plus a swap function that replaces that
	// directory with a symlink pointing out of the store.
	setup := func(t *testing.T) (base string, swap func()) {
		t.Helper()
		ts, storagePath, server := setupServerWithRegistry(t)

		outside := t.TempDir()
		if err := os.WriteFile(filepath.Join(outside, "data"), []byte(secret), 0o600); err != nil {
			t.Fatal(err)
		}

		// A real directory, registered legitimately: at this moment the alias
		// is contained and would pass any registration-time check.
		target := filepath.Join(storagePath, "caches", "rc-42")
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(target, "data"), []byte(contained), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := server.Registry().RegisterAlias("rc-42", target); err != nil {
			t.Fatal(err)
		}

		return ts.URL, func() {
			// What a task container can do through the hostPath mount without
			// the daemon being involved.
			if err := os.RemoveAll(target); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, target); err != nil {
				t.Fatal(err)
			}
		}
	}

	body := func(t *testing.T, resp *http.Response) []byte {
		t.Helper()
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil // a hard abort mid-stream is a refusal, not a leak
		}
		return b
	}

	for _, route := range []string{"/artifacts/rc-42", "/resource-caches/rc-42"} {
		t.Run("GET "+route, func(t *testing.T) {
			base, swap := setup(t)

			// ZERO CASE FIRST. Without it a broken route 404s and the escape
			// assertion below passes for the wrong reason — which is exactly
			// how the previous version of this test went green.
			resp, err := http.Get(base + route)
			if err != nil {
				t.Fatal(err)
			}
			if got := body(t, resp); !bytes.Contains(got, []byte(contained)) {
				t.Fatalf("the contained alias is not served at all (status %d, %d bytes) — "+
					"the escape assertion below would prove nothing", resp.StatusCode, len(got))
			}

			swap()

			resp, err = http.Get(base + route)
			if err != nil {
				t.Fatal(err)
			}
			got := body(t, resp)
			if bytes.Contains(got, []byte(secret)) {
				t.Fatalf("content from outside the storage root was served (status %d)", resp.StatusCode)
			}
			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("a swapped alias answered %d, want 404: the use-time check evicts it", resp.StatusCode)
			}
		})

		t.Run("HEAD "+route, func(t *testing.T) {
			base, swap := setup(t)

			head := func() int {
				req, _ := http.NewRequest(http.MethodHead, base+route, nil)
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				resp.Body.Close()
				return resp.StatusCode
			}

			if code := head(); code != http.StatusOK {
				t.Fatalf("the contained alias is not reported present (%d) — "+
					"the escape assertion below would prove nothing", code)
			}

			swap()

			// HEAD serves no body, so the assertion is that it does not report
			// the escaping target as present: a 200 tells a peer to fetch it.
			if code := head(); code == http.StatusOK {
				t.Error("HEAD reported an escaping alias as present; a peer would fetch it")
			}
		})
	}

}

// AC4's positive half, at the level the spec states it: EVERY value in
// aliases.json is relative — no leading separator and no storagePath substring.
//
// Track 6's AC10 test asserts stability across two runs and would have passed
// either side of this change; the spec called that out explicitly, so this is
// the assertion that actually holds the format.
func TestAC4_EveryAliasValueIsRelative(t *testing.T) {
	ts, storagePath, server := setupServerWithRegistry(t)
	server.Registry().SetAliasStore(newAliasStoreT(t, storagePath, server))

	for _, k := range []string{"vol-a", "nested/vol-b", "steps-like"} {
		dir := filepath.Join(storagePath, "caches", filepath.FromSlash(k))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		body, _ := json.Marshal(map[string]string{"key": k, "local_path": dir})
		resp, err := http.Post(ts.URL+"/register", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("register %s -> %d", k, resp.StatusCode)
		}
	}

	data, err := os.ReadFile(filepath.Join(storagePath, "aliases.json"))
	if err != nil {
		t.Fatalf("aliases.json not written: %v", err)
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if len(m) != 3 {
		t.Fatalf("expected 3 aliases, got %d: %v", len(m), m)
	}
	for k, v := range m {
		if strings.HasPrefix(v, "/") {
			t.Errorf("%s: value %q has a leading separator", k, v)
		}
		if strings.Contains(v, storagePath) {
			t.Errorf("%s: value %q contains the storage root", k, v)
		}
	}
}

func newAliasStoreT(t *testing.T, storagePath string, server *daemon.Server) *daemon.AliasStore {
	t.Helper()
	return daemon.NewAliasStore(lagertest.NewTestLogger("alias"), storagePath, server.Root())
}
