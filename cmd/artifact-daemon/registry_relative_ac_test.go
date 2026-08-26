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

// AC2 — the TOCTOU is closed for the handle-backed consumers.
//
// Written as a SEQUENCE, not a race, so it cannot be flaky: register a real
// alias, then replace its target with a symlink pointing out of the store, then
// request it. The swapped target must never be read.
//
// The route is narrow but real. Task containers write directly into the store
// through the hostPath mount, so a symlink can appear under it without passing
// through the daemon at all — which is why validating at REGISTRATION is not
// enough and lookupRegistry's check at USE survives this track.
//
// Scoped per the spec: GET and HEAD on /artifacts/, both /resource-caches/
// routes, and the durable lookup. resolveOne's registry branch is EXCLUDED —
// its src reaches cp -R, which is not a handle, and that window closes in
// slice 8. Naming the exclusion matters: a version of this test covering only
// GET /artifacts/ would pass while the busiest consumer stayed open.
//
// THE SWAPPED TARGET IS A FILE, and that detail is load-bearing. The first
// version of this test swapped in a symlink to a DIRECTORY, and it passed with
// BOTH defences removed — filepath.WalkDir does not descend a symlinked root
// (verified: it visits one entry and stops), so tarDirectory could not have
// leaked the content whatever the daemon did. The test staged an escape the
// code path cannot perform, and would have reported a guard as proven that it
// never exercised. A symlink to a file reaches os.Open, which follows it.
func TestAC2_SwappedAliasTargetIsNeverRead(t *testing.T) {
	const secret = "SECRET-OUTSIDE-THE-STORE"
	const contained = "legitimately-contained-content"

	// setup registers a legitimate alias to a real file inside the store and
	// returns the base URL plus a swap function that replaces that file with a
	// symlink pointing out of the store.
	setup := func(t *testing.T) (base string, swap func()) {
		t.Helper()
		ts, storagePath, server := setupServerWithRegistry(t)

		outside := t.TempDir()
		outsideFile := filepath.Join(outside, "data")
		if err := os.WriteFile(outsideFile, []byte(secret), 0o600); err != nil {
			t.Fatal(err)
		}

		// A real file, registered legitimately: at this moment the alias is
		// contained and would pass any registration-time check.
		target := filepath.Join(storagePath, "caches", "rc-42")
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(contained), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := server.Registry().RegisterAlias("rc-42", target); err != nil {
			t.Fatal(err)
		}

		return ts.URL, func() {
			// What a task container can do through the hostPath mount without
			// the daemon being involved.
			if err := os.Remove(target); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outsideFile, target); err != nil {
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

	// The durable-lookup half of AC2 is in registry_relative_ac_inpkg_test.go.
	// It needs a configured durable tier, whose constructor is unexported, and
	// without one the handler returns 501 before it ever reaches the registry
	// lookup — so a subtest here would have asserted nothing. Which it did:
	// the first version of it passed with every guard removed.
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
