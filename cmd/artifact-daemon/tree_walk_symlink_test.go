package main

// copyTree and tarTree must never traverse a directory through a symlink.
//
// The source tree is writable by the step pod that produced it, so an entry
// that is a real directory when its parent is listed can be a symlink by the
// time the walk opens it. These tests make that change DETERMINISTICALLY at a
// point the walk itself reaches — the tar writer receiving an earlier file's
// bytes, or the per-entry context check — rather than racing it.

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const victimSecret = "VICTIM-SECRET-CONTENT"

// seedSwapTree builds a store holding another step's output and a source tree
// whose entry "d" is, for now, a real directory. It returns the store and a
// swap that replaces "d" with a symlink to target.
func seedSwapTree(t *testing.T, target string) (store string, swap func()) {
	t.Helper()
	store = t.TempDir()
	victim := filepath.Join(store, "steps", "victim", "out")
	src := filepath.Join(store, "steps", "attacker", "out")
	for _, dir := range []string{victim, filepath.Join(src, "d"), filepath.Join(src, "e")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		filepath.Join(victim, "secret.txt"): victimSecret,
		filepath.Join(src, "a.txt"):         "TRIGGER",
		filepath.Join(src, "d", "mine.txt"): "mine",
		filepath.Join(src, "e", "e-file"):   "e-content",
	}
	for p, content := range files {
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	swapped := false
	return store, func() {
		if swapped {
			return
		}
		swapped = true
		if err := os.Rename(filepath.Join(src, "d"), filepath.Join(store, "steps", "attacker", "stash")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(src, "d")); err != nil {
			t.Fatal(err)
		}
	}
}

// triggerWriter runs fire once, the first time the stream carries marker.
type triggerWriter struct {
	buf    bytes.Buffer
	marker []byte
	fire   func()
	fired  bool
}

func (w *triggerWriter) Write(p []byte) (int, error) {
	n, err := w.buf.Write(p)
	if !w.fired && bytes.Contains(w.buf.Bytes(), w.marker) {
		w.fired = true
		w.fire()
	}
	return n, err
}

// triggerCtx runs fire on its at-th Err call. copyTree checks the context once
// per entry, so the 2nd call is the first entry ("a.txt"), after the source
// directory has been listed and before "d" is visited.
type triggerCtx struct {
	context.Context
	calls, at int
	fire      func()
}

func (c *triggerCtx) Err() error {
	c.calls++
	if c.calls == c.at {
		c.fire()
	}
	return c.Context.Err()
}

func openStore(t *testing.T, store string) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return root
}

// filesContaining lists the regular files under dir, found without following
// any link, whose content includes needle.
func filesContaining(t *testing.T, dir, needle string) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
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

func tarNames(t *testing.T, archive []byte) map[string]*tar.Header {
	t.Helper()
	out := map[string]*tar.Header{}
	tr := tar.NewReader(bytes.NewReader(archive))
	for {
		hdr, err := tr.Next()
		if err != nil {
			// A refused walk leaves an unterminated stream; what was emitted
			// before the refusal is all that matters here.
			return out
		}
		out[hdr.Name] = hdr
	}
}

var swapTargets = []struct {
	name   string
	target string
	// leak is what the output must never contain if "d" was traversed.
	leak string
}{
	// Another step's output elsewhere in the node-wide store.
	{"to another step's output", "../../victim/out", "secret.txt"},
	// A sibling inside the same tree: in-root and in-tree, so only refusing to
	// traverse the link keeps its contents from appearing under "d/".
	{"to a sibling in the same tree", "e", "e-file"},
}

func TestTarTree_DoesNotTraverseADirectoryReplacedByASymlink(t *testing.T) {
	for _, tc := range swapTargets {
		t.Run(tc.name, func(t *testing.T) {
			store, swap := seedSwapTree(t, tc.target)
			root := openStore(t, store)

			w := &triggerWriter{marker: []byte("TRIGGER"), fire: swap}
			err := tarTree(w, root, "steps/attacker/out")
			if !w.fired {
				t.Fatal("the swap never ran — this test proves nothing")
			}

			if bytes.Contains(w.buf.Bytes(), []byte(victimSecret)) {
				t.Fatal("tarTree emitted another step's file content")
			}
			names := tarNames(t, w.buf.Bytes())
			for name := range names {
				if strings.HasPrefix(name, "d/") {
					t.Errorf("tarTree traversed the symlink at d: emitted %q", name)
				}
			}
			if _, ok := names["d/"+tc.leak]; ok {
				t.Errorf("tarTree emitted d/%s through the symlink", tc.leak)
			}

			// What happens to "d" itself: an in-tree link is carried as the
			// link it now is; an escaping one is refused, as it always was.
			if tc.target == "e" {
				if err != nil {
					t.Fatalf("tarTree: %v", err)
				}
				hdr, ok := names["d"]
				if !ok || hdr.Typeflag != tar.TypeSymlink || hdr.Linkname != "e" {
					t.Errorf("want d carried as a symlink to e, got %+v", hdr)
				}
			} else if !errors.Is(err, ErrRefused) {
				t.Errorf("want the escaping link refused, got err=%v", err)
			}
		})
	}
}

func TestCopyTree_DoesNotTraverseADirectoryReplacedByASymlink(t *testing.T) {
	for _, tc := range swapTargets {
		t.Run(tc.name, func(t *testing.T) {
			store, swap := seedSwapTree(t, tc.target)
			root := openStore(t, store)
			dst := t.TempDir()
			dstRoot := openStore(t, dst)

			ctx := &triggerCtx{Context: context.Background(), at: 2, fire: swap}
			err := copyTree(ctx, root, "steps/attacker/out", dstRoot)
			if ctx.calls < 2 {
				t.Fatal("the swap never ran — this test proves nothing")
			}

			// Lstat, not Stat: in the in-tree case dst/d is legitimately a
			// link to dst/e, and following it would find e's own copy.
			if info, lstatErr := os.Lstat(filepath.Join(dst, "d")); lstatErr == nil && info.IsDir() {
				t.Fatalf("copyTree created d as a directory from a symlink")
			}
			if leaked := filesContaining(t, dst, victimSecret); len(leaked) > 0 {
				t.Fatalf("copyTree copied another step's file content into %v", leaked)
			}

			if tc.target == "e" {
				if err != nil {
					t.Fatalf("copyTree: %v", err)
				}
				link, readErr := os.Readlink(filepath.Join(dst, "d"))
				if readErr != nil || link != "e" {
					t.Errorf("want d copied as a symlink to e, got %q (%v)", link, readErr)
				}
			} else if !errors.Is(err, ErrRefused) {
				t.Errorf("want the escaping link refused, got err=%v", err)
			}
		})
	}
}

// The location a walk starts from is descended the same way: a symlink at any
// component of it is refused, not followed into whatever it names.
func TestTreeWalk_RefusesASymlinkInTheStartingLocation(t *testing.T) {
	store := t.TempDir()
	victim := filepath.Join(store, "steps", "victim", "out")
	if err := os.MkdirAll(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(victim, "secret.txt"), []byte(victimSecret), 0o644); err != nil {
		t.Fatal(err)
	}
	attacker := filepath.Join(store, "steps", "attacker")
	if err := os.MkdirAll(filepath.Join(attacker, "out"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../victim/out", filepath.Join(attacker, "out", "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../victim", filepath.Join(attacker, "hop")); err != nil {
		t.Fatal(err)
	}

	for _, loc := range []string{
		"steps/attacker/out/link", // the final component
		"steps/attacker/hop/out",  // an intermediate component
	} {
		t.Run(loc, func(t *testing.T) {
			root := openStore(t, store)

			var archive bytes.Buffer
			tarErr := tarTree(&archive, root, RelKey(loc))
			if tarErr == nil {
				t.Error("tarTree walked a location through a symlink")
			}
			if bytes.Contains(archive.Bytes(), []byte(victimSecret)) {
				t.Error("tarTree emitted another step's file content")
			}

			dst := t.TempDir()
			copyErr := copyTree(context.Background(), root, loc, openStore(t, dst))
			if copyErr == nil {
				t.Error("copyTree walked a location through a symlink")
			}
			if _, err := os.Lstat(filepath.Join(dst, "secret.txt")); err == nil {
				t.Error("copyTree copied another step's file")
			}
		})
	}
}

// A location that names a regular file is an empty tree, as it always was:
// the walk never lists anything below a non-directory.
func TestTreeWalk_ALocationThatIsAFileIsEmpty(t *testing.T) {
	store := t.TempDir()
	if err := os.MkdirAll(filepath.Join(store, "steps", "h"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store, "steps", "h", "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := openStore(t, store)

	var archive bytes.Buffer
	if err := tarTree(&archive, root, "steps/h/f"); err != nil {
		t.Fatalf("tarTree: %v", err)
	}
	if _, err := tar.NewReader(&archive).Next(); err != io.EOF {
		t.Errorf("want an empty archive, got err=%v", err)
	}

	dst := t.TempDir()
	if err := copyTree(context.Background(), root, "steps/h/f", openStore(t, dst)); err != nil {
		t.Fatalf("copyTree: %v", err)
	}
	if entries, _ := os.ReadDir(dst); len(entries) != 0 {
		t.Errorf("want an empty copy, got %v", entries)
	}
}

// The window between the Lstat that classifies an entry and the open that
// reads it. The swap happens between the two calls, and the link stays inside
// the parent so os.Root follows it: only the identity check refuses it.
func TestOpenVerified_RefusesAnEntrySwappedAfterItsLstat(t *testing.T) {
	setup := func(t *testing.T) (string, *os.Root) {
		t.Helper()
		dir := t.TempDir()
		for _, sub := range []string{"real", "other"} {
			if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		for name, content := range map[string]string{"f": "mine", "other-file": victimSecret} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return dir, openStore(t, dir)
	}
	swapFor := func(t *testing.T, dir, name, target string) {
		t.Helper()
		if err := os.Rename(filepath.Join(dir, name), filepath.Join(dir, name+".moved")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("a directory", func(t *testing.T) {
		dir, root := setup(t)
		info, err := root.Lstat("real")
		if err != nil {
			t.Fatal(err)
		}
		// Zero case: unchanged, it opens.
		opened, err := openVerifiedDir(root, "real", info)
		if err != nil {
			t.Fatalf("an unchanged directory was refused: %v", err)
		}
		opened.Close()

		swapFor(t, dir, "real", "other")
		if opened, err := openVerifiedDir(root, "real", info); err == nil {
			opened.Close()
			t.Fatal("opened a directory that had become a symlink after its Lstat")
		}
	})

	t.Run("a regular file", func(t *testing.T) {
		dir, root := setup(t)
		info, err := root.Lstat("f")
		if err != nil {
			t.Fatal(err)
		}
		opened, err := openVerifiedFile(root, "f", info)
		if err != nil {
			t.Fatalf("an unchanged file was refused: %v", err)
		}
		opened.Close()

		swapFor(t, dir, "f", "other-file")
		if opened, err := openVerifiedFile(root, "f", info); err == nil {
			opened.Close()
			t.Fatal("opened a file that had become a symlink after its Lstat")
		}
	})
}

// A fifo swapped in at a regular file's name must not block the open, which
// would hold the resolve slot until some writer appeared. It is opened at once
// and refused by the identity check.
func TestOpenVerifiedFile_AFifoSwappedInIsRefusedPromptly(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "f")
	if err := os.WriteFile(f, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := openStore(t, dir)
	info, err := root.Lstat("f")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(f, filepath.Join(dir, "f.moved")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(f, 0o644); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		opened, err := openVerifiedFile(root, "f", info)
		if err == nil {
			opened.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("opened a fifo that replaced a regular file after its Lstat")
		}
	case <-time.After(5 * time.Second):
		// Unblock the stuck open so the test process can exit.
		if w, err := os.OpenFile(f, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			w.Close()
		}
		t.Fatal("the open blocked on a fifo swapped in at a regular file's name")
	}
}

// buildChain makes depth nested directories below dir, one per level.
func buildChain(t *testing.T, dir string, depth int) {
	t.Helper()
	p := dir
	for i := 0; i < depth; i++ {
		p = filepath.Join(p, "n")
	}
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

// Every level of a walk holds a handle open, and the depth is the step pod's
// to choose, so the walk refuses a tree deeper than its bound rather than
// running the daemon out of descriptors.
func TestTreeWalk_RefusesATreeDeeperThanItsBound(t *testing.T) {
	if maxTreeWalkDepth < 1024 {
		t.Fatalf("maxTreeWalkDepth is %d, too shallow for real trees", maxTreeWalkDepth)
	}
	const bound = 4
	visitAll := func(treeEntry) error { return nil }

	t.Run("at the bound", func(t *testing.T) {
		store := t.TempDir()
		buildChain(t, filepath.Join(store, "steps", "h", "out"), bound)
		if err := (treeWalk{maxDepth: bound}).walk(context.Background(), openStore(t, store), "steps/h/out", visitAll); err != nil {
			t.Fatalf("a tree exactly at the bound was refused: %v", err)
		}
	})

	t.Run("one deeper", func(t *testing.T) {
		store := t.TempDir()
		buildChain(t, filepath.Join(store, "steps", "h", "out"), bound+1)
		err := (treeWalk{maxDepth: bound}).walk(context.Background(), openStore(t, store), "steps/h/out", visitAll)
		if !errors.Is(err, errTreeTooDeep) || !errors.Is(err, ErrRefused) {
			t.Fatalf("want errTreeTooDeep as a refusal, got %v", err)
		}
	})
}

// The Lstat→open window inside the walk itself: beforeOpen changes the entry
// after the walk has classified it and before it opens it. The links stay in
// the tree, so os.Root follows them and only the identity check refuses.
func TestTreeWalk_RefusesAnEntrySwappedBetweenLstatAndOpen(t *testing.T) {
	for _, tc := range []struct {
		name, entry, target string
	}{
		{"a directory", "d", "e"},
		{"a regular file", "a.txt", "e/e-file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := t.TempDir()
			src := filepath.Join(store, "steps", "h", "out")
			for _, dir := range []string{filepath.Join(src, "d"), filepath.Join(src, "e")} {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			for p, content := range map[string]string{
				filepath.Join(src, "a.txt"):         "mine",
				filepath.Join(src, "d", "mine.txt"): "mine",
				filepath.Join(src, "e", "e-file"):   victimSecret,
			} {
				if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			swapped := false
			walk := treeWalk{beforeOpen: func(rel string) {
				if rel != tc.entry || swapped {
					return
				}
				swapped = true
				if err := os.Rename(filepath.Join(src, tc.entry), filepath.Join(store, "steps", "h", "moved")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(tc.target, filepath.Join(src, tc.entry)); err != nil {
					t.Fatal(err)
				}
			}}

			var visited []string
			err := walk.walk(context.Background(), openStore(t, store), "steps/h/out", func(e treeEntry) error {
				visited = append(visited, e.rel)
				if e.file != nil {
					body, readErr := io.ReadAll(e.file)
					if readErr != nil {
						return readErr
					}
					if strings.Contains(string(body), victimSecret) {
						t.Errorf("the walk read %s through the swapped-in link", e.rel)
					}
				}
				return nil
			})
			if !swapped {
				t.Fatal("beforeOpen never ran for the entry — this test proves nothing")
			}
			if err == nil || !strings.Contains(err.Error(), "changed while it was being opened") {
				t.Fatalf("want the identity check to refuse the swapped entry, got %v (visited %v)", err, visited)
			}
			for _, rel := range visited {
				if strings.HasPrefix(rel, tc.entry+"/") {
					t.Errorf("the walk descended into %s through the swapped-in link", rel)
				}
			}
		})
	}
}
