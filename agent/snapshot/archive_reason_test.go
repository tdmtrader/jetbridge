package snapshot

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

// Every archive rejection reason must be reachable by driving the REAL
// canonicalizer over a real tar. A reason nothing can produce is worse than no
// reason: it is a promise in the enum that the code never keeps.
func TestEveryArchiveRejectionReasonIsReachableThroughCapture(t *testing.T) {
	t.Parallel()

	overLongName := strings.Repeat("p", int(MaxSnapshotPathBytes)+1)

	tests := []struct {
		name          string
		canonicalizer Canonicalizer
		raw           func(*testing.T) []byte
		reason        ValidationFailureReason
		entry         string
		entryCheck    func(*testing.T, string)
	}{
		{
			name: "a dot segment is not a canonical path",
			raw: func(t *testing.T) []byte {
				return makeTar(t, []tarEntry{{name: "./file", typeflag: tar.TypeReg, content: "x"}})
			},
			reason: ArchivePathNotCanonical,
			entry:  "./file",
		},
		{
			name:   "a trailing separator is not a canonical path",
			raw:    func(t *testing.T) []byte { return rawTarHeader("dir/", tar.TypeReg, 0644, nil) },
			reason: ArchivePathNotCanonical,
			entry:  "dir/",
		},
		{
			name: "an absolute path is not a canonical path",
			raw: func(t *testing.T) []byte {
				return makeTar(t, []tarEntry{{name: "/etc/shadow", typeflag: tar.TypeReg, content: "x"}})
			},
			reason: ArchivePathNotCanonical,
			entry:  "/etc/shadow",
		},
		{
			name:   "an over-long path is its own reason and its entry is bounded",
			raw:    func(t *testing.T) []byte { return makeTar(t, []tarEntry{{name: overLongName, typeflag: tar.TypeReg}}) },
			reason: ArchivePathTooLong,
			entryCheck: func(t *testing.T, entry string) {
				if len(entry) > MaxPublicEntryBytes {
					t.Fatalf("entry is %d bytes, want at most %d", len(entry), MaxPublicEntryBytes)
				}
				if !strings.HasSuffix(entry, PublicEntryTruncationMarker) {
					t.Fatalf("entry %q was truncated without a visible marker", entry)
				}
				if !strings.HasPrefix(overLongName, strings.TrimSuffix(entry, PublicEntryTruncationMarker)) {
					t.Fatalf("entry %q is not a prefix of the rejected path", entry)
				}
			},
		},
		{
			name: "one canonical path declared twice",
			raw: func(t *testing.T) []byte {
				return makeTar(t, []tarEntry{
					{name: "dir/file", typeflag: tar.TypeReg, content: "one"},
					{name: "dir/file", typeflag: tar.TypeReg, content: "two"},
				})
			},
			reason: ArchivePathDuplicate,
			entry:  "dir/file",
		},
		{
			name: "a path whose parent is a symlink",
			raw: func(t *testing.T) []byte {
				return makeTar(t, []tarEntry{
					{name: "parent", typeflag: tar.TypeSymlink, linkname: "safe"},
					{name: "parent/file", typeflag: tar.TypeReg, content: "owned"},
				})
			},
			reason: ArchivePathParentInvalid,
			entry:  "parent/file",
		},
		{
			name: "two distinct POSIX names the host filesystem aliases",
			canonicalizer: Canonicalizer{
				beforeMaterialize: func(root *os.Root, name string) error {
					if name == "collision" {
						return root.WriteFile(name, []byte("host alias"), 0600)
					}
					return nil
				},
			},
			raw: func(t *testing.T) []byte {
				return makeTar(t, []tarEntry{{name: "collision", typeflag: tar.TypeReg, content: "archive"}})
			},
			reason: ArchivePathCollides,
			entry:  "collision",
		},
		{
			name:   "a hard link is not a supported entry type",
			raw:    func(t *testing.T) []byte { return rawTarHeader2("link", tar.TypeLink, 0644, "target", nil) },
			reason: ArchiveEntryTypeUnsupported,
			entry:  "link",
		},
		{
			name: "a setuid bit is unsafe metadata",
			raw: func(t *testing.T) []byte {
				return makeTar(t, []tarEntry{{name: "helper", typeflag: tar.TypeReg, mode: 04755, content: "x"}})
			},
			reason: ArchiveEntryMetadataUnsupported,
			entry:  "helper",
		},
		{
			name:   "a directory entry that declares content",
			raw:    func(t *testing.T) []byte { return rawTarHeader2("dir", tar.TypeDir, 0755, "", []byte("sneaky")) },
			reason: ArchiveEntrySizeInvalid,
			entry:  "dir",
		},
		{
			name: "an absolute symlink target",
			raw: func(t *testing.T) []byte {
				return makeTar(t, []tarEntry{{name: "link", typeflag: tar.TypeSymlink, linkname: "/etc/shadow"}})
			},
			reason: ArchiveSymlinkTargetInvalid,
			entry:  "link",
		},
		{
			name: "a symlink target that escapes the archive root",
			raw: func(t *testing.T) []byte {
				return makeTar(t, []tarEntry{{name: "link", typeflag: tar.TypeSymlink, linkname: "../outside"}})
			},
			reason: ArchiveSymlinkEscapesRoot,
			entry:  "link",
		},
		{
			name:   "bytes that are not a tar at all",
			raw:    func(t *testing.T) []byte { return bytes.Repeat([]byte("not a tar header, not even close."), 32) },
			reason: ArchiveStreamUnreadable,
			entry:  "",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			canonicalizer := test.canonicalizer
			canonicalizer.TempDir = t.TempDir()
			tree, err := canonicalizer.Capture(context.Background(), bytes.NewReader(test.raw(t)))
			if tree != nil {
				defer tree.Close()
			}
			if err == nil {
				t.Fatal("Capture() accepted an archive the canonicalizer must reject")
			}
			var public *PublicValidationFailure
			if !errors.As(err, &public) {
				t.Fatalf("rejection carries no public reason: %v", err)
			}
			if public.Reason() != test.reason {
				t.Fatalf("reason = %q, want %q (cause: %v)", public.Reason(), test.reason, err)
			}
			if test.entryCheck != nil {
				test.entryCheck(t, public.Entry())
			} else if public.Entry() != test.entry {
				t.Fatalf("entry = %q, want %q", public.Entry(), test.entry)
			}
		})
	}
}

// rawTarHeader2 is rawTarEntry plus the two-block terminator, for entries
// archive/tar's writer refuses to produce.
func rawTarHeader2(name string, typeflag byte, mode int64, linkname string, content []byte) []byte {
	return append(rawTarEntry(name, typeflag, mode, linkname, content), make([]byte, 1024)...)
}
