package snapshot

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
)

// ErrExposureMismatch reports that recorded exposure lineage disagrees with the
// bytes it claims to describe. It is a refusal, not a warning: an exposure whose
// path digests are wrong is a false answer to "what did this step actually see",
// which is the only question the lineage exists to answer.
var ErrExposureMismatch = errors.New("snapshot: exposure does not match the exposed content")

// VerifyExposedPaths recomputes every enumerated exposed-path digest from the
// exposed snapshot's canonical archive and refuses on any disagreement.
//
// It exists because the per-path digest is the one part of exposure lineage that
// is a CLAIM ABOUT CONTENT rather than a server-observed fact. Everything else is
// already bound: the mode is chosen by the platform, and the tree digest is
// checked against the exposed input's own SnapshotRef by validateDeclaredExposures
// and again by the artifact reader on every read. The path digests were validated
// for FORMAT only and recomputed by nobody.
//
// A path digest is DEFINED HERE, because nothing defined it before: it is SHA-256
// over the exact content bytes of the regular file at that archive-relative path,
// rendered "sha256:<lowercase hex>" — the same algorithm and the same spelling as
// a tree digest, one level down. Directories and symlinks are not documents and
// cannot be exposed as one.
//
// The walk is a single streaming pass with no random access, because both
// sequences are already sorted bytewise: canonical archive entries by
// sortedCaptureEntries, exposed paths by sortExposedPaths. A claimed path that
// sorts before the current header can never appear later, so it is absent.
//
// This is a seal-time check. It reads no stored record and rejects no stored
// bytes.
func VerifyExposedPaths(ctx context.Context, canonicalArchive io.Reader, exposure InputExposure) error {
	if canonicalArchive == nil {
		return fmt.Errorf("snapshot: exposed archive reader is required")
	}
	if err := exposure.Validate(); err != nil {
		return err
	}
	if exposure.Mode != MaterializationStaticSelector {
		// Full materialization enumerates nothing: the tree digest already
		// records every path, and it is bound elsewhere.
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	reader := tar.NewReader(contextReader{ctx: ctx, reader: canonicalArchive})
	next := 0
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("snapshot: read exposed archive: %w", err)
		}
		if next < len(exposure.Paths) && exposure.Paths[next].Path < header.Name {
			return fmt.Errorf("%w: exposed path %q is absent from the exposed tree",
				ErrExposureMismatch, exposure.Paths[next].Path)
		}
		if next >= len(exposure.Paths) || exposure.Paths[next].Path != header.Name {
			continue
		}
		expected := exposure.Paths[next]
		next++

		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return fmt.Errorf("%w: exposed path %q is not a regular file", ErrExposureMismatch, expected.Path)
		}
		hasher := sha256.New()
		copied, err := io.Copy(hasher, contextReader{ctx: ctx, reader: reader})
		if err != nil {
			return fmt.Errorf("snapshot: read exposed path %q: %w", expected.Path, err)
		}
		if copied != header.Size {
			return fmt.Errorf("%w: exposed path %q is truncated at %d of %d bytes",
				ErrExposureMismatch, expected.Path, copied, header.Size)
		}
		actual := Digest(fmt.Sprintf("sha256:%x", hasher.Sum(nil)))
		if actual != expected.Digest {
			return fmt.Errorf("%w: exposed path %q hashes to %s but the exposure claims %s",
				ErrExposureMismatch, expected.Path, actual, expected.Digest)
		}
	}
	if next < len(exposure.Paths) {
		return fmt.Errorf("%w: exposed path %q is absent from the exposed tree",
			ErrExposureMismatch, exposure.Paths[next].Path)
	}
	return nil
}
