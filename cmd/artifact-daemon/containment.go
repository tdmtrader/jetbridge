package main

import (
	"fmt"
	"path/filepath"
	"strings"
)

// validateSymlinkTarget decides whether a tar symlink entry may be created.
//
// os.Root contains the WRITES an extraction performs, but it deliberately does
// not contain the links themselves: "Symlink does not validate oldname, which
// may reference a location outside the root" (os/root.go). So os.Root alone
// yields a safe extraction that still leaves an outward-pointing link on disk,
// which the next consumer — reading with an ordinary os.ReadFile and no root
// handle — follows straight out. This function is what keeps such a link off
// disk in the first place.
//
// entryName is the archive entry's name, relative to the extraction
// destination. linkname is the target recorded in the header, which may be
// relative to the entry's own directory.
//
// The rule is three clauses:
//
//   - a relative target resolving inside the destination is allowed — the
//     daemon's own producer emits these, and an artifact holding two trees that
//     share a dependency directory depends on them surviving transport;
//   - an absolute target is refused: it names a path on the machine that
//     produced the archive, which is a different filesystem on the machine
//     extracting it, so it was never meaningful here;
//   - a relative target escaping the destination is refused.
//
// Both callers of this rule — extraction and, once it lands, the producer in
// tarDirectory — must use this function rather than reimplementing it. Two
// pieces of code disagreeing about what a path means is the defect this whole
// track removes.
func validateSymlinkTarget(entryName, linkname string) error {
	if linkname == "" {
		return fmt.Errorf("symlink entry %q has an empty target", entryName)
	}

	if filepath.IsAbs(linkname) {
		return fmt.Errorf(
			"symlink entry %q targets an absolute path %q: absolute targets name the producing machine's filesystem and are refused",
			entryName, linkname,
		)
	}

	// Resolve the target against the link's own directory, the way the
	// filesystem will, and require the result to stay inside the destination.
	resolved := filepath.Clean(filepath.Join(filepath.Dir(entryName), linkname))

	if resolved == ".." || strings.HasPrefix(resolved, ".."+string(filepath.Separator)) {
		return fmt.Errorf(
			"symlink entry %q targets %q, which resolves outside the destination (%q)",
			entryName, linkname, resolved,
		)
	}

	return nil
}
