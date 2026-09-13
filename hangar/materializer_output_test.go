package hangar

// The output read profile, and the strict-input path it does not touch.
//
// The byte-identical claim is made two ways here, on purpose. Behaviourally:
// the same tree staged through Materialize and through MaterializeManaged
// produces destinations that compare EQUAL, entry for entry, mode for mode,
// byte for byte. Structurally: Materialize's own body mentions no profile, and
// a guard reads the source to say so -- because the behavioural half proves the
// two agree on one input, and the requirement is that one path was not changed.

import (
	"archive/tar"
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// admittingProfile is a profile that says yes and records what happened to it.
type admittingProfile struct {
	work     time.Duration
	refuse   error
	admits   int
	renews   int
	releases int
	staged   error
	ref      TreeRef
	handle   string
	volume   string
}

func (profile *admittingProfile) Admit(_ context.Context, ref TreeRef, handle, volume string) (time.Duration, error) {
	profile.admits++
	profile.ref, profile.handle, profile.volume = ref, handle, volume
	if profile.refuse != nil {
		return 0, profile.refuse
	}

	return profile.work, nil
}

func (profile *admittingProfile) Renew(context.Context) error {
	profile.renews++

	return nil
}

func (profile *admittingProfile) Release(_ context.Context, staged error) error {
	profile.releases++
	profile.staged = staged

	return nil
}

func managedTreeFixture(t *testing.T) (TreeRef, []byte) {
	t.Helper()

	raw := testTreeArchive(t, []testTreeEntry{
		{name: "dir", mode: 0750, kind: tar.TypeDir},
		{name: "dir/file", mode: 0766, kind: tar.TypeReg, body: "durable"},
		{name: "link", mode: 0777, kind: tar.TypeSymlink, link: "dir/file"},
	})

	return canonicalTreeFixture(t, raw)
}

func TestAManagedMaterializationStagesTheSameBytesAsAStrictOne(t *testing.T) {
	ref, canonical := managedTreeFixture(t)

	strictStorage := t.TempDir()
	cleanupMaterializedStorage(t, strictStorage)
	strict := Materializer{
		Store:         &strictMaterializerStore{want: ref, archive: canonical},
		Canonicalizer: Canonicalizer{}, StoragePath: strictStorage, MaxTreeBytes: 1 << 20,
	}
	if err := strict.Materialize(context.Background(), ref, "handle", "volume"); err != nil {
		t.Fatalf("the strict-input path: %v", err)
	}

	managedStorage := t.TempDir()
	cleanupMaterializedStorage(t, managedStorage)
	managed := Materializer{
		Store:         &strictMaterializerStore{want: ref, archive: canonical},
		Canonicalizer: Canonicalizer{}, StoragePath: managedStorage, MaxTreeBytes: 1 << 20,
	}
	profile := &admittingProfile{work: time.Minute}
	if err := managed.MaterializeManaged(context.Background(), ref, "handle", "volume",
		profile); err != nil {
		t.Fatalf("the managed-output path: %v", err)
	}

	assertTreesIdentical(t,
		filepath.Join(strictStorage, "steps", "handle", "volume"),
		filepath.Join(managedStorage, "steps", "handle", "volume"))

	if profile.admits != 1 || profile.releases != 1 {
		t.Errorf("the profile was admitted %d times and released %d", profile.admits,
			profile.releases)
	}
	if profile.staged != nil {
		t.Errorf("a successful staging was released with %v", profile.staged)
	}
	if profile.ref != ref || profile.handle != "handle" || profile.volume != "volume" {
		t.Errorf("the profile was admitted for %+v %s/%s", profile.ref, profile.handle,
			profile.volume)
	}
}

// A profile that refuses opens nothing.
func TestARefusedProfileOpensNoObject(t *testing.T) {
	ref, canonical := managedTreeFixture(t)
	store := &strictMaterializerStore{want: ref, archive: canonical}
	storage := t.TempDir()
	cleanupMaterializedStorage(t, storage)
	materializer := Materializer{
		Store: store, Canonicalizer: Canonicalizer{}, StoragePath: storage, MaxTreeBytes: 1 << 20,
	}

	refused := errors.New("the lease is gone")
	profile := &admittingProfile{work: time.Minute, refuse: refused}

	if err := materializer.MaterializeManaged(context.Background(), ref, "handle", "volume",
		profile); !errors.Is(err, refused) {
		t.Fatalf("a refused profile answered %v", err)
	}
	if store.opens != 0 {
		t.Errorf("a refused read opened the object %d times", store.opens)
	}
	if profile.releases != 0 {
		t.Errorf("a read that was never admitted released %d times", profile.releases)
	}
	if _, err := os.Stat(filepath.Join(storage, "steps", "handle", "volume")); !os.IsNotExist(err) {
		t.Errorf("a refused read left a destination behind: %v", err)
	}
}

// No profile at all is not "unmanaged", it is unauthorized.
func TestAManagedMaterializationWithoutAProfileIsUnauthorized(t *testing.T) {
	ref, canonical := managedTreeFixture(t)
	materializer := Materializer{
		Store:         &strictMaterializerStore{want: ref, archive: canonical},
		Canonicalizer: Canonicalizer{}, StoragePath: t.TempDir(), MaxTreeBytes: 1 << 20,
	}

	if err := materializer.MaterializeManaged(context.Background(), ref, "handle", "volume",
		nil); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("a managed read with no profile answered %v", err)
	}
}

// A profile that admits no working time admits nothing.
func TestAProfileThatAdmitsNoTimeStagesNothing(t *testing.T) {
	ref, canonical := managedTreeFixture(t)
	store := &strictMaterializerStore{want: ref, archive: canonical}
	materializer := Materializer{
		Store: store, Canonicalizer: Canonicalizer{}, StoragePath: t.TempDir(),
		MaxTreeBytes: 1 << 20,
	}

	if err := materializer.MaterializeManaged(context.Background(), ref, "handle", "volume",
		&admittingProfile{work: 0}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("a profile that admitted no time answered %v", err)
	}
	if store.opens != 0 {
		t.Errorf("a read with no admitted time opened the object %d times", store.opens)
	}
}

// The release runs on the failure path too: protection nobody is using has to
// be given back.
func TestAFailedManagedStagingStillReleasesItsProfile(t *testing.T) {
	ref, canonical := managedTreeFixture(t)
	wrong := ref
	wrong.Digest = Digest("sha256:" + strings.Repeat("b", 64))

	materializer := Materializer{
		Store:         &strictMaterializerStore{want: wrong, archive: canonical},
		Canonicalizer: Canonicalizer{}, StoragePath: t.TempDir(), MaxTreeBytes: 1 << 20,
	}
	profile := &admittingProfile{work: time.Minute}

	if err := materializer.MaterializeManaged(context.Background(), wrong, "handle", "volume",
		profile); err == nil {
		t.Fatal("a staging that could not verify its tree reported success")
	}
	if profile.releases != 1 {
		t.Errorf("a failed staging released %d times", profile.releases)
	}
	if profile.staged == nil {
		t.Error("the release was told the staging succeeded")
	}
}

// The structural half of the byte-identical claim.
func TestTheStrictInputMaterializerNamesNoOutputProfile(t *testing.T) {
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, "materializer.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing materializer.go: %v", err)
	}

	scanned := 0
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Body == nil {
			continue
		}
		scanned++

		ast.Inspect(function.Body, func(node ast.Node) bool {
			identifier, ok := node.(*ast.Ident)
			if !ok {
				return true
			}
			for _, forbidden := range []string{"OutputReadProfile", "MaterializeManaged", "profile"} {
				if identifier.Name == forbidden {
					t.Errorf("hangar/materializer.go: %s names %q.\n\nThe strict-input path is "+
						"extended ONLY through the output read profile, which means a second "+
						"entry point rather than a branch. A managed-output concept inside this "+
						"file is a change to behaviour ordinary inputs already depend on.",
						function.Name.Name, forbidden)
				}
			}

			return true
		})
	}
	if scanned == 0 {
		t.Fatal("this guard parsed materializer.go and found no functions, so it is passing " +
			"vacuously")
	}
}

// assertTreesIdentical compares two staged destinations exactly: the same
// entries, the same modes, the same bytes, the same link targets.
func assertTreesIdentical(t *testing.T, left, right string) {
	t.Helper()

	describe := func(root string) map[string]string {
		described := map[string]string{}
		if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			switch {
			case entry.Type()&os.ModeSymlink != 0:
				target, err := os.Readlink(path)
				if err != nil {
					return err
				}
				described[relative] = "symlink " + target
			case entry.IsDir():
				described[relative] = "dir " + info.Mode().Perm().String()
			default:
				body, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				described[relative] = "file " + info.Mode().Perm().String() + " " + string(body)
			}

			return nil
		}); err != nil {
			t.Fatalf("walking %s: %v", root, err)
		}

		return described
	}

	strict, managed := describe(left), describe(right)
	if len(strict) == 0 {
		t.Fatal("the strict-input destination is empty, so this comparison says nothing")
	}
	for name, want := range strict {
		if got, ok := managed[name]; !ok || got != want {
			t.Errorf("%s: strict %q, managed %q", name, want, got)
		}
	}
	for name := range managed {
		if _, ok := strict[name]; !ok {
			t.Errorf("%s exists only in the managed destination", name)
		}
	}
}
