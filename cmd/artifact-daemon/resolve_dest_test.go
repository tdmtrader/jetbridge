package main_test

// A resolve destination is REMOVED before it is written: copyArtifact and the
// peer promote both clear dest and rename over it. So the dest is not merely a
// place to put bytes — naming one is asking the daemon to delete whatever is
// already there, and /resolve and /resolve-batch are the two mTLS-exempt
// routes.
//
// Containment alone does not make that safe. "<storage>/steps" is perfectly
// contained, and resolving into it deletes every artifact on the node and
// answers 200. The capability binds a caller to one dest and closes this, but
// capability enforcement is configuration (the daemon runs open when no key is
// given), and a control that only holds when it is switched on is not the
// place to put the whole-store delete. These assertions must hold with NO
// capability configured, which is how setupServer runs.

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func postJSON(t *testing.T, url, body string) int {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// seedTwoArtifacts puts two unrelated artifacts on the node and returns the
// storage path.
func seedTwoArtifacts(t *testing.T, storagePath string) {
	t.Helper()
	for _, name := range []string{"victim-a/out", "victim-b/out"} {
		dir := filepath.Join(storagePath, "steps", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("payload"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestResolve_RefusesAStructuralDestination(t *testing.T) {
	// Each of these names part of the store's SHAPE. Resolving into one
	// deletes everything beneath it.
	for _, dest := range []string{"steps", "caches", "resource-caches", "artifacts", "STEPS"} {
		t.Run(dest, func(t *testing.T) {
			ts, storagePath := setupServer(t)
			seedTwoArtifacts(t, storagePath)

			body := `{"key":"victim-a/out","dest":"` + filepath.Join(storagePath, dest) + `"}`
			if got := postJSON(t, ts.URL+"/resolve", body); got < 400 {
				t.Errorf("resolve into %q -> %d, want 4xx", dest, got)
			}

			// The other artifact must still be there whatever the status was.
			if _, err := os.Stat(filepath.Join(storagePath, "steps", "victim-b", "out", "f.txt")); err != nil {
				t.Errorf("an unrelated artifact was destroyed by a resolve into %q: %v", dest, err)
			}
		})
	}
}

// One segment under the storage root is never a real destination: the ATC
// resolves into steps/<handle>/<name>, three deep. A single segment is either
// a structural directory or a new top-level one, and neither is an input mount.
func TestResolve_RefusesATopLevelDestination(t *testing.T) {
	ts, storagePath := setupServer(t)
	seedTwoArtifacts(t, storagePath)

	body := `{"key":"victim-a/out","dest":"` + filepath.Join(storagePath, "anything") + `"}`
	if got := postJSON(t, ts.URL+"/resolve", body); got < 400 {
		t.Errorf("resolve into a top-level destination -> %d, want 4xx", got)
	}
}

// The batch route validates every item before starting any, so a structural
// dest anywhere in the batch must refuse the WHOLE request with nothing
// copied and nothing deleted.
func TestResolveBatch_RefusesAStructuralDestination(t *testing.T) {
	ts, storagePath := setupServer(t)
	seedTwoArtifacts(t, storagePath)

	good := destUnder(t, storagePath, "ok-dest")
	body, _ := json.Marshal(batchRequest{Items: []batchItem{
		{Key: "victim-a/out", Dest: good},
		{Key: "victim-a/out", Dest: filepath.Join(storagePath, "steps")},
	}})
	if got := postJSON(t, ts.URL+"/resolve-batch", string(body)); got < 400 {
		t.Errorf("batch with a structural dest -> %d, want 4xx", got)
	}
	if _, err := os.Stat(filepath.Join(storagePath, "steps", "victim-b", "out", "f.txt")); err != nil {
		t.Errorf("an unrelated artifact was destroyed by a refused batch: %v", err)
	}
	if _, err := os.Stat(good); err == nil {
		t.Error("a refused batch copied item 0 anyway; refusal must have no side effects")
	}
}

// A build's own directory under a structural tree is not a destination
// either. steps/<handle> is where a build's volumes live, so resolving onto it
// deletes every volume that build produced and puts an attacker-chosen
// artifact in their place — the victim's recorded outputs become someone
// else's content, and that propagates to every downstream step. A real
// destination names a volume INSIDE a handle: steps/<handle>/<volume>.
func TestResolve_RefusesAnotherBuildsHandleDirectory(t *testing.T) {
	ts, storagePath := setupServer(t)
	seedTwoArtifacts(t, storagePath)

	// victim-b has two volumes; both must survive.
	other := filepath.Join(storagePath, "steps", "victim-b", "other-vol")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "g.txt"), []byte("second"), 0o644); err != nil {
		t.Fatal(err)
	}

	body := `{"key":"victim-a/out","dest":"` + filepath.Join(storagePath, "steps", "victim-b") + `"}`
	if got := postJSON(t, ts.URL+"/resolve", body); got < 400 {
		t.Errorf("resolve onto another build's handle directory -> %d, want 4xx", got)
	}
	for _, f := range []string{
		filepath.Join(storagePath, "steps", "victim-b", "out", "f.txt"),
		filepath.Join(other, "g.txt"),
	} {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("another build's volume was destroyed: %s: %v", f, err)
		}
	}
}

// The zero case. Without it every assertion above is satisfied by a daemon
// that refuses every resolve.
func TestResolve_StillAcceptsARealDestination(t *testing.T) {
	ts, storagePath := setupServer(t)
	seedTwoArtifacts(t, storagePath)

	// The shape the ATC actually sends: steps/<handle>/<volume name>. The
	// parent exists because the volume is a HostPathDirectoryOrCreate mount.
	if err := os.MkdirAll(filepath.Join(storagePath, "steps", "consumer-handle"), 0o755); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(storagePath, "steps", "consumer-handle", "input-0")
	body := `{"key":"victim-a/out","dest":"` + dest + `"}`
	if got := postJSON(t, ts.URL+"/resolve", body); got != http.StatusOK {
		t.Fatalf("resolve into a real input destination -> %d, want 200", got)
	}
	if _, err := os.Stat(filepath.Join(dest, "f.txt")); err != nil {
		t.Errorf("the artifact was not delivered: %v", err)
	}
}
