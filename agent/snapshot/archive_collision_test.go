package snapshot

import (
	"archive/tar"
	"bytes"
	"testing"
	"time"
)

// TestCanonicalCaptureDistinguishesNearMissTrees is the anti-collision table:
// the statement of what a snapshot digest MUST be able to tell apart. Every
// pair below is a tree an attacker or a bug would like to substitute for its
// neighbour, and every one of them is a shape the length-framed canonical tar
// encoding is supposed to make unconfusable. "Structurally impossible" was the
// argument; this is the assertion.
//
// Its companion, TestCanonicalCaptureIdentityBoundaryIsExecBitOnly, states the
// other half — what deliberately does NOT differ. Read them together; either one
// alone is half a specification.
func TestCanonicalCaptureDistinguishesNearMissTrees(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		why   string
		left  []tarEntry
		right []tarEntry
	}{
		{
			name: "empty file versus absent file",
			why:  "a zero-byte file is a fact about the tree, not the absence of one",
			left: []tarEntry{
				{name: "keep", typeflag: tar.TypeReg, content: "x"},
				{name: "empty", typeflag: tar.TypeReg, content: ""},
			},
			right: []tarEntry{{name: "keep", typeflag: tar.TypeReg, content: "x"}},
		},
		{
			name: "empty directory versus absent directory",
			why:  "an empty directory has no content to hash and must still be identity",
			left: []tarEntry{
				{name: "keep", typeflag: tar.TypeReg, content: "x"},
				{name: "empty", typeflag: tar.TypeDir},
			},
			right: []tarEntry{{name: "keep", typeflag: tar.TypeReg, content: "x"}},
		},
		{
			name:  "same name as file versus as directory",
			why:   "the typeflag is identity: a name is not a value",
			left:  []tarEntry{{name: "foo", typeflag: tar.TypeReg, content: ""}},
			right: []tarEntry{{name: "foo", typeflag: tar.TypeDir}},
		},
		{
			name:  "trailing newline is content",
			why:   "the classic text-file near miss; nothing normalizes line endings",
			left:  []tarEntry{{name: "f", typeflag: tar.TypeReg, content: "a\n"}},
			right: []tarEntry{{name: "f", typeflag: tar.TypeReg, content: "a"}},
		},
		{
			name:  "separator placement is identity",
			why:   "a/bc and ab/c are the same bytes with the separator moved one place",
			left:  []tarEntry{{name: "a/bc", typeflag: tar.TypeReg, content: "same"}},
			right: []tarEntry{{name: "ab/c", typeflag: tar.TypeReg, content: "same"}},
		},
		{
			name:  "executable bit flip",
			why:   "the one permission bit that IS identity; a runnable tree is a different tree",
			left:  []tarEntry{{name: "f", typeflag: tar.TypeReg, mode: 0644, content: "x"}},
			right: []tarEntry{{name: "f", typeflag: tar.TypeReg, mode: 0755, content: "x"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			left := capture(t, Canonicalizer{}, bytes.NewReader(makeTar(t, tt.left)))
			defer left.Close()
			right := capture(t, Canonicalizer{}, bytes.NewReader(makeTar(t, tt.right)))
			defer right.Close()

			if left.Digest == right.Digest {
				t.Fatalf("near-miss trees share digest %s (%s)", left.Digest, tt.why)
			}
			if bytes.Equal(readFile(t, left.ArchivePath), readFile(t, right.ArchivePath)) {
				t.Fatalf("near-miss trees produced byte-identical canonical archives (%s)", tt.why)
			}
		})
	}
}

// TestCanonicalCaptureIdentityBoundaryIsExecBitOnly states what canonicalization
// deliberately discards. Each pair below MUST hash to one digest today.
//
// This test is not a convenience. Making any of these survive canonicalization —
// preserving the full permission mode, or ownership, or mtimes — changes the
// digest of every tree ever sealed. That is an identity migration with the same
// blast radius as changing tar.FormatGNU: every stored digest becomes
// unreproducible from its stored bytes, and the exact-equality revalidation on
// the read paths starts refusing history. If a future change needs
// mode-preserving snapshots, this test must be edited deliberately, in a commit
// that says so and that ships the migration — it must never break by accident.
func TestCanonicalCaptureIdentityBoundaryIsExecBitOnly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		why   string
		left  []tarEntry
		right []tarEntry
	}{
		{
			name:  "group and other permission bits",
			why:   "0640 and 0644 are one canonical file: only the exec bit survives",
			left:  []tarEntry{{name: "f", typeflag: tar.TypeReg, mode: 0640, content: "x"}},
			right: []tarEntry{{name: "f", typeflag: tar.TypeReg, mode: 0644, content: "x"}},
		},
		{
			name:  "any executable spelling",
			why:   "0711 and 0755 both mean executable and normalize to 0755",
			left:  []tarEntry{{name: "f", typeflag: tar.TypeReg, mode: 0711, content: "x"}},
			right: []tarEntry{{name: "f", typeflag: tar.TypeReg, mode: 0755, content: "x"}},
		},
		{
			name:  "directory permission bits",
			why:   "directories are always 0755; their source mode is not identity at all",
			left:  []tarEntry{{name: "d", typeflag: tar.TypeDir, mode: 0700}},
			right: []tarEntry{{name: "d", typeflag: tar.TypeDir, mode: 0777}},
		},
		{
			name: "ownership",
			why:  "uid, gid and owner names are producer-environment facts, not content",
			left: []tarEntry{{
				name: "f", typeflag: tar.TypeReg, mode: 0644, content: "x",
				uid: 1000, gid: 1000, uname: "alice", gname: "staff",
			}},
			right: []tarEntry{{name: "f", typeflag: tar.TypeReg, mode: 0644, content: "x"}},
		},
		{
			name: "modification, access and change times",
			why:  "all three timestamps are zeroed to the epoch; a re-run is not a new value",
			left: []tarEntry{{
				name: "f", typeflag: tar.TypeReg, mode: 0644, content: "x",
				modTime:    time.Unix(1_000_000, 0),
				accessTime: time.Unix(1_000_001, 0),
				changeTime: time.Unix(1_000_002, 0),
			}},
			right: []tarEntry{{
				name: "f", typeflag: tar.TypeReg, mode: 0644, content: "x",
				modTime: time.Unix(1, 0),
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			left := capture(t, Canonicalizer{}, bytes.NewReader(makeTar(t, tt.left)))
			defer left.Close()
			right := capture(t, Canonicalizer{}, bytes.NewReader(makeTar(t, tt.right)))
			defer right.Close()

			if left.Digest != right.Digest {
				t.Fatalf("identity boundary moved: %s != %s (%s); if this change is intended, it is an identity migration and needs its own commit",
					left.Digest, right.Digest, tt.why)
			}
			if !bytes.Equal(readFile(t, left.ArchivePath), readFile(t, right.ArchivePath)) {
				t.Fatalf("identity boundary moved: canonical bytes differ (%s)", tt.why)
			}
		})
	}
}
