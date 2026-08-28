package main

// The destination must be acquired as a HANDLE, not re-derived from the string
// that was validated.
//
// validateResolveDest resolves symlinks to judge a dest; the copy then opened
// the dest's parent from the raw path again. Between those two moments the
// filesystem is writable by the very containers whose inputs live there —
// every task pod gets <store>/steps/<handle>/<volume> mounted read-write — so
// a component validated as a plain directory can be a symlink by the time the
// copy runs. Check and use were different objects, which is the same defect
// shape as validating a tar entry lexically and letting the kernel resolve it
// differently.
//
// These tests do the swap DETERMINISTICALLY between the two calls, so they
// prove the property without racing.

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
)

// seedForDestSwap builds a store with an artifact to copy and a dest path
// whose second-to-last component is a plain directory that the test will
// replace with a symlink.
func seedForDestSwap(t *testing.T) (*Server, string, string) {
	t.Helper()
	root := t.TempDir()
	s := newServerT(t, lagertest.NewTestLogger("swap"), root, "node")

	src := filepath.Join(root, "steps", "mine", "out")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "f.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The attacker-writable directory: a task container's own input mount.
	swap := filepath.Join(root, "steps", "mine", "vol", "swap")
	if err := os.MkdirAll(swap, 0o755); err != nil {
		t.Fatal(err)
	}
	return s, root, filepath.Join(swap, "victim")
}

func TestCopyArtifact_RefusesADestThatBecameAnEscapingSymlink(t *testing.T) {
	s, root, dest := seedForDestSwap(t)

	// Validation passes while "swap" is a plain directory.
	if err := validateResolveDest(root, dest); err != nil {
		t.Fatalf("a legitimate dest was refused: %v", err)
	}

	// Somewhere outside the store, with content that must survive.
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(filepath.Join(outside, "victim"), 0o755); err != nil {
		t.Fatal(err)
	}
	precious := filepath.Join(outside, "victim", "precious.txt")
	if err := os.WriteFile(precious, []byte("PRECIOUS"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The swap: "swap" is now a symlink pointing out of the store.
	swap := filepath.Join(root, "steps", "mine", "vol", "swap")
	if err := os.RemoveAll(swap); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, swap); err != nil {
		t.Fatal(err)
	}

	if err := s.copyArtifact(context.Background(), "steps/mine/out", dest); err == nil {
		t.Error("the copy followed a symlink out of the storage root")
	}
	if b, err := os.ReadFile(precious); err != nil || string(b) != "PRECIOUS" {
		t.Errorf("content outside the storage root was destroyed: %q err=%v", b, err)
	}
}

func TestCopyArtifact_RefusesADestThatBecameTheStoreRoot(t *testing.T) {
	s, root, dest := seedForDestSwap(t)

	if err := validateResolveDest(root, dest); err != nil {
		t.Fatalf("a legitimate dest was refused: %v", err)
	}

	// A second build's artifact, which a store-root-targeted RemoveAll destroys.
	victim := filepath.Join(root, "steps", "victim-b", "out")
	if err := os.MkdirAll(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(victim, "g.txt"), []byte("victim"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The swap points INSIDE the store, at the root — so containment alone
	// permits it and only the destination's identity refuses it. dest's base is
	// then "victim"... but pointing swap at the root and asking for base
	// "steps" is the whole-store delete, so aim there.
	swap := filepath.Join(root, "steps", "mine", "vol", "swap")
	if err := os.RemoveAll(swap); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, swap); err != nil {
		t.Fatal(err)
	}

	wipe := filepath.Join(swap, "steps")
	if err := s.copyArtifact(context.Background(), "steps/mine/out", wipe); err == nil {
		t.Error("the copy accepted a destination that resolves onto the store's own structure")
	}
	if _, err := os.Stat(filepath.Join(victim, "g.txt")); err != nil {
		t.Errorf("another build's artifact was destroyed: %v", err)
	}
}

// A swap can also redirect the destination onto a STRUCTURAL directory without
// leaving the store — steps/<other-handle> has parent "steps", which is
// contained and is not the storage root, so containment and the root-identity
// check both pass. Its RemoveAll takes another build's entire step tree.
//
// The shape rules that refuse this are applied to the string at the door, so
// they must be applied again to the object actually opened; anything else is
// the same check-then-use gap one level down.
func TestCopyArtifact_RefusesADestSwappedOntoAStructuralDirectory(t *testing.T) {
	for _, structural := range []string{"steps", "caches", "resource-caches"} {
		t.Run(structural, func(t *testing.T) {
			s, root, dest := seedForDestSwap(t)
			if err := validateResolveDest(root, dest); err != nil {
				t.Fatalf("a legitimate dest was refused: %v", err)
			}

			// Another build, whose whole tree the RemoveAll would take.
			victim := filepath.Join(root, structural, "victim-b", "out")
			if err := os.MkdirAll(victim, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(victim, "g.txt"), []byte("victim"), 0o644); err != nil {
				t.Fatal(err)
			}

			swap := filepath.Join(root, "steps", "mine", "vol", "swap")
			if err := os.RemoveAll(swap); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(root, structural), swap); err != nil {
				t.Fatal(err)
			}

			// dest is now <swap>/victim-b, i.e. <structural>/victim-b.
			target := filepath.Join(swap, "victim-b")
			if err := s.copyArtifact(context.Background(), "steps/mine/out", target); err == nil {
				t.Errorf("the copy accepted a destination inside %q, one level under the store root", structural)
			}
			if _, err := os.Stat(filepath.Join(victim, "g.txt")); err != nil {
				t.Errorf("another build's whole tree was destroyed: %v", err)
			}
		})
	}
}

// The peer branch creates the destination's parent before it writes. Doing
// that on the raw path followed a swapped symlink and created root-owned
// directories anywhere on the node — no artifact data, but a write outside the
// store all the same, and the daemon holds CAP_DAC_OVERRIDE.
func TestFetchFromPeer_CreatesNoDirectoryOutsideTheStore(t *testing.T) {
	s, root, dest := seedForDestSwap(t)
	if err := validateResolveDest(root, dest); err != nil {
		t.Fatalf("a legitimate dest was refused: %v", err)
	}

	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}

	swap := filepath.Join(root, "steps", "mine", "vol", "swap")
	if err := os.RemoveAll(swap); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, swap); err != nil {
		t.Fatal(err)
	}

	// Two levels below the swapped component, so a raw MkdirAll would have to
	// create <outside>/a to reach it.
	if err := s.fetchFromPeer(context.Background(), "10.0.0.1", "steps/mine/out", filepath.Join(swap, "a", "b")); err == nil {
		t.Error("the peer fetch accepted a destination outside the storage root")
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("directories were created outside the storage root: %v", entries)
	}
}

// No component of a destination may be a symlink AT ALL.
//
// This is the rule the previous three rounds were each a special case of. A
// destination is a name the caller was authorized to write — the capability
// signs that exact string — so if the daemon resolves the string through links
// the caller controls, authorization and effect describe different objects and
// every other guard is reasoning about the wrong place. Planting
// steps/ATTACKER/vol as a link to steps/VICTIM/vol turns a capability for one's
// own volume into a clear-and-replace of someone else's, and no rule about
// shape or about the parent's identity can see it, because by then the parent
// legitimately IS steps/VICTIM.
//
// Refusing links costs nothing real: a destination is a hostPath mount the
// kubelet created, never a link.
func TestOpenDestParent_RefusesASymlinkedComponent(t *testing.T) {
	victimFile := func(t *testing.T, root string) string {
		t.Helper()
		vol := filepath.Join(root, "steps", "VICTIM", "vol")
		if err := os.MkdirAll(vol, 0o755); err != nil {
			t.Fatal(err)
		}
		f := filepath.Join(vol, "important.txt")
		if err := os.WriteFile(f, []byte("IMPORTANT"), 0o644); err != nil {
			t.Fatal(err)
		}
		return f
	}

	t.Run("the final component is a link to another build's volume", func(t *testing.T) {
		s, root, _ := seedForDestSwap(t)
		important := victimFile(t, root)

		attacker := filepath.Join(root, "steps", "ATTACKER")
		if err := os.MkdirAll(attacker, 0o755); err != nil {
			t.Fatal(err)
		}
		dest := filepath.Join(attacker, "vol")
		if err := os.Symlink(filepath.Join(root, "steps", "VICTIM", "vol"), dest); err != nil {
			t.Fatal(err)
		}

		if err := s.copyArtifact(context.Background(), "steps/mine/out", dest); err == nil {
			t.Error("a destination that is a symlink onto another build's volume was accepted")
		}
		if b, err := os.ReadFile(important); err != nil || string(b) != "IMPORTANT" {
			t.Errorf("another build's volume was destroyed through a symlinked destination: %q err=%v", b, err)
		}
	})

	t.Run("a middle component is a link to another build", func(t *testing.T) {
		s, root, _ := seedForDestSwap(t)
		important := victimFile(t, root)

		attacker := filepath.Join(root, "steps", "ATTACKER")
		if err := os.MkdirAll(attacker, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(root, "steps", "VICTIM"), filepath.Join(attacker, "hop")); err != nil {
			t.Fatal(err)
		}

		dest := filepath.Join(attacker, "hop", "vol")
		if err := s.copyArtifact(context.Background(), "steps/mine/out", dest); err == nil {
			t.Error("a destination reached through a symlinked component was accepted")
		}
		if b, err := os.ReadFile(important); err != nil || string(b) != "IMPORTANT" {
			t.Errorf("another build's volume was destroyed through a symlinked component: %q err=%v", b, err)
		}
	})
}

// The peer branch has to CREATE the destination's parent before it can open
// it, and creating is a write like any other: it must follow the authorized
// name, refuse symlinks, and leave nothing behind when the request is refused.
func TestFetchFromPeer_ParentCreationFollowsTheAuthorizedName(t *testing.T) {
	t.Run("a symlink inside the caller's own volume cannot steer the mkdir", func(t *testing.T) {
		s, root, _ := seedForDestSwap(t)

		// The attacker owns steps/mine/vol and plants a link out of it.
		link := filepath.Join(root, "steps", "mine", "vol", "L")
		if err := os.Symlink(filepath.Join("..", "..", "..", "caches"), link); err != nil {
			t.Fatal(err)
		}

		dest := filepath.Join(link, "poisoned-cache", "x")
		if err := s.fetchFromPeer(context.Background(), "10.0.0.1", "steps/mine/out", dest); err == nil {
			t.Error("a destination through a symlinked component was accepted")
		}
		// A refused request must have no side effects.
		if _, err := os.Stat(filepath.Join(root, "caches", "poisoned-cache")); err == nil {
			t.Error("a refused request created a directory under caches/")
		}
	})

	t.Run("a structural FILE cannot be created as a directory", func(t *testing.T) {
		// aliases.json is a file the daemon persists its alias store into. Three
		// segments satisfies the depth rule, so only knowing that this name is
		// a file refuses it — and if the mkdir wins, the alias store can never
		// be written again.
		s, root, _ := seedForDestSwap(t)

		dest := filepath.Join(root, "aliases.json", "a", "b")
		if err := s.fetchFromPeer(context.Background(), "10.0.0.1", "steps/mine/out", dest); err == nil {
			t.Error("a destination under aliases.json was accepted")
		}
		if info, err := os.Stat(filepath.Join(root, "aliases.json")); err == nil && info.IsDir() {
			t.Error("aliases.json was created as a directory; the alias store can never persist again")
		}
	})

	t.Run("the legitimate shape still creates its parent", func(t *testing.T) {
		// The peer path exists to deliver an artifact for a handle this node
		// has never seen, so creating steps/<handle> is its normal job. Driven
		// through the creation step directly rather than a whole fetch: the
		// fetch itself would spend three retries failing to reach a peer.
		s, root, _ := seedForDestSwap(t)

		dest := filepath.Join(root, "steps", "fresh-handle", "input-0")
		if err := validateResolveDest(root, dest); err != nil {
			t.Fatalf("the legitimate shape was refused by the destination rules: %v", err)
		}
		rel, err := s.lexicalDestRel(dest)
		if err != nil {
			t.Fatal(err)
		}
		created, _, err := s.createDestParent(filepath.Dir(osName(rel)))
		if err != nil {
			t.Fatalf("the peer path could not create its destination parent: %v", err)
		}
		created.Close()

		info, err := os.Stat(filepath.Join(root, "steps", "fresh-handle"))
		if err != nil {
			t.Fatalf("the peer path did not create its destination parent: %v", err)
		}
		if !info.IsDir() {
			t.Error("the destination parent is not a directory")
		}
		// And the acquisition that follows it succeeds.
		parent, base, err := s.openDestParent(dest)
		if err != nil {
			t.Fatalf("the created parent could not then be acquired: %v", err)
		}
		defer parent.Close()
		if base != "input-0" {
			t.Errorf("base = %q, want input-0", base)
		}
	})
}

// The door and the operation must judge the SAME relative path.
//
// While the door resolved symlinks and the operation did not, a symlink
// expanding deeper than its own location made a following ".." run land in
// different places under the two computations: the door approved a harmless
// six-segment path while the daemon created the two-segment one the attacker
// actually wanted. No individual rule was wrong; there were simply two answers
// to "where does this destination go".
func TestDestRules_DoorAndOperationAgreeOnTheSamePath(t *testing.T) {
	s, root, _ := seedForDestSwap(t)

	// A link that expands to five levels, so "../../../../" applied after it
	// lands somewhere the lexical form never reaches.
	deep := filepath.Join(root, "steps", "mine", "vol", "d1", "d2", "d3", "d4", "d5")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("d1", "d2", "d3", "d4", "d5"), filepath.Join(root, "steps", "mine", "vol", "L")); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"aliases.json", "aliases.json.tmp"} {
		t.Run(name, func(t *testing.T) {
			// Built by RAW concatenation: filepath.Join would Clean the ".."
			// run away and the symlink would never be on the path, which is
			// precisely the divergence under test. A caller sends whatever
			// bytes it likes.
			dest := root + "/steps/mine/vol/L/../../../../" + name + "/x"

			if err := validateResolveDest(root, dest); err == nil {
				t.Error("the door accepted a destination the operation would place under a structural file")
			}
			if err := s.fetchFromPeer(context.Background(), "10.0.0.1", "steps/mine/out", dest); err == nil {
				t.Error("the peer path accepted it")
			}
			if info, err := os.Lstat(filepath.Join(root, name)); err == nil {
				t.Errorf("%s was created (IsDir=%v); the alias store can never be written again", name, info.IsDir())
			}
		})
	}
}

// A refused request must leave the filesystem exactly as it found it. Creating
// the destination's parent one component at a time means a failure at the last
// component had already made the earlier ones, so any attacker-chosen name
// became a permanent root-owned directory in the store — repeatable without
// bound, from a request the daemon reports as refused.
func TestFetchFromPeer_RefusalLeavesNoPartialParent(t *testing.T) {
	long := strings.Repeat("L", 300)

	for _, tc := range []struct{ name, dest, leftover string }{
		{"over-long component", filepath.Join("steps", "attacker-chosen", long, "x"), filepath.Join("steps", "attacker-chosen")},
		{"new top-level tree", filepath.Join("evil-top", "evil-second", long, "x"), "evil-top"},
		{"NUL in a component", filepath.Join("steps", "nul-handle", "a\x00b", "x"), filepath.Join("steps", "nul-handle")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, root, _ := seedForDestSwap(t)

			dest := filepath.Join(root, tc.dest)
			if err := validateResolveDest(root, dest); err == nil {
				t.Error("the door accepted a destination with an unusable component")
			}
			if err := s.fetchFromPeer(context.Background(), "10.0.0.1", "steps/mine/out", dest); err == nil {
				t.Error("the peer path accepted it")
			}
			if _, err := os.Stat(filepath.Join(root, tc.leftover)); err == nil {
				t.Errorf("a refused request left %q behind", tc.leftover)
			}
		})
	}

	// The door now refuses these before any directory is made, so the unwind
	// below it is defence in depth rather than the thing that saves us here.
	// Exercised directly, because that is the only way to reach it: a walk that
	// creates one component and then fails on the next.
	t.Run("the unwind itself", func(t *testing.T) {
		s, root, _ := seedForDestSwap(t)

		if _, _, err := s.createDestParent(path.Join("steps", "half-made", long)); err == nil {
			t.Fatal("a walk with an unusable component reported success")
		}
		if _, err := os.Stat(filepath.Join(root, "steps", "half-made")); err == nil {
			t.Error("the walk left the component it created before failing")
		}
	})

	// And it must not remove anything it did NOT create.
	t.Run("the unwind spares what it found", func(t *testing.T) {
		s, root, _ := seedForDestSwap(t)

		existing := filepath.Join(root, "steps", "already-here")
		if err := os.MkdirAll(existing, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(existing, "keep.txt"), []byte("keep"), 0o644); err != nil {
			t.Fatal(err)
		}

		if _, _, err := s.createDestParent(path.Join("steps", "already-here", long)); err == nil {
			t.Fatal("a walk with an unusable component reported success")
		}
		if _, err := os.Stat(filepath.Join(existing, "keep.txt")); err != nil {
			t.Errorf("the unwind removed a directory it did not create: %v", err)
		}
	})
}

// The parent creation must be undone when the work it was for fails, not only
// when the walk itself fails. A peer that never answers is the ordinary case,
// and it left an attacker-named root-owned directory in the store every time —
// reclaimed by nothing, since the sweeper only walks steps/ and artifacts/.
func TestFetchFromPeer_FailedFetchLeavesNoCreatedDirectories(t *testing.T) {
	s, root, _ := seedForDestSwap(t)
	s.SetPeerResolver(NewPeerResolver(lagertest.NewTestLogger("peer"), nil, "", "", 1, "", nil))

	before, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}

	// 127.0.0.1:1 refuses immediately, so all three attempts fail fast.
	for i := 0; i < 3; i++ {
		dest := filepath.Join(root, "attacker-"+itoa(i), "x")
		if err := s.fetchFromPeer(context.Background(), "127.0.0.1", "steps/mine/out", dest); err == nil {
			t.Fatalf("fetch %d reported success against an unreachable peer", i)
		}
		if _, err := os.Stat(filepath.Join(root, "attacker-"+itoa(i))); err == nil {
			t.Errorf("a failed fetch left %q in the store", "attacker-"+itoa(i))
		}
	}

	after, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("the store root grew from %d entries to %d across three failed fetches", len(before), len(after))
	}
}

// The destination lock must key on the destination's IDENTITY. lexicalRel
// accepts the storage root under either its raw or its resolved spelling, so
// two dest strings can name one directory; keying the lock on the string let
// both run at once, on the very path peers.Fetch cites this lock to justify
// treating a failed clear-and-rename as a hard error.
func TestAcquireDest_KeysOnIdentityNotSpelling(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "store-link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	// The server's storagePath is the SYMLINK, so resolvePath differs from it.
	s := newServerT(t, lagertest.NewTestLogger("spelling"), link, "node")
	if resolvePath(link) == filepath.Clean(link) {
		t.Skip("this platform did not give the root two spellings")
	}

	viaLink := filepath.Join(link, "steps", "h", "v")
	viaReal := filepath.Join(resolvePath(link), "steps", "h", "v")

	release, err := s.acquireDest(context.Background(), viaLink)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if leaked, err := s.acquireDest(ctx, viaReal); err == nil {
		// Release it before failing: holding it would deadlock the re-acquire
		// below, turning a clear assertion into a ten-minute hang.
		leaked()
		t.Fatal("a second spelling of the same destination acquired the lock while the first held it")
	}

	release()
	again, err := s.acquireDest(context.Background(), viaReal)
	if err != nil {
		t.Fatalf("the other spelling could not acquire after release: %v", err)
	}
	again()
}

// The zero case: the same shape with no swap must still work, or the tests
// above are satisfied by a daemon that refuses everything.
func TestCopyArtifact_StillCopiesThroughARealDirectory(t *testing.T) {
	s, root, dest := seedForDestSwap(t)

	if err := validateResolveDest(root, dest); err != nil {
		t.Fatalf("a legitimate dest was refused: %v", err)
	}
	if err := s.copyArtifact(context.Background(), "steps/mine/out", dest); err != nil {
		t.Fatalf("copy through a real directory chain failed: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dest, "f.txt")); err != nil || string(b) != "mine" {
		t.Errorf("artifact not delivered: %q err=%v", b, err)
	}
	_ = root
}
