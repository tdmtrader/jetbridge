package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"
)

// These cover one defect shape in two places: a validator computes a canonical
// form and the caller uses the original.

// resolvePath must resolve symlinks in the kernel's order, component by
// component. Cleaning the input first makes "link/.." vanish textually, so the
// validator reports contained for a path the kernel lands outside.
func TestResolvePath_DoesNotCollapseDotDotAcrossASymlink(t *testing.T) {
	base := t.TempDir()
	store := filepath.Join(base, "store")
	outside := filepath.Join(base, "outside")
	mustMkdir(t, filepath.Join(store, "steps", "h"))
	mustMkdir(t, filepath.Join(outside, "deep"))
	mustMkdir(t, filepath.Join(store, "caches", "real"))
	if err := os.WriteFile(filepath.Join(outside, "victim.txt"), []byte("OUTSIDE"), 0o644); err != nil {
		t.Fatal(err)
	}
	// What a task container can plant through the hostPath mount.
	if err := os.Symlink(filepath.Join(outside, "deep"), filepath.Join(store, "steps", "h", "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(store, "caches", "real"), filepath.Join(store, "steps", "h", "inlink")); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name    string
		path    string
		refused bool
		rel     RelKey
	}{
		// NOT filepath.Join — Join cleans, and a JSON request body does not.
		{"escape via link/..", store + "/steps/h/link/../victim.txt", true, ""},
		{"escape, deeper", store + "/steps/h/link/../../outside/victim.txt", true, ""},
		{"the root itself", store, true, ""},
		{"a sibling of the root", outside, true, ""},

		// Zero cases. Without these the rule is satisfiable by refusing
		// everything, and the /var -> /private/var class is exactly how an
		// earlier version of this validator broke real builds.
		{"plain contained", filepath.Join(store, "steps", "h", "out"), false, "steps/h/out"},
		{"deep non-existent dest", filepath.Join(store, "steps", "h", "no", "such", "x"), false, "steps/h/no/such/x"},
		{"contained .. ", store + "/steps/h/../h/out", false, "steps/h/out"},
		{"trailing slash", store + "/steps/h/", false, "steps/h"},
		{"double separator", store + "/steps//h", false, "steps/h"},
		{"dot segment", store + "/steps/./h", false, "steps/h"},
		// A symlink pointing INSIDE the store resolves to its canonical
		// target. That is deliberate: the guard key then agrees with the
		// sweeper's view of the real tree rather than with an alias of it.
		{"symlink into the store", filepath.Join(store, "steps", "h", "inlink", "f.txt"), false, "caches/real/f.txt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := containedRelKey(store, tc.path)
			if tc.refused {
				if err == nil {
					t.Fatalf("accepted %q as %q", tc.path, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("refused a contained path %q: %v", tc.path, err)
			}
			if got != tc.rel {
				t.Errorf("got %q, want %q", got, tc.rel)
			}
		})
	}
}

// The escape is only interesting if the kernel really does land outside. Proven
// rather than asserted, so this test cannot rot into a tautology about Clean.
func TestResolvePath_TheKernelReallyLandsOutside(t *testing.T) {
	base := t.TempDir()
	store := filepath.Join(base, "store")
	outside := filepath.Join(base, "outside")
	mustMkdir(t, filepath.Join(store, "steps", "h"))
	mustMkdir(t, outside)
	if err := os.WriteFile(filepath.Join(outside, "victim.txt"), []byte("OUTSIDE"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(store, "steps", "h", "link")); err != nil {
		t.Fatal(err)
	}

	raw := store + "/steps/h/link/../outside/victim.txt"
	if b, err := os.ReadFile(raw); err != nil || string(b) != "OUTSIDE" {
		t.Skipf("this platform does not resolve link/.. outward (%v); the escape is moot here", err)
	}
	if _, err := containedRelKey(store, raw); err == nil {
		t.Fatal("the validator accepted a path the kernel resolves outside the root")
	}
}

// A request key must BE canonical, not merely clean to something canonical:
// two handlers build a RelKey from the raw key by hand, and stepHandle splits
// a RelKey on "/".
func TestValidateRequestKey_RefusesNonCanonical(t *testing.T) {
	for _, tc := range []struct {
		key     string
		refused bool
	}{
		{"steps/build-42/out", false},
		{"build-42", false},
		{"a/b/c/d", false},

		// Every one of these routed and served 200 before, and every one
		// derived a guard key the sweeper could never match.
		{"steps//build-42/out", true},
		{"steps/./build-42/out", true},
		{"steps/build-42/out/", true},
		{"./steps/build-42/out", true},
		{"steps/build-42//out", true},
	} {
		t.Run(tc.key, func(t *testing.T) {
			err := validateRequestKey(tc.key)
			if tc.refused && err == nil {
				t.Errorf("accepted the non-canonical key %q", tc.key)
			}
			if !tc.refused && err != nil {
				t.Errorf("refused the canonical key %q: %v", tc.key, err)
			}
		})
	}
}

// For every key the validator admits, the reader's guard key must equal the
// sweeper's.
//
// The sibling assertion in registry_relative_test.go derives the reader key
// from a Lookup value, which is always canonical because Register goes through
// filepath.Join — so it never reaches the two sites that build a RelKey by
// hand. The type makes a stale READ a compile error, never a stale
// CONSTRUCTION.
func TestGuardKey_EveryAdmittedKeyAgreesWithTheSweeper(t *testing.T) {
	s := newServerT(t, lagertest.NewTestLogger("canonical"), t.TempDir(), "node")

	for _, spelling := range []string{
		"steps/build-42/out",
		"steps//build-42/out",
		"steps/./build-42/out",
		"steps/build-42//out",
		"steps/build-42/out/",
	} {
		if err := validateRequestKey(spelling); err != nil {
			continue // refused at the door; it can never reach stepHandle
		}
		// The sweeper's key for this tree, derived the way sweeper.go does.
		const sweeperKey = "build-42"
		if got := s.stepHandle(RelKey(spelling)); got != sweeperKey {
			t.Errorf("key %q is admitted but derives guard key %q; the sweeper uses %q — "+
				"a lock that excludes nobody, and the sweeper is free to remove the tree mid-read",
				spelling, got, sweeperKey)
		}
	}
}

// Non-steps locations must not collide with a bare step handle.
func TestStepHandle_NonStepsKeysAreNamespaced(t *testing.T) {
	s := newServerT(t, lagertest.NewTestLogger("canonical"), t.TempDir(), "node")

	step := s.stepHandle(RelKey("steps/build-42/out"))
	cache := s.stepHandle(RelKey("build-42"))
	if step == cache {
		t.Errorf("a cache entry and step %q share the guard key %q — unrelated work serialises", step, step)
	}
	if !strings.HasPrefix(cache, "loc:") {
		t.Errorf("expected a namespaced key, got %q", cache)
	}
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}
