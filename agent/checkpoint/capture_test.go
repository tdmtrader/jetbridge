package checkpoint

import (
	"archive/tar"
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/hangar"
	"golang.org/x/sys/unix"
)

func TestArchiveRequestValidateRejectsAmbiguousOrEscapingDeclaredRoots(t *testing.T) {
	t.Parallel()

	valid := ArchiveRequest{ContainerHandle: "agent-42", WorkspaceRoots: []string{"workspace"}, SessionRoots: []string{".concourse/session"}, MaxBytes: 1024}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid request: %v", err)
	}

	for name, request := range map[string]ArchiveRequest{
		"absolute workspace root": {ContainerHandle: "agent-42", WorkspaceRoots: []string{"/workspace"}, SessionRoots: []string{".concourse/session"}, MaxBytes: 1024},
		"parent workspace root":   {ContainerHandle: "agent-42", WorkspaceRoots: []string{"../workspace"}, SessionRoots: []string{".concourse/session"}, MaxBytes: 1024},
		"duplicate roots":         {ContainerHandle: "agent-42", WorkspaceRoots: []string{"workspace", "workspace"}, SessionRoots: []string{".concourse/session"}, MaxBytes: 1024},
		"overlapping roots":       {ContainerHandle: "agent-42", WorkspaceRoots: []string{"workspace", "workspace/cache"}, SessionRoots: []string{".concourse/session"}, MaxBytes: 1024},
		"no session root":         {ContainerHandle: "agent-42", WorkspaceRoots: []string{"workspace"}, MaxBytes: 1024},
		"uncapped bytes":          {ContainerHandle: "agent-42", WorkspaceRoots: []string{"workspace"}, SessionRoots: []string{".concourse/session"}, MaxBytes: 0},
	} {
		t.Run(name, func(t *testing.T) {
			if err := request.Validate(); err == nil {
				t.Fatal("Validate() succeeded for an unsafe archive declaration")
			}
		})
	}
}

func TestCaptureArchiveProducesDeterministicSeparateWorkspaceAndSessionTrees(t *testing.T) {
	t.Parallel()
	containerRoot := t.TempDir()
	writeArchiveFile(t, containerRoot, "workspace/z.txt", "z")
	writeArchiveFile(t, containerRoot, "workspace/bin/run", "run")
	writeArchiveFile(t, containerRoot, ".concourse/session/cursor", "42")
	request := ArchiveRequest{ContainerHandle: "agent-42", WorkspaceRoots: []string{"workspace"}, SessionRoots: []string{".concourse/session"}, MaxBytes: 1024}
	source := ArchiveSource{ContainerRoot: containerRoot, MaxBytes: 1024, MaxEntries: 16, TempDir: t.TempDir()}
	first, err := CaptureArchive(context.Background(), request, source)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if got, want := archiveMemberNames(t, first), []string{"session", "session/cursor", "workspace", "workspace/bin", "workspace/bin/run", "workspace/z.txt"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("archive members = %q, want %q", got, want)
	}
	if first.Prepared.Files != len(archiveMemberNames(t, first)) {
		t.Fatalf("prepared file count = %d", first.Prepared.Files)
	}
	if got := archiveMemberHeader(t, first, "workspace/bin/run"); got.ModTime.Unix() != 0 || got.Mode != 0o644 {
		t.Fatalf("canonical header = %#v, want epoch timestamp and source permissions", got)
	}
	if err := os.Chtimes(filepath.Join(containerRoot, "workspace", "z.txt"), first.PreparedAt.AddDate(1, 0, 0), first.PreparedAt.AddDate(1, 0, 0)); err != nil {
		t.Fatal(err)
	}
	second, err := CaptureArchive(context.Background(), request, source)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if first.Prepared.Digest != second.Prepared.Digest {
		t.Fatalf("deterministic digest changed: %q != %q", first.Prepared.Digest, second.Prepared.Digest)
	}
}

func TestCaptureArchiveFailsClosedForUnsafeSourceEntries(t *testing.T) {
	t.Parallel()
	for name, setup := range map[string]func(t *testing.T, root string) ArchiveSource{
		"configured secret root": func(t *testing.T, root string) ArchiveSource {
			writeArchiveFile(t, root, "workspace/secret/token", "secret")
			writeArchiveFile(t, root, ".concourse/session/cursor", "42")
			return ArchiveSource{ContainerRoot: root, ExcludedRoots: []string{"workspace/secret"}, MaxBytes: 1024, MaxEntries: 16, TempDir: t.TempDir()}
		},
		"symlink escaping source root": func(t *testing.T, root string) ArchiveSource {
			writeArchiveFile(t, root, ".concourse/session/cursor", "42")
			if err := os.MkdirAll(filepath.Join(root, "workspace"), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("../../outside", filepath.Join(root, "workspace", "escape")); err != nil {
				t.Fatal(err)
			}
			return ArchiveSource{ContainerRoot: root, MaxBytes: 1024, MaxEntries: 16, TempDir: t.TempDir()}
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			source := setup(t, root)
			_, err := CaptureArchive(context.Background(), ArchiveRequest{ContainerHandle: "agent-42", WorkspaceRoots: []string{"workspace"}, SessionRoots: []string{".concourse/session"}, MaxBytes: 1024}, source)
			if err == nil {
				t.Fatal("CaptureArchive() succeeded for unsafe source")
			}
		})
	}
}

func TestCaptureArchiveRejectsSpecialAndPrivilegedFiles(t *testing.T) {
	for name, setup := range map[string]func(t *testing.T, root string){
		"named pipe": func(t *testing.T, root string) {
			writeArchiveFile(t, root, ".concourse/session/cursor", "42")
			if err := os.MkdirAll(filepath.Join(root, "workspace"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := unix.Mkfifo(filepath.Join(root, "workspace", "pipe"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"setuid file": func(t *testing.T, root string) {
			writeArchiveFile(t, root, "workspace/run", "run")
			writeArchiveFile(t, root, ".concourse/session/cursor", "42")
			if err := os.Chmod(filepath.Join(root, "workspace", "run"), 0o4755); err != nil {
				t.Fatal(err)
			}
			info, err := os.Lstat(filepath.Join(root, "workspace", "run"))
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode()&os.ModeSetuid == 0 {
				t.Skip("temporary filesystem does not preserve setuid metadata")
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			setup(t, root)
			_, err := CaptureArchive(context.Background(), ArchiveRequest{ContainerHandle: "agent-42", WorkspaceRoots: []string{"workspace"}, SessionRoots: []string{".concourse/session"}, MaxBytes: 1024}, ArchiveSource{ContainerRoot: root, MaxBytes: 1024, MaxEntries: 16, TempDir: t.TempDir()})
			if err == nil {
				t.Fatal("CaptureArchive() accepted unsafe source entry")
			}
		})
	}
}

func TestCaptureArchiveEnforcesServerOwnedEntryAndByteLimits(t *testing.T) {
	t.Parallel()
	containerRoot := t.TempDir()
	writeArchiveFile(t, containerRoot, "workspace/a", "aa")
	writeArchiveFile(t, containerRoot, ".concourse/session/cursor", "42")
	_, err := CaptureArchive(context.Background(), ArchiveRequest{ContainerHandle: "agent-42", WorkspaceRoots: []string{"workspace"}, SessionRoots: []string{".concourse/session"}, MaxBytes: 1}, ArchiveSource{ContainerRoot: containerRoot, MaxBytes: 1024, MaxEntries: 2, TempDir: t.TempDir()})
	if err == nil || (!strings.Contains(err.Error(), "limit") && !strings.Contains(err.Error(), "maximum")) {
		t.Fatalf("CaptureArchive() error = %v, want limit rejection", err)
	}
}

func writeArchiveFile(t *testing.T, root, relative, contents string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(contents), 0644); err != nil {
		t.Fatal(err)
	}
}

func archiveMemberNames(t *testing.T, archive *CapturedArchive) []string {
	t.Helper()
	file, err := archive.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader := tar.NewReader(file)
	var names []string
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return names
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, strings.TrimSuffix(header.Name, "/"))
	}
}

func archiveMemberHeader(t *testing.T, archive *CapturedArchive, want string) tar.Header {
	t.Helper()
	file, err := archive.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader := tar.NewReader(file)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			t.Fatalf("archive member %q not found", want)
		}
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSuffix(header.Name, "/") == want {
			return *header
		}
	}
}

func TestPreparedArchiveValidateBindsACompleteCombinedArchive(t *testing.T) {
	t.Parallel()
	valid := PreparedArchive{Handle: "prepared-abc", Digest: hangar.Digest("sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"), Files: 2, Bytes: 1024}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid prepared archive: %v", err)
	}
	for name, prepared := range map[string]PreparedArchive{"empty handle": {Digest: valid.Digest, Files: 2, Bytes: 1024}, "bad digest": {Handle: valid.Handle, Digest: "sha256:not-a-digest", Files: 2, Bytes: 1024}, "empty archive": {Handle: valid.Handle, Digest: valid.Digest, Files: 0, Bytes: 1024}, "zero bytes": {Handle: valid.Handle, Digest: valid.Digest, Files: 2, Bytes: 0}} {
		t.Run(name, func(t *testing.T) {
			if err := prepared.Validate(); err == nil {
				t.Fatal("Validate() succeeded for unusable archive")
			}
		})
	}
}
