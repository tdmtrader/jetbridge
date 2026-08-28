package main_test

// Chained symlink entries: validateSymlinkTarget judges a link by its LEXICAL
// name, but os.Root creates it at its ON-DISK location — and the two diverge
// the moment an earlier entry in the same archive is a directory symlink.
//
// The two-entry archive [symlink "a" -> ".", symlink "a/b" -> ".."] passes the
// lexical check on both entries (Join("a", "..") = "."), yet on disk "a"
// resolves to the extraction root, so "b" lands AT the root with target ".." —
// a link resolving above the artifact, which is exactly what the validator
// exists to refuse. The archive as a whole must be refused: no entry may use a
// symlink created by this archive as a path component, because from that point
// on lexical validation is validating the wrong location.

import (
	"archive/tar"
	"os"
	"path/filepath"
	"testing"
)

func TestStreamIn_ChainedSymlinksAreRefused(t *testing.T) {
	t.Run("symlink under a directory-symlink is refused", func(t *testing.T) {
		ts, storagePath := setupServer(t)
		b := tarOf(t, []*tar.Header{
			{Name: "a", Typeflag: tar.TypeSymlink, Linkname: ".", Mode: 0o777},
			{Name: "a/b", Typeflag: tar.TypeSymlink, Linkname: "..", Mode: 0o777},
		}, nil)
		if got := put(t, ts.URL+"/stream-in/chain/out", b); got != 400 {
			t.Errorf("got %d, want 400", got)
		}
		// The staged-extract-then-rename discipline means a refused archive
		// leaves nothing behind — in particular not the mislocated link, which
		// lands at out/b, not at its lexical name out/a/b.
		if _, err := os.Lstat(filepath.Join(storagePath, "steps", "chain", "out")); !os.IsNotExist(err) {
			t.Errorf("refused archive left the artifact directory behind (err=%v)", err)
		}
	})

	// A plain FILE written through an archive-internal symlink is NOT an
	// escape: os.Root follows the inward-pointing link and the file lands
	// inside the destination (here at out/f.txt, since "a" -> "."). A regular
	// file cannot resolve outward, so the security property — nothing lands
	// outside the destination — holds whether the daemon accepts or refuses.
	// The authoritative pass only judges symlinks; assert containment, not a
	// specific status.
	t.Run("file written through a symlinked dir stays contained", func(t *testing.T) {
		ts, storagePath := setupServer(t)
		b := tarOf(t, []*tar.Header{
			{Name: "a", Typeflag: tar.TypeSymlink, Linkname: ".", Mode: 0o777},
			{Name: "a/f.txt", Typeflag: tar.TypeReg, Size: 2, Mode: 0o644},
		}, []string{"", "xx"})
		got := put(t, ts.URL+"/stream-in/chain-f/out", b)
		if got != 201 && got != 400 {
			t.Fatalf("got %d, want 201 or 400", got)
		}
		// However it resolved, no file may exist ABOVE the artifact directory.
		outParent := filepath.Join(storagePath, "steps", "chain-f")
		if _, err := os.Stat(filepath.Join(outParent, "f.txt")); err == nil {
			t.Error("file escaped the artifact directory into its parent")
		}
	})

	t.Run("hard link to a symlink is a symlink too", func(t *testing.T) {
		// root.Link on a symlink target links the symlink INODE, so "l" is
		// another name for the symlink and "l/x" traverses it the same way.
		ts, _ := setupServer(t)
		b := tarOf(t, []*tar.Header{
			{Name: "s", Typeflag: tar.TypeSymlink, Linkname: ".", Mode: 0o777},
			{Name: "l", Typeflag: tar.TypeLink, Linkname: "s", Mode: 0o644},
			{Name: "l/x", Typeflag: tar.TypeSymlink, Linkname: "..", Mode: 0o777},
		}, nil)
		if got := put(t, ts.URL+"/stream-in/chain-h/out", b); got != 400 {
			t.Errorf("got %d, want 400", got)
		}
	})

	t.Run("normalised names still match", func(t *testing.T) {
		ts, _ := setupServer(t)
		b := tarOf(t, []*tar.Header{
			{Name: "./a", Typeflag: tar.TypeSymlink, Linkname: ".", Mode: 0o777},
			{Name: "a/b", Typeflag: tar.TypeSymlink, Linkname: "..", Mode: 0o777},
		}, nil)
		if got := put(t, ts.URL+"/stream-in/chain-n/out", b); got != 400 {
			t.Errorf("got %d, want 400", got)
		}
	})

	// A symlink whose TARGET (not its name) traverses an earlier link. Lexical
	// validation of the target passes — Clean("a/..") == "." — but on disk "a"
	// is a symlink to the root, so "esc" -> "a/.." resolves to the root's
	// PARENT. Reported and reproduced during review.
	t.Run("symlink target traverses an earlier symlink", func(t *testing.T) {
		ts, _ := setupServer(t)
		b := tarOf(t, []*tar.Header{
			{Name: "a", Typeflag: tar.TypeSymlink, Linkname: ".", Mode: 0o777},
			{Name: "esc", Typeflag: tar.TypeSymlink, Linkname: "a/..", Mode: 0o777},
		}, nil)
		if got := put(t, ts.URL+"/stream-in/chain-tgt/out", b); got != 400 {
			t.Errorf("got %d, want 400", got)
		}
	})

	// A name whose symlink component is followed by "..": path.Clean collapses
	// "y/.." lexically, but os.Root walks "x" -> "y"(symlink to root) -> ".." to
	// the root's parent. The escape hides in the gap between Clean and the
	// kernel's component-by-component resolution.
	t.Run("name with a symlink component then dotdot", func(t *testing.T) {
		ts, _ := setupServer(t)
		b := tarOf(t, []*tar.Header{
			{Name: "x/y", Typeflag: tar.TypeSymlink, Linkname: ".", Mode: 0o777},
			{Name: "x/y/../q", Typeflag: tar.TypeSymlink, Linkname: "../secret", Mode: 0o777},
		}, nil)
		if got := put(t, ts.URL+"/stream-in/chain-dd/out", b); got != 400 {
			t.Errorf("got %d, want 400", got)
		}
	})

	// A hard link whose Linkname reaches a symlink THROUGH a symlinked
	// directory: root.Link aliases the symlink inode, so the hard-linked name
	// is itself a symlink and can be traversed again.
	t.Run("hard link through a symlinked directory", func(t *testing.T) {
		ts, _ := setupServer(t)
		b := tarOf(t, []*tar.Header{
			{Name: "dir", Typeflag: tar.TypeSymlink, Linkname: ".", Mode: 0o777},
			{Name: "s", Typeflag: tar.TypeSymlink, Linkname: ".", Mode: 0o777},
			{Name: "t", Typeflag: tar.TypeLink, Linkname: "dir/s", Mode: 0o644},
			{Name: "t/evil", Typeflag: tar.TypeSymlink, Linkname: "..", Mode: 0o777},
		}, nil)
		if got := put(t, ts.URL+"/stream-in/chain-hl/out", b); got != 400 {
			t.Errorf("got %d, want 400", got)
		}
	})

	// The target contains "symlink/..": filepath.Join would collapse it
	// lexically ("a/.." -> nothing) and judge the link contained, but the
	// kernel follows the symlink "a" first, so "a/../.." climbs one level
	// higher than the lexical form. The link's own directory depth supplies
	// the extra level the escape needs. Reproduced during review.
	t.Run("target with an intermediate-symlink dotdot escapes", func(t *testing.T) {
		ts, _ := setupServer(t)
		b := tarOf(t, []*tar.Header{
			{Name: "sub/a", Typeflag: tar.TypeSymlink, Linkname: ".", Mode: 0o777},
			{Name: "sub/esc", Typeflag: tar.TypeSymlink, Linkname: "a/../../evil/secret", Mode: 0o777},
		}, nil)
		if got := put(t, ts.URL+"/stream-in/chain-tgtdd/out", b); got != 400 {
			t.Errorf("got %d, want 400", got)
		}
	})

	// Positive control for the fix above: a link that resolves to the artifact
	// ROOT is contained, not an escape. node_modules/<pkg> -> .. is the
	// standard npm/yarn/pnpm self-referencing-package symlink and MUST survive.
	t.Run("symlink resolving to the artifact root is kept", func(t *testing.T) {
		ts, storagePath := setupServer(t)
		b := tarOf(t, []*tar.Header{
			{Name: "package.json", Typeflag: tar.TypeReg, Size: 2, Mode: 0o644},
			{Name: "node_modules/self", Typeflag: tar.TypeSymlink, Linkname: "..", Mode: 0o777},
		}, []string{"{}", ""})
		if got := put(t, ts.URL+"/stream-in/npm-self/out", b); got != 201 {
			t.Fatalf("got %d, want 201 — a link to the artifact root was refused as an escape", got)
		}
		link := filepath.Join(storagePath, "steps", "npm-self", "out", "node_modules", "self")
		fi, err := os.Lstat(link)
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("self-referencing package link lost: err=%v", err)
		}
		// And it resolves back to the artifact's own package.json.
		if _, err := os.Stat(filepath.Join(link, "package.json")); err != nil {
			t.Errorf("root-referencing link does not resolve inside the artifact: %v", err)
		}
	})

	// A top-level "self -> ." must also survive: it names the destination.
	t.Run("self link to dot is kept", func(t *testing.T) {
		ts, _ := setupServer(t)
		b := tarOf(t, []*tar.Header{
			{Name: "f.txt", Typeflag: tar.TypeReg, Size: 2, Mode: 0o644},
			{Name: "self", Typeflag: tar.TypeSymlink, Linkname: ".", Mode: 0o777},
		}, []string{"ok", ""})
		if got := put(t, ts.URL+"/stream-in/self-dot/out", b); got != 201 {
			t.Errorf("got %d, want 201 — a self link to '.' was refused", got)
		}
	})

	// Positive controls: the rule is "no entry TRAVERSES a symlink", not "no
	// archive contains one". Our own producer (tarTree walks with fs.WalkDir,
	// which does not descend into links) never emits a traversing entry, so
	// every legitimate archive stays accepted.
	t.Run("sibling entries next to a symlink still land", func(t *testing.T) {
		ts, storagePath := setupServer(t)
		b := tarOf(t, []*tar.Header{
			{Name: "shared/pkg.txt", Typeflag: tar.TypeReg, Size: 2, Mode: 0o644},
			{Name: "app/node_modules", Typeflag: tar.TypeSymlink, Linkname: "../shared", Mode: 0o777},
			{Name: "app/main.go", Typeflag: tar.TypeReg, Size: 2, Mode: 0o644},
		}, []string{"ok", "", "go"})
		if got := put(t, ts.URL+"/stream-in/chain-ok/out", b); got != 201 {
			t.Fatalf("got %d, want 201 — the traversal rule refused a legitimate archive", got)
		}
		if _, err := os.Stat(filepath.Join(storagePath, "steps", "chain-ok", "out", "app", "main.go")); err != nil {
			t.Errorf("sibling of a symlink did not land: %v", err)
		}
	})

	t.Run("entries under a real directory still land", func(t *testing.T) {
		ts, storagePath := setupServer(t)
		b := tarOf(t, []*tar.Header{
			{Name: "d", Typeflag: tar.TypeDir, Mode: 0o755},
			{Name: "d/f.txt", Typeflag: tar.TypeReg, Size: 2, Mode: 0o644},
		}, []string{"", "ok"})
		if got := put(t, ts.URL+"/stream-in/chain-d/out", b); got != 201 {
			t.Fatalf("got %d, want 201", got)
		}
		if _, err := os.Stat(filepath.Join(storagePath, "steps", "chain-d", "out", "d", "f.txt")); err != nil {
			t.Errorf("file under a real directory did not land: %v", err)
		}
	})
}
