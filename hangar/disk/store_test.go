package disk

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/objectstore"
	bolt "go.etcd.io/bbolt"
)

func TestRecoveryPreservesBytesWhenReverseIndexIsCorrupt(t *testing.T) {
	for _, fault := range []string{"missing", "wrong-owner"} {
		t.Run(fault, func(t *testing.T) {
			s, root := testStore(t)
			a := create(t, s, "tree", "committed")
			r, err := s.lookup([]byte("outputs\x00tree"), a.Generation)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.db.Update(func(tx *bolt.Tx) error {
				if fault == "missing" {
					return tx.Bucket(blobOwners).Delete([]byte(r.Blob))
				}
				return tx.Bucket(blobOwners).Put([]byte(r.Blob), []byte("outputs\x00other"))
			}); err != nil {
				t.Fatal(err)
			}
			orphan := filepath.Join(root, "blobs", "blob-orphan")
			if err := os.WriteFile(orphan, []byte("uncommitted"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(root, "test-store", 1024)
			if reopened != nil {
				_ = reopened.Close()
			}
			if !errors.Is(err, hangar.ErrCorrupt) {
				t.Fatalf("reopen: %v", err)
			}
			for path, want := range map[string]string{filepath.Join(root, "blobs", r.Blob): "committed", orphan: "uncommitted"} {
				got, err := os.ReadFile(path)
				if err != nil || string(got) != want {
					t.Fatalf("recovery modified %s: %q, %v", path, got, err)
				}
			}
		})
	}
}

func testStore(t *testing.T) (*Store, string) {
	t.Helper()
	root := t.TempDir()
	if err := Initialize(root, "test-store"); err != nil {
		t.Fatal(err)
	}
	s, err := Open(root, "test-store", 1024)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, root
}
func create(t *testing.T, s *Store, key, body string) objectstore.Attrs {
	t.Helper()
	a, err := s.CreateAbsent(context.Background(), "outputs", key, map[string]string{"owner": "one"}, bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func TestCreateRaceAndStaleDeleteAcrossRestart(t *testing.T) {
	s, root := testStore(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	results := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.CreateAbsent(ctx, "outputs", "same", nil, bytes.NewBufferString("hello"))
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	created := 0
	for err := range results {
		if err == nil {
			created++
		} else if !errors.Is(err, objectstore.ErrPreconditionFailed) {
			t.Fatal(err)
		}
	}
	if created != 1 {
		t.Fatalf("created %d", created)
	}
	a, err := s.StatCurrent(ctx, "outputs", "same")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteExact(ctx, "outputs", "same", a.Generation); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(root, "test-store", 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	b := create(t, s, "same", "replacement")
	if b.Generation <= a.Generation {
		t.Fatal("generation reused")
	}
	if err := s.DeleteExact(ctx, "outputs", "same", a.Generation); !errors.Is(err, objectstore.ErrPreconditionFailed) {
		t.Fatalf("stale delete: %v", err)
	}
	if _, err := s.OpenExact(ctx, "outputs", "same", a.Generation); !errors.Is(err, objectstore.ErrNotFound) {
		t.Fatalf("stale read: %v", err)
	}
	r, err := s.OpenExact(ctx, "outputs", "same", b.Generation)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	body, err := io.ReadAll(r)
	if err != nil || string(body) != "replacement" {
		t.Fatalf("read %q: %v", body, err)
	}
}
func TestDiskIdentityLimitsAndCorruption(t *testing.T) {
	s, root := testStore(t)
	ctx := context.Background()
	if _, err := Open(root, "wrong", 1024); err == nil {
		t.Fatal("second owner admitted")
	}
	if _, err := s.CreateAbsent(ctx, "outputs", "large", nil, bytes.NewReader(make([]byte, 1025))); !errors.Is(err, hangar.ErrLimitExceeded) {
		t.Fatal(err)
	}
	if _, err := s.StatCurrent(ctx, "outputs", "large"); !errors.Is(err, objectstore.ErrNotFound) {
		t.Fatal(err)
	}
	a := create(t, s, "tree", "abc")
	// Same-size corruption must fail through io.Copy too (no promoted File.WriteTo).
	r, err := s.lookup([]byte("outputs\x00tree"), a.Generation)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "blobs", r.Blob), []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	body, err := s.OpenExact(ctx, "outputs", "tree", a.Generation)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(io.Discard, body)
	_ = body.Close()
	if !errors.Is(err, hangar.ErrCorrupt) {
		t.Fatalf("corruption: %v", err)
	}
	_ = s.Close()
	if _, err := Open(root, "wrong", 1024); !errors.Is(err, hangar.ErrConflict) {
		t.Fatalf("wrong identity: %v", err)
	}
	if err := Initialize(root, "test-store"); err == nil {
		t.Fatal("initialized occupied disk")
	}
	if _, err := Open(t.TempDir(), "test-store", 1024); err == nil {
		t.Fatal("implicitly initialized empty disk")
	}
}
func TestListCursorIncludesRecreatedKey(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	a := create(t, s, "a", "a")
	create(t, s, "b", "b")
	p, err := s.List(ctx, "outputs", objectstore.ListRequest{PageSize: 1})
	if err != nil || p.Done || p.LastKey != "a" {
		t.Fatalf("first page %+v %v", p, err)
	}
	if err := s.DeleteExact(ctx, "outputs", "a", a.Generation); err != nil {
		t.Fatal(err)
	}
	b := create(t, s, "a", "a2")
	p, err = s.List(ctx, "outputs", objectstore.ListRequest{PageSize: 1, After: "a", AfterGeneration: a.Generation})
	if err != nil || len(p.Objects) != 1 || p.Objects[0].Generation != b.Generation {
		t.Fatalf("recreated page %+v %v", p, err)
	}
	p, err = s.List(ctx, "outputs", objectstore.ListRequest{PageSize: 1, After: "a", AfterGeneration: b.Generation})
	if err != nil || !p.Done || p.LastKey != "b" {
		t.Fatalf("last page %+v %v", p, err)
	}
}
func TestProcessCrashRecovery(t *testing.T) {
	if root := os.Getenv("HANGAR_DISK_CRASH_ROOT"); root != "" {
		s, err := Open(root, "test-store", 1024)
		if err != nil {
			t.Fatal(err)
		}
		s.checkpoint = func(name string) {
			if name == os.Getenv("HANGAR_DISK_CRASH_AT") {
				os.Exit(71)
			}
		}
		if os.Getenv("HANGAR_DISK_CRASH_DELETE") == "1" {
			err = s.DeleteExact(context.Background(), "outputs", "tree", 1)
		} else {
			_, err = s.CreateAbsent(context.Background(), "outputs", "tree", nil, bytes.NewBufferString("durable"))
		}
		t.Fatalf("checkpoint not reached: %v", err)
	}
	for _, point := range []string{"blob-durable", "object-committed", "delete-recorded", "blob-removed", "delete-committed"} {
		t.Run(point, func(t *testing.T) {
			s, root := testStore(t)
			deleting := point != "blob-durable" && point != "object-committed"
			if deleting {
				create(t, s, "tree", "durable")
			}
			_ = s.Close()
			cmd := exec.Command(os.Args[0], "-test.run=^TestProcessCrashRecovery$")
			cmd.Env = append(os.Environ(), "HANGAR_DISK_CRASH_ROOT="+root, "HANGAR_DISK_CRASH_AT="+point)
			if deleting {
				cmd.Env = append(cmd.Env, "HANGAR_DISK_CRASH_DELETE=1")
			}
			out, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 71 {
				t.Fatalf("child: %v %s", err, out)
			}
			s, err = Open(root, "test-store", 1024)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			a, err := s.StatCurrent(context.Background(), "outputs", "tree")
			if point == "object-committed" {
				if err != nil || a.Size != 7 {
					t.Fatalf("lost commit: %+v %v", a, err)
				}
			} else {
				if !errors.Is(err, objectstore.ErrNotFound) {
					t.Fatalf("partial object visible: %v", err)
				}
				entries, err := os.ReadDir(filepath.Join(root, "blobs"))
				if err != nil || len(entries) != 0 {
					t.Fatalf("leftovers %v %v", entries, err)
				}
			}
		})
	}
}
