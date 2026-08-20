//go:build linux || darwin

package hangar

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestMaterializerPublishesExactReadOnlyTreeAtDerivedDestination(t *testing.T) {
	raw := testTreeArchive(t, []testTreeEntry{
		{name: "dir", mode: 0750, kind: tar.TypeDir},
		{name: "dir/file", mode: 0766, kind: tar.TypeReg, body: "hello"},
		{name: "link", mode: 0777, kind: tar.TypeSymlink, link: "dir/file"},
	})
	ref, canonical := canonicalTreeFixture(t, raw)
	store := &strictMaterializerStore{want: ref, archive: canonical}
	storage := t.TempDir()
	cleanupMaterializedStorage(t, storage)
	materializer := Materializer{Store: store, Canonicalizer: Canonicalizer{}, StoragePath: storage, MaxTreeBytes: 1 << 20}

	if err := materializer.Materialize(context.Background(), ref, "handle", "volume"); err != nil {
		t.Fatal(err)
	}
	if store.opens != 1 {
		t.Fatalf("opened tree %d times, want once", store.opens)
	}
	destination := filepath.Join(storage, "steps", "handle", "volume")
	assertMaterializedMode(t, destination, 0555)
	assertMaterializedMode(t, filepath.Join(destination, "dir"), 0555)
	assertMaterializedMode(t, filepath.Join(destination, "dir", "file"), 0444)
	content, err := os.ReadFile(filepath.Join(destination, "dir", "file"))
	if err != nil || string(content) != "hello" {
		t.Fatalf("published content = %q, %v", content, err)
	}
	target, err := os.Readlink(filepath.Join(destination, "link"))
	if err != nil || target != "dir/file" {
		t.Fatalf("symlink target = %q, %v", target, err)
	}
}

func TestMaterializerPreservesPreopenedEmptyDestination(t *testing.T) {
	ref, canonical := canonicalTreeFixture(t, testTreeArchive(t, []testTreeEntry{{name: "result", mode: 0600, kind: tar.TypeReg, body: "ready"}}))
	storage := t.TempDir()
	cleanupMaterializedStorage(t, storage)
	destination := filepath.Join(storage, "steps", "handle", "volume")
	if err := os.MkdirAll(destination, 0755); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	before, err := root.Stat(".")
	if err != nil {
		t.Fatal(err)
	}

	materializer := Materializer{Store: &strictMaterializerStore{want: ref, archive: canonical}, Canonicalizer: Canonicalizer{}, StoragePath: storage, MaxTreeBytes: 1 << 20, hooks: withTestReceiptPrivilege(destination, materializerHooks{})}
	if err := materializer.Materialize(context.Background(), ref, "handle", "volume"); err != nil {
		t.Fatal(err)
	}
	after, err := root.Stat(".")
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("materialization replaced the existing destination inode")
	}
	content, err := root.ReadFile("result")
	if err != nil || string(content) != "ready" {
		t.Fatalf("pre-opened descriptor sees %q, %v", content, err)
	}
}

func TestMaterializerRejectsInvalidOrOccupiedDestinations(t *testing.T) {
	ref, canonical := canonicalTreeFixture(t, testTreeArchive(t, []testTreeEntry{{name: "file", kind: tar.TypeReg, body: "data"}}))
	storage := t.TempDir()
	cleanupMaterializedStorage(t, storage)
	store := &strictMaterializerStore{want: ref, archive: canonical}
	materializer := Materializer{Store: store, Canonicalizer: Canonicalizer{}, StoragePath: storage, MaxTreeBytes: 1 << 20}
	for _, pair := range [][2]string{{"../escape", "volume"}, {"handle", "a/b"}, {".", "volume"}} {
		if err := materializer.Materialize(context.Background(), ref, pair[0], pair[1]); err == nil {
			t.Fatalf("accepted invalid destination segments %q/%q", pair[0], pair[1])
		}
	}
	destination := filepath.Join(storage, "steps", "handle", "volume")
	if err := os.MkdirAll(destination, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "victim"), []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := materializer.Materialize(context.Background(), ref, "handle", "volume"); !errors.Is(err, ErrConflict) {
		t.Fatalf("non-empty destination got %v", err)
	}
	content, _ := os.ReadFile(filepath.Join(destination, "victim"))
	if string(content) != "keep" {
		t.Fatalf("occupied destination was mutated: %q", content)
	}
}

func TestMaterializerConcurrentExactRequestIsIdempotent(t *testing.T) {
	ref, canonical := canonicalTreeFixture(t, testTreeArchive(t, []testTreeEntry{{name: "file", kind: tar.TypeReg, body: "data"}}))
	storage := t.TempDir()
	cleanupMaterializedStorage(t, storage)
	materializer := Materializer{Store: &strictMaterializerStore{want: ref, archive: canonical}, Canonicalizer: Canonicalizer{}, StoragePath: storage, MaxTreeBytes: 1 << 20}
	start := make(chan struct{})
	errs := make(chan error, 2)
	for index := 0; index < 2; index++ {
		go func() {
			<-start
			errs <- materializer.Materialize(context.Background(), ref, "handle", "volume")
		}()
	}
	close(start)
	for index := 0; index < 2; index++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent materialization: %v", err)
		}
	}
	content, err := os.ReadFile(filepath.Join(storage, "steps", "handle", "volume", "file"))
	if err != nil || string(content) != "data" {
		t.Fatalf("final tree = %q, %v", content, err)
	}
}

func TestMaterializerDoesNotAcceptForgedReceiptForPartialTree(t *testing.T) {
	ref, canonical := canonicalTreeFixture(t, testTreeArchive(t, []testTreeEntry{{name: "file", kind: tar.TypeReg, body: "expected"}}))
	storage := t.TempDir()
	cleanupMaterializedStorage(t, storage)
	destination := filepath.Join(storage, "steps", "handle", "volume")
	if err := os.MkdirAll(destination, 0755); err != nil {
		t.Fatal(err)
	}
	receipt, err := json.Marshal(ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, materializationReceiptName), receipt, 0444); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "file"), []byte("attacker"), 0444); err != nil {
		t.Fatal(err)
	}
	materializer := Materializer{Store: &strictMaterializerStore{want: ref, archive: canonical}, Canonicalizer: Canonicalizer{}, StoragePath: storage, MaxTreeBytes: 1 << 20}
	if err := materializer.Materialize(context.Background(), ref, "handle", "volume"); !errors.Is(err, ErrConflict) {
		t.Fatalf("forged partial tree got %v", err)
	}
	content, _ := os.ReadFile(filepath.Join(destination, "file"))
	if string(content) != "attacker" {
		t.Fatalf("forged destination was mutated: %q", content)
	}
}

func TestMaterializerRejectsReplacementDuringRetryComparison(t *testing.T) {
	ref, canonical := canonicalTreeFixture(t, testTreeArchive(t, []testTreeEntry{{name: "file", kind: tar.TypeReg, body: "same"}}))
	storage := t.TempDir()
	cleanupMaterializedStorage(t, storage)
	destination := filepath.Join(storage, "steps", "handle", "volume")
	base := Materializer{Store: &strictMaterializerStore{want: ref, archive: canonical}, Canonicalizer: Canonicalizer{}, StoragePath: storage, MaxTreeBytes: 1 << 20}
	if err := base.Materialize(context.Background(), ref, "handle", "volume"); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	attackErr := error(nil)
	base.hooks.duringRetryCompare = func() error {
		once.Do(func() {
			name := filepath.Join(destination, "file")
			if err := os.Rename(name, name+"-displaced"); err != nil {
				attackErr = err
				return
			}
			attackErr = os.WriteFile(name, []byte("same"), 0444)
		})
		return attackErr
	}
	if err := base.Materialize(context.Background(), ref, "handle", "volume"); err == nil {
		t.Fatal("replacement during retry comparison was accepted")
	}
}

func TestMaterializerRevalidatesPayloadBytesAfterMutationHooks(t *testing.T) {
	ref, canonical := canonicalTreeFixture(t, testTreeArchive(t, []testTreeEntry{{name: "file", kind: tar.TypeReg, body: "good"}}))
	for _, test := range []struct {
		name     string
		existing bool
		mutate   func(string) materializerHooks
	}{
		{
			name: "staged payload",
			mutate: func(_ string) materializerHooks {
				return materializerHooks{afterStage: func(stagePath string) error {
					return os.WriteFile(filepath.Join(stagePath, "file"), []byte("evil"), 0644)
				}}
			},
		},
		{
			name:     "transferred payload",
			existing: true,
			mutate: func(destination string) materializerHooks {
				return materializerHooks{beforePayloadSeal: func() error {
					return os.WriteFile(filepath.Join(destination, "file"), []byte("evil"), 0644)
				}}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			storage := t.TempDir()
			cleanupMaterializedStorage(t, storage)
			destination := filepath.Join(storage, "steps", "handle", "volume")
			if test.existing {
				if err := os.MkdirAll(destination, 0755); err != nil {
					t.Fatal(err)
				}
			}
			materializer := Materializer{
				Store:         &strictMaterializerStore{want: ref, archive: canonical},
				Canonicalizer: Canonicalizer{},
				StoragePath:   storage,
				MaxTreeBytes:  1 << 20,
				hooks:         test.mutate(destination),
			}
			if err := materializer.Materialize(context.Background(), ref, "handle", "volume"); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("same-inode payload rewrite got %v, want ErrCorrupt", err)
			}
			if _, err := os.Lstat(filepath.Join(destination, materializationReceiptName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("payload rewrite published a completion receipt: %v", err)
			}
		})
	}
}

func TestMaterializerRetryRechecksAuthorityAfterComparison(t *testing.T) {
	ref, canonical := canonicalTreeFixture(t, testTreeArchive(t, []testTreeEntry{{name: "file", kind: tar.TypeReg, body: "same"}}))
	storage := t.TempDir()
	cleanupMaterializedStorage(t, storage)
	handlePath := filepath.Join(storage, "steps", "handle")
	materializer := Materializer{Store: &strictMaterializerStore{want: ref, archive: canonical}, Canonicalizer: Canonicalizer{}, StoragePath: storage, MaxTreeBytes: 1 << 20}
	if err := materializer.Materialize(context.Background(), ref, "handle", "volume"); err != nil {
		t.Fatal(err)
	}
	materializer.hooks.afterDestinationOpen = func() error {
		if err := os.Rename(handlePath, handlePath+"-displaced"); err != nil {
			return err
		}
		if err := os.Mkdir(handlePath, 0755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(handlePath, "victim"), []byte("keep"), 0644)
	}
	if err := materializer.Materialize(context.Background(), ref, "handle", "volume"); err == nil {
		t.Fatal("retry accepted an ancestor swap after opening the destination")
	}
	content, err := os.ReadFile(filepath.Join(handlePath, "victim"))
	if err != nil || string(content) != "keep" {
		t.Fatalf("replacement authority changed: %q, %v", content, err)
	}
}

func TestMaterializerRetryComparisonRechecksNamespaceAndMetadata(t *testing.T) {
	for _, attack := range []string{"extra entry", "same-inode mode mutation"} {
		t.Run(attack, func(t *testing.T) {
			ref, canonical := canonicalTreeFixture(t, testTreeArchive(t, []testTreeEntry{{name: "file", kind: tar.TypeReg, body: "same"}}))
			storage := t.TempDir()
			cleanupMaterializedStorage(t, storage)
			destination := filepath.Join(storage, "steps", "handle", "volume")
			materializer := Materializer{Store: &strictMaterializerStore{want: ref, archive: canonical}, Canonicalizer: Canonicalizer{}, StoragePath: storage, MaxTreeBytes: 1 << 20}
			if err := materializer.Materialize(context.Background(), ref, "handle", "volume"); err != nil {
				t.Fatal(err)
			}
			var once sync.Once
			var attackErr error
			materializer.hooks.duringRetryCompare = func() error {
				once.Do(func() {
					switch attack {
					case "extra entry":
						if err := os.Chmod(destination, 0700); err != nil {
							attackErr = err
							return
						}
						if err := os.WriteFile(filepath.Join(destination, "extra"), []byte("injected"), 0444); err != nil {
							attackErr = err
							return
						}
						attackErr = os.Chmod(destination, 0555)
					case "same-inode mode mutation":
						attackErr = os.Chmod(filepath.Join(destination, "file"), 0400)
					}
				})
				return attackErr
			}
			if err := materializer.Materialize(context.Background(), ref, "handle", "volume"); err == nil {
				t.Fatalf("retry accepted %s", attack)
			}
		})
	}
}

func TestMaterializerReceiptCollisionRaceNeverOverwritesWinner(t *testing.T) {
	ref, canonical := canonicalTreeFixture(t, testTreeArchive(t, []testTreeEntry{{name: "file", kind: tar.TypeReg, body: "payload"}}))
	storage := t.TempDir()
	cleanupMaterializedStorage(t, storage)
	destination := filepath.Join(storage, "steps", "handle", "volume")
	if err := os.MkdirAll(destination, 0755); err != nil {
		t.Fatal(err)
	}
	hooks := materializerHooks{beforeReceiptRename: func() error {
		if err := os.Chmod(destination, 0700); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(destination, materializationReceiptName), []byte("winner"), 0444)
	}}
	materializer := Materializer{Store: &strictMaterializerStore{want: ref, archive: canonical}, Canonicalizer: Canonicalizer{}, StoragePath: storage, MaxTreeBytes: 1 << 20, hooks: hooks}
	if err := materializer.Materialize(context.Background(), ref, "handle", "volume"); err == nil {
		t.Fatal("receipt collision was overwritten")
	}
	contents, err := os.ReadFile(filepath.Join(destination, materializationReceiptName))
	if err != nil || string(contents) != "winner" {
		t.Fatalf("colliding receipt changed: %q, %v", contents, err)
	}
	if _, err := os.Lstat(filepath.Join(destination, "file")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed collision left owned payload: %v", err)
	}
}

func TestMaterializerRejectsSourceReceiptCollisionWithoutPublishing(t *testing.T) {
	ref, canonical := canonicalTreeFixture(t, testTreeArchive(t, []testTreeEntry{{name: materializationReceiptName, kind: tar.TypeReg, body: "forged"}}))
	storage := t.TempDir()
	cleanupMaterializedStorage(t, storage)
	materializer := Materializer{Store: &strictMaterializerStore{want: ref, archive: canonical}, Canonicalizer: Canonicalizer{}, StoragePath: storage, MaxTreeBytes: 1 << 20}
	if err := materializer.Materialize(context.Background(), ref, "handle", "volume"); err == nil {
		t.Fatal("accepted a source path colliding with the receipt")
	}
	if _, err := os.Lstat(filepath.Join(storage, "steps", "handle", "volume")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed materialization became visible: %v", err)
	}
}

func TestMaterializerCleansExistingDestinationBeforeReceiptButNotAfter(t *testing.T) {
	ref, canonical := canonicalTreeFixture(t, testTreeArchive(t, []testTreeEntry{{name: "dir/file", kind: tar.TypeReg, body: "data"}}))
	for _, test := range []struct {
		name        string
		hooks       materializerHooks
		wantReceipt bool
	}{
		{name: "before receipt", hooks: materializerHooks{beforeReceipt: func() error { return errors.New("injected before receipt") }}},
		{name: "after receipt", hooks: materializerHooks{afterReceipt: func() error { return errors.New("injected after receipt") }}, wantReceipt: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			storage := t.TempDir()
			cleanupMaterializedStorage(t, storage)
			destination := filepath.Join(storage, "steps", "handle", "volume")
			if err := os.MkdirAll(destination, 0755); err != nil {
				t.Fatal(err)
			}
			materializer := Materializer{Store: &strictMaterializerStore{want: ref, archive: canonical}, Canonicalizer: Canonicalizer{}, StoragePath: storage, MaxTreeBytes: 1 << 20, hooks: withTestReceiptPrivilege(destination, test.hooks)}
			if err := materializer.Materialize(context.Background(), ref, "handle", "volume"); err == nil {
				t.Fatal("injected failure was ignored")
			}
			_, receiptErr := os.Lstat(filepath.Join(destination, materializationReceiptName))
			if test.wantReceipt && receiptErr != nil {
				t.Fatalf("post-commit failure destroyed receipt: %v", receiptErr)
			}
			if !test.wantReceipt {
				entries, err := os.ReadDir(destination)
				if err != nil || len(entries) != 0 {
					t.Fatalf("pre-commit failure left partial destination: %v, %v", entries, err)
				}
			}
		})
	}
}

func TestMaterializerAbsentPostReceiptFailureIsNonDestructive(t *testing.T) {
	ref, canonical := canonicalTreeFixture(t, testTreeArchive(t, []testTreeEntry{{name: "file", kind: tar.TypeReg, body: "data"}}))
	storage := t.TempDir()
	cleanupMaterializedStorage(t, storage)
	hooks := materializerHooks{afterReceipt: func() error { return errors.New("injected after receipt") }}
	materializer := Materializer{Store: &strictMaterializerStore{want: ref, archive: canonical}, Canonicalizer: Canonicalizer{}, StoragePath: storage, MaxTreeBytes: 1 << 20, hooks: hooks}
	if err := materializer.Materialize(context.Background(), ref, "handle", "volume"); err == nil {
		t.Fatal("post-receipt failure was ignored")
	}
	destination := filepath.Join(storage, "steps", "handle", "volume")
	content, err := os.ReadFile(filepath.Join(destination, "file"))
	if err != nil || string(content) != "data" {
		t.Fatalf("post-receipt failure destroyed payload: %q, %v", content, err)
	}
	if _, err := os.Lstat(filepath.Join(destination, materializationReceiptName)); err != nil {
		t.Fatalf("post-receipt failure destroyed receipt: %v", err)
	}
}

func TestMaterializerKeepsStagePrivateUntilPublication(t *testing.T) {
	ref, canonical := canonicalTreeFixture(t, testTreeArchive(t, []testTreeEntry{{name: "file", kind: tar.TypeReg, body: "data"}}))
	storage := t.TempDir()
	cleanupMaterializedStorage(t, storage)
	observed := os.FileMode(0)
	hooks := materializerHooks{afterStage: func(stagePath string) error {
		info, err := os.Stat(stagePath)
		if err == nil {
			observed = info.Mode().Perm()
		}
		return err
	}}
	materializer := Materializer{Store: &strictMaterializerStore{want: ref, archive: canonical}, Canonicalizer: Canonicalizer{}, StoragePath: storage, MaxTreeBytes: 1 << 20, hooks: hooks}
	if err := materializer.Materialize(context.Background(), ref, "handle", "volume"); err != nil {
		t.Fatal(err)
	}
	if observed != 0700 {
		t.Fatalf("private stage mode = %#o, want 0700", observed)
	}
}

func TestMaterializerRejectsUnsafeArchiveWithoutVisiblePartialTree(t *testing.T) {
	ref := mustGrantRef(t, "builds", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 2)
	for name, raw := range map[string][]byte{
		"escaping symlink": testTreeArchive(t, []testTreeEntry{{name: "link", kind: tar.TypeSymlink, link: "../escape"}}),
		"special file":     testTreeArchive(t, []testTreeEntry{{name: "device", kind: tar.TypeChar}}),
	} {
		t.Run(name, func(t *testing.T) {
			storage := t.TempDir()
			cleanupMaterializedStorage(t, storage)
			materializer := Materializer{Store: &strictMaterializerStore{want: ref, archive: raw}, Canonicalizer: Canonicalizer{}, StoragePath: storage, MaxTreeBytes: 1 << 20}
			if err := materializer.Materialize(context.Background(), ref, "handle", "volume"); err == nil {
				t.Fatal("unsafe archive was accepted")
			}
			if _, err := os.Lstat(filepath.Join(storage, "steps", "handle", "volume")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unsafe archive left visible state: %v", err)
			}
		})
	}
}

func TestMaterializerStoreDigestAndCancellationFailuresLeaveNoDestination(t *testing.T) {
	ref, canonical := canonicalTreeFixture(t, testTreeArchive(t, []testTreeEntry{{name: "file", kind: tar.TypeReg, body: "data"}}))
	wrongDigest := ref
	wrongDigest.Digest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, test := range []struct {
		name  string
		ctx   context.Context
		ref   TreeRef
		store *strictMaterializerStore
	}{
		{name: "store", ctx: context.Background(), ref: ref, store: &strictMaterializerStore{want: ref, openErr: ErrInfrastructure}},
		{name: "digest", ctx: context.Background(), ref: wrongDigest, store: &strictMaterializerStore{want: wrongDigest, archive: canonical}},
		{name: "cancellation", ctx: canceled, ref: ref, store: &strictMaterializerStore{want: ref, archive: canonical}},
	} {
		t.Run(test.name, func(t *testing.T) {
			storage := t.TempDir()
			cleanupMaterializedStorage(t, storage)
			materializer := Materializer{Store: test.store, Canonicalizer: Canonicalizer{}, StoragePath: storage, MaxTreeBytes: 1 << 20}
			if err := materializer.Materialize(test.ctx, test.ref, "handle", "volume"); err == nil {
				t.Fatal("failure was ignored")
			}
			if _, err := os.Lstat(filepath.Join(storage, "steps", "handle", "volume")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failure left visible state: %v", err)
			}
		})
	}
}

func TestMaterializerUsesAnchoredCapturedRootAndRebindsStageDigest(t *testing.T) {
	for _, attack := range []string{"root pathname replacement", "same-length rewrite with restored metadata"} {
		t.Run(attack, func(t *testing.T) {
			ref, canonical := canonicalTreeFixture(t, testTreeArchive(t, []testTreeEntry{{name: "file", kind: tar.TypeReg, body: "good"}}))
			storage := t.TempDir()
			cleanupMaterializedStorage(t, storage)
			hooks := materializerHooks{afterCapture: func(tree *CapturedTree) error {
				if attack == "root pathname replacement" {
					if err := os.Rename(tree.Root, tree.Root+"-anchored"); err != nil {
						return err
					}
					if err := os.Mkdir(tree.Root, 0700); err != nil {
						return err
					}
					return os.WriteFile(filepath.Join(tree.Root, "file"), []byte("evil"), 0644)
				}
				name := filepath.Join(tree.Root, "file")
				info, err := os.Stat(name)
				if err != nil {
					return err
				}
				if err := os.WriteFile(name, []byte("evil"), info.Mode().Perm()); err != nil {
					return err
				}
				return os.Chtimes(name, info.ModTime(), info.ModTime())
			}}
			materializer := Materializer{Store: &strictMaterializerStore{want: ref, archive: canonical}, Canonicalizer: Canonicalizer{}, StoragePath: storage, MaxTreeBytes: 1 << 20, hooks: hooks}
			err := materializer.Materialize(context.Background(), ref, "handle", "volume")
			if attack == "same-length rewrite with restored metadata" {
				if !errors.Is(err, ErrCorrupt) {
					t.Fatalf("rewritten source got %v, want ErrCorrupt", err)
				}
				if _, statErr := os.Lstat(filepath.Join(storage, "steps", "handle", "volume")); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("rewritten source became visible: %v", statErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			content, err := os.ReadFile(filepath.Join(storage, "steps", "handle", "volume", "file"))
			if err != nil || string(content) != "good" {
				t.Fatalf("published replacement content %q, %v", content, err)
			}
		})
	}
}

func TestMaterializerAbsentDestinationRaceNeverOverwritesWinner(t *testing.T) {
	firstRef, firstArchive := canonicalTreeFixture(t, testTreeArchive(t, []testTreeEntry{{name: "file", kind: tar.TypeReg, body: "first"}}))
	secondRef, secondArchive := canonicalTreeFixture(t, testTreeArchive(t, []testTreeEntry{{name: "file", kind: tar.TypeReg, body: "second"}}))
	secondRef.Generation++
	storage := t.TempDir()
	cleanupMaterializedStorage(t, storage)
	materializers := []*Materializer{
		{Store: &strictMaterializerStore{want: firstRef, archive: firstArchive}, Canonicalizer: Canonicalizer{}, StoragePath: storage, MaxTreeBytes: 1 << 20},
		{Store: &strictMaterializerStore{want: secondRef, archive: secondArchive}, Canonicalizer: Canonicalizer{}, StoragePath: storage, MaxTreeBytes: 1 << 20},
	}
	refs := []TreeRef{firstRef, secondRef}
	start := make(chan struct{})
	errs := make(chan error, 2)
	for index := range materializers {
		go func(index int) {
			<-start
			errs <- materializers[index].Materialize(context.Background(), refs[index], "handle", "volume")
		}(index)
	}
	close(start)
	results := []error{<-errs, <-errs}
	if (results[0] == nil) == (results[1] == nil) {
		t.Fatalf("race results = %v, want one winner", results)
	}
	content, err := os.ReadFile(filepath.Join(storage, "steps", "handle", "volume", "file"))
	if err != nil || string(content) != "first" && string(content) != "second" {
		t.Fatalf("winner content = %q, %v", content, err)
	}
}

func TestMaterializerSerializesWritersForExistingEmptyDestination(t *testing.T) {
	firstRef, firstArchive := canonicalTreeFixture(t, testTreeArchive(t, []testTreeEntry{{name: "first", kind: tar.TypeReg, body: "one"}}))
	secondRef, secondArchive := canonicalTreeFixture(t, testTreeArchive(t, []testTreeEntry{{name: "second", kind: tar.TypeReg, body: "two"}}))
	storage := t.TempDir()
	cleanupMaterializedStorage(t, storage)
	destination := filepath.Join(storage, "steps", "handle", "volume")
	if err := os.MkdirAll(destination, 0755); err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{}, 2)
	start := make(chan struct{})
	beforeLock := func() error {
		ready <- struct{}{}
		<-start
		return nil
	}
	materializers := []*Materializer{
		{Store: &strictMaterializerStore{want: firstRef, archive: firstArchive}, Canonicalizer: Canonicalizer{}, StoragePath: storage, MaxTreeBytes: 1 << 20, hooks: withTestReceiptPrivilege(destination, materializerHooks{beforeLock: beforeLock})},
		{Store: &strictMaterializerStore{want: secondRef, archive: secondArchive}, Canonicalizer: Canonicalizer{}, StoragePath: storage, MaxTreeBytes: 1 << 20, hooks: withTestReceiptPrivilege(destination, materializerHooks{beforeLock: beforeLock})},
	}
	refs := []TreeRef{firstRef, secondRef}
	errs := make(chan error, 2)
	for index := range materializers {
		go func(index int) {
			errs <- materializers[index].Materialize(context.Background(), refs[index], "handle", "volume")
		}(index)
	}
	<-ready
	<-ready
	close(start)
	results := []error{<-errs, <-errs}
	if (results[0] == nil) == (results[1] == nil) {
		t.Fatalf("concurrent existing-destination results = %v, want one winner", results)
	}
	entries, err := os.ReadDir(destination)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 { // one payload and the receipt
		t.Fatalf("published mixed/partial tree: %v", entries)
	}
}

func TestMaterializerRollbackPreservesUnrelatedInjectedEntry(t *testing.T) {
	ref, canonical := canonicalTreeFixture(t, testTreeArchive(t, []testTreeEntry{{name: "owned", kind: tar.TypeReg, body: "data"}}))
	storage := t.TempDir()
	cleanupMaterializedStorage(t, storage)
	destination := filepath.Join(storage, "steps", "handle", "volume")
	if err := os.MkdirAll(destination, 0755); err != nil {
		t.Fatal(err)
	}
	hooks := materializerHooks{beforePayloadSeal: func() error {
		return os.WriteFile(filepath.Join(destination, "unrelated"), []byte("keep"), 0644)
	}}
	materializer := Materializer{Store: &strictMaterializerStore{want: ref, archive: canonical}, Canonicalizer: Canonicalizer{}, StoragePath: storage, MaxTreeBytes: 1 << 20, hooks: hooks}
	if err := materializer.Materialize(context.Background(), ref, "handle", "volume"); err == nil {
		t.Fatal("unrelated injection was accepted")
	}
	content, err := os.ReadFile(filepath.Join(destination, "unrelated"))
	if err != nil || string(content) != "keep" {
		t.Fatalf("rollback erased unrelated entry: %q, %v", content, err)
	}
	if _, err := os.Lstat(filepath.Join(destination, "owned")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rollback retained owned partial entry: %v", err)
	}
}

func TestMaterializerRejectsStagePathReplacementWithoutVictimMutation(t *testing.T) {
	ref, canonical := canonicalTreeFixture(t, testTreeArchive(t, []testTreeEntry{{name: "file", kind: tar.TypeReg, body: "data"}}))
	storage := t.TempDir()
	cleanupMaterializedStorage(t, storage)
	victim := filepath.Join(storage, "victim")
	if err := os.Mkdir(victim, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(victim, "sentinel"), []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	hooks := materializerHooks{afterStage: func(stagePath string) error {
		if err := os.Rename(stagePath, stagePath+"-displaced"); err != nil {
			return err
		}
		return os.Symlink(victim, stagePath)
	}}
	materializer := Materializer{Store: &strictMaterializerStore{want: ref, archive: canonical}, Canonicalizer: Canonicalizer{}, StoragePath: storage, MaxTreeBytes: 1 << 20, hooks: hooks}
	if err := materializer.Materialize(context.Background(), ref, "handle", "volume"); err == nil {
		t.Fatal("accepted a replaced stage path")
	}
	content, err := os.ReadFile(filepath.Join(victim, "sentinel"))
	if err != nil || string(content) != "keep" {
		t.Fatalf("stage-path victim was mutated: %q, %v", content, err)
	}
}

func TestMaterializerDoesNotCleanReplacementStageDirectory(t *testing.T) {
	ref, canonical := canonicalTreeFixture(t, testTreeArchive(t, []testTreeEntry{{name: "file", kind: tar.TypeReg, body: "data"}}))
	storage := t.TempDir()
	cleanupMaterializedStorage(t, storage)
	var replacement string
	hooks := materializerHooks{afterStage: func(stagePath string) error {
		if err := os.Rename(stagePath, stagePath+"-displaced"); err != nil {
			return err
		}
		if err := os.Mkdir(stagePath, 0755); err != nil {
			return err
		}
		replacement = stagePath
		return os.WriteFile(filepath.Join(stagePath, "victim"), []byte("keep"), 0644)
	}}
	materializer := Materializer{Store: &strictMaterializerStore{want: ref, archive: canonical}, Canonicalizer: Canonicalizer{}, StoragePath: storage, MaxTreeBytes: 1 << 20, hooks: hooks}
	if err := materializer.Materialize(context.Background(), ref, "handle", "volume"); err == nil {
		t.Fatal("replacement stage directory was accepted")
	}
	content, err := os.ReadFile(filepath.Join(replacement, "victim"))
	if err != nil || string(content) != "keep" {
		t.Fatalf("cleanup mutated replacement directory: %q, %v", content, err)
	}
}

func TestMaterializerSealsExistingRootBeforeReceipt(t *testing.T) {
	ref, canonical := canonicalTreeFixture(t, testTreeArchive(t, []testTreeEntry{{name: "file", kind: tar.TypeReg, body: "data"}}))
	storage := t.TempDir()
	cleanupMaterializedStorage(t, storage)
	destination := filepath.Join(storage, "steps", "handle", "volume")
	if err := os.MkdirAll(destination, 0755); err != nil {
		t.Fatal(err)
	}
	observed := os.FileMode(0)
	hooks := materializerHooks{beforeReceipt: func() error {
		info, err := os.Stat(destination)
		if err == nil {
			observed = info.Mode().Perm()
		}
		return errors.New("stop before receipt")
	}}
	materializer := Materializer{Store: &strictMaterializerStore{want: ref, archive: canonical}, Canonicalizer: Canonicalizer{}, StoragePath: storage, MaxTreeBytes: 1 << 20, hooks: hooks}
	_ = materializer.Materialize(context.Background(), ref, "handle", "volume")
	if observed != 0555 {
		t.Fatalf("destination mode before receipt = %#o, want 0555", observed)
	}
	if _, err := os.Lstat(filepath.Join(destination, materializationReceiptName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pre-receipt failure left receipt: %v", err)
	}
}

func TestMaterializerRootChmodAndSyncFailuresPublishNoReceipt(t *testing.T) {
	ref, canonical := canonicalTreeFixture(t, testTreeArchive(t, []testTreeEntry{{name: "file", kind: tar.TypeReg, body: "data"}}))
	for _, test := range []struct {
		name  string
		hooks materializerHooks
	}{
		{name: "chmod", hooks: materializerHooks{beforeRootChmod: func() error { return errors.New("injected chmod") }}},
		{name: "fsync", hooks: materializerHooks{beforeRootSync: func() error { return errors.New("injected fsync") }}},
	} {
		t.Run(test.name, func(t *testing.T) {
			storage := t.TempDir()
			cleanupMaterializedStorage(t, storage)
			destination := filepath.Join(storage, "steps", "handle", "volume")
			if err := os.MkdirAll(destination, 0755); err != nil {
				t.Fatal(err)
			}
			materializer := Materializer{Store: &strictMaterializerStore{want: ref, archive: canonical}, Canonicalizer: Canonicalizer{}, StoragePath: storage, MaxTreeBytes: 1 << 20, hooks: test.hooks}
			if err := materializer.Materialize(context.Background(), ref, "handle", "volume"); err == nil {
				t.Fatal("injected root durability failure was ignored")
			}
			if _, err := os.Lstat(filepath.Join(destination, materializationReceiptName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("durability failure left receipt: %v", err)
			}
		})
	}
}

func TestMaterializerRechecksAuthorityChainAfterReceipt(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "absent"
		if existing {
			name = "existing"
		}
		t.Run(name, func(t *testing.T) {
			ref, canonical := canonicalTreeFixture(t, testTreeArchive(t, []testTreeEntry{{name: "file", kind: tar.TypeReg, body: "data"}}))
			storage := t.TempDir()
			cleanupMaterializedStorage(t, storage)
			handlePath := filepath.Join(storage, "steps", "handle")
			destination := filepath.Join(handlePath, "volume")
			if existing {
				if err := os.MkdirAll(destination, 0755); err != nil {
					t.Fatal(err)
				}
			}
			hooks := materializerHooks{afterReceipt: func() error {
				if err := os.Rename(handlePath, handlePath+"-displaced"); err != nil {
					return err
				}
				if err := os.Mkdir(handlePath, 0755); err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(handlePath, "victim"), []byte("keep"), 0644)
			}}
			if existing {
				hooks = withTestReceiptPrivilege(destination, hooks)
			}
			materializer := Materializer{Store: &strictMaterializerStore{want: ref, archive: canonical}, Canonicalizer: Canonicalizer{}, StoragePath: storage, MaxTreeBytes: 1 << 20, hooks: hooks}
			if err := materializer.Materialize(context.Background(), ref, "handle", "volume"); err == nil {
				t.Fatal("post-receipt ancestor swap was accepted")
			}
			content, err := os.ReadFile(filepath.Join(handlePath, "victim"))
			if err != nil || string(content) != "keep" {
				t.Fatalf("replacement authority mutated: %q, %v", content, err)
			}
		})
	}
}

func TestMaterializerRechecksDestinationNameAfterReceipt(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "absent"
		if existing {
			name = "existing"
		}
		t.Run(name, func(t *testing.T) {
			ref, canonical := canonicalTreeFixture(t, testTreeArchive(t, []testTreeEntry{{name: "file", kind: tar.TypeReg, body: "data"}}))
			storage := t.TempDir()
			cleanupMaterializedStorage(t, storage)
			destination := filepath.Join(storage, "steps", "handle", "volume")
			if existing {
				if err := os.MkdirAll(destination, 0755); err != nil {
					t.Fatal(err)
				}
			}
			hooks := materializerHooks{afterReceipt: func() error {
				if err := os.Chmod(destination, 0700); err != nil {
					return err
				}
				if err := os.Rename(destination, destination+"-displaced"); err != nil {
					return err
				}
				if err := os.Mkdir(destination, 0755); err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(destination, "victim"), []byte("keep"), 0644)
			}}
			if existing {
				hooks = withTestReceiptPrivilege(destination, hooks)
			}
			materializer := Materializer{Store: &strictMaterializerStore{want: ref, archive: canonical}, Canonicalizer: Canonicalizer{}, StoragePath: storage, MaxTreeBytes: 1 << 20, hooks: hooks}
			if err := materializer.Materialize(context.Background(), ref, "handle", "volume"); err == nil {
				t.Fatal("post-receipt destination swap was accepted")
			}
			content, err := os.ReadFile(filepath.Join(destination, "victim"))
			if err != nil || string(content) != "keep" {
				t.Fatalf("replacement destination mutated: %q, %v", content, err)
			}
		})
	}
}

func TestMaterializerRejectsParentAndDestinationSwapsWithoutMutatingReplacement(t *testing.T) {
	ref, canonical := canonicalTreeFixture(t, testTreeArchive(t, []testTreeEntry{{name: "file", kind: tar.TypeReg, body: "data"}}))
	for _, attack := range []string{"parent", "destination"} {
		t.Run(attack, func(t *testing.T) {
			storage := t.TempDir()
			cleanupMaterializedStorage(t, storage)
			destination := filepath.Join(storage, "steps", "handle", "volume")
			if attack == "destination" {
				if err := os.MkdirAll(destination, 0755); err != nil {
					t.Fatal(err)
				}
			}
			hooks := materializerHooks{}
			if attack == "parent" {
				hooks.beforePublish = func() error {
					handlePath := filepath.Dir(destination)
					if err := os.Rename(handlePath, handlePath+"-displaced"); err != nil {
						return err
					}
					if err := os.Mkdir(handlePath, 0755); err != nil {
						return err
					}
					return os.WriteFile(filepath.Join(handlePath, "victim"), []byte("keep"), 0644)
				}
			} else {
				hooks.afterDestinationOpen = func() error {
					if err := os.Rename(destination, destination+"-displaced"); err != nil {
						return err
					}
					if err := os.Mkdir(destination, 0755); err != nil {
						return err
					}
					return os.WriteFile(filepath.Join(destination, "victim"), []byte("keep"), 0644)
				}
			}
			materializer := Materializer{Store: &strictMaterializerStore{want: ref, archive: canonical}, Canonicalizer: Canonicalizer{}, StoragePath: storage, MaxTreeBytes: 1 << 20, hooks: hooks}
			if err := materializer.Materialize(context.Background(), ref, "handle", "volume"); err == nil {
				t.Fatal("accepted an inode swap")
			}
			victimPath := filepath.Join(destination, "victim")
			if attack == "parent" {
				victimPath = filepath.Join(filepath.Dir(destination), "victim")
			}
			victim, err := os.ReadFile(victimPath)
			if err != nil || string(victim) != "keep" {
				t.Fatalf("replacement victim changed: %q, %v", victim, err)
			}
		})
	}
}

type strictMaterializerStore struct {
	mu      sync.Mutex
	want    TreeRef
	archive []byte
	opens   int
	openErr error
}

func (store *strictMaterializerStore) OpenTree(_ context.Context, ref TreeRef, maxBytes int64) (io.ReadCloser, TreeAttributes, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if ref != store.want {
		return nil, TreeAttributes{}, errors.New("wrong tree ref")
	}
	if store.openErr != nil {
		return nil, TreeAttributes{}, store.openErr
	}
	if maxBytes <= 0 || int64(len(store.archive)) > maxBytes {
		return nil, TreeAttributes{}, ErrLimitExceeded
	}
	store.opens++
	return io.NopCloser(bytes.NewReader(store.archive)), TreeAttributes{Ref: ref, StoredBytes: int64(len(store.archive))}, nil
}

func (*strictMaterializerStore) EnsureTree(context.Context, Scope, Digest, io.Reader, int64) (TreeAttributes, bool, error) {
	panic("unexpected EnsureTree")
}
func (*strictMaterializerStore) InspectTree(context.Context, Scope, Digest, int64) (TreeAttributes, error) {
	panic("unexpected InspectTree")
}
func (*strictMaterializerStore) DeleteTree(context.Context, TreeRef) error {
	panic("unexpected DeleteTree")
}

type testTreeEntry struct {
	name string
	mode int64
	kind byte
	body string
	link string
}

func testTreeArchive(t *testing.T, entries []testTreeEntry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for _, entry := range entries {
		mode := entry.mode
		if mode == 0 {
			mode = 0644
		}
		header := &tar.Header{Name: entry.name, Mode: mode, Typeflag: entry.kind, Linkname: entry.link, ModTime: time.Unix(0, 0), Size: int64(len(entry.body))}
		if entry.kind != tar.TypeReg && entry.kind != tar.TypeRegA {
			header.Size = 0
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if header.Size > 0 {
			if _, err := writer.Write([]byte(entry.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func canonicalTreeFixture(t *testing.T, raw []byte) (TreeRef, []byte) {
	t.Helper()
	tree, err := (Canonicalizer{}).Capture(context.Background(), bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Close()
	archive, err := os.ReadFile(tree.ArchivePath)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := NewTreeRef("builds", tree.Digest, 17)
	if err != nil {
		t.Fatal(err)
	}
	return ref, archive
}

func assertMaterializedMode(t *testing.T, name string, want os.FileMode) {
	t.Helper()
	info, err := os.Lstat(name)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %#o, want %#o", name, got, want)
	}
}

func cleanupMaterializedStorage(t *testing.T, storage string) {
	t.Helper()
	t.Cleanup(func() {
		_ = filepath.Walk(storage, func(name string, info os.FileInfo, err error) error {
			if err == nil && info.IsDir() {
				_ = os.Chmod(name, 0700)
			}
			return nil
		})
	})
}

func withTestReceiptPrivilege(destination string, hooks materializerHooks) materializerHooks {
	beforeReceiptRename := hooks.beforeReceiptRename
	hooks.beforeReceiptRename = func() error {
		if beforeReceiptRename != nil {
			if err := beforeReceiptRename(); err != nil {
				return err
			}
		}
		return os.Chmod(destination, 0700)
	}
	afterReceipt := hooks.afterReceipt
	hooks.afterReceipt = func() error {
		if err := os.Chmod(destination, 0555); err != nil {
			return err
		}
		if err := syncDirectoryPath(destination); err != nil {
			return err
		}
		if afterReceipt != nil {
			return afterReceipt()
		}
		return nil
	}
	return hooks
}

func syncDirectoryPath(name string) error {
	directory, err := os.Open(name)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
