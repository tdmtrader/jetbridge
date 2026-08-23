package main

import (
	"fmt"
	"net/url"
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

// validateRequestKey decides whether a key taken from a request may become a
// filesystem path.
//
// Every containment rule before this one guarded what an archive ENTRY may do.
// None guarded what the REQUEST may do — and the key becomes a path before any
// archive is read. Six escapes followed from that, including an arbitrary
// recursive delete that returned 204.
//
// The vector is percent-encoding. Go's ServeMux cleans the UNESCAPED path, so
// "%2e%2e%2f" survives routing and arrives in r.URL.Path already decoded to
// "../". A literal "../" never reaches a handler — the mux 301s it — so the
// encoded form is the one that matters and the one the tests send.
//
// The rule is "a contained relative path", NOT "a single path segment".
// durable.ValidateKey exists and looks like the right validator, but it caps at
// two segments, and real traffic runs to three: "steps/build-42/result" and
// "caches/job-42/build-abc.tar" are ordinary keys. Reusing it would have
// rejected production traffic, which is a worse failure than the bug — refusing
// to deliver artifacts is more visible than delivering them wrongly, but it is
// still an outage.
//
// The single-segment invariant DOES apply to registerRequest.Key, and
// durable_handlers.go already enforces it there. That is a narrower rule for a
// narrower field, not this one.
func validateRequestKey(key string) error {
	if key == "" {
		return fmt.Errorf("request key is empty")
	}

	if filepath.IsAbs(key) || strings.HasPrefix(key, "/") {
		return fmt.Errorf("request key %q is absolute", key)
	}

	// Clean resolves any "." and ".." the decoded path carries. A key that
	// escapes its root does so lexically here, before it ever reaches the
	// filesystem.
	cleaned := filepath.Clean(key)

	// "." names the storage root itself, and every one of these routes is a
	// SINGLE-ARTIFACT verb. Admitting it turns DELETE /artifacts/<key> into
	// "delete the node's entire artifact store" and POST /mirror into "tar every
	// artifact on this node and PUT it to every peer" — both in one
	// unauthenticated request. The first cut of this validator checked only for
	// ".." and shipped that amplification; an adversarial review found it.
	if cleaned == "." {
		return fmt.Errorf("request key %q names the storage root itself", key)
	}

	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return fmt.Errorf("request key %q escapes the storage root (resolves to %q)", key, cleaned)
	}

	return nil
}

// validateContainedPath decides whether a path supplied in a request body may
// be written to, read from, or removed.
//
// Distinct from validateRequestKey because the input is different: a key is a
// relative fragment the daemon joins onto its own root, while local_path and
// dest arrive as absolute paths the caller chose. Both end in the same place.
//
// Deliberately uses filepath.Rel and not strings.HasPrefix. A string prefix is
// not a path boundary — "<root>-evil" satisfies HasPrefix("<root>") — and that
// is the exact defect this proposal exists to remove. registry.go:119 still has
// it; it is recorded as out of scope with an owner.
//
// Two conditions the plan review named, both of which produce failures that
// look like this rule being too strict:
//
//   - The candidate must NOT be required to exist. A resolve dest is normally
//     created by the call being validated.
//   - root and candidate must be compared in the same resolved form. On macOS
//     t.TempDir() and os.MkdirTemp both hand back /var/folders/... symlinks, so
//     resolving one side only yields spurious rejections.
func validateContainedPath(root, candidate string) error {
	if candidate == "" {
		return fmt.Errorf("path is empty")
	}

	// Resolve the nearest EXISTING ancestor and re-append the remainder.
	//
	// Walking up matters: resolving only the candidate or only its immediate
	// parent leaves a deeply-nonexistent path unresolved while the root
	// resolves, and on macOS /var is a symlink to /private/var — so the two
	// sides end up in different roots and filepath.Rel reports an escape for a
	// perfectly contained path. That failure looks exactly like this rule being
	// too strict, which is the trap the plan review named.
	resolve := func(p string) string {
		p = filepath.Clean(p)
		var trailing []string
		cur := p
		for {
			if r, err := filepath.EvalSymlinks(cur); err == nil {
				return filepath.Join(append([]string{r}, trailing...)...)
			}
			parent := filepath.Dir(cur)
			if parent == cur {
				return p
			}
			trailing = append([]string{filepath.Base(cur)}, trailing...)
			cur = parent
		}
	}

	rootResolved := resolve(root)
	candidateResolved := resolve(candidate)

	rel, err := filepath.Rel(rootResolved, candidateResolved)
	if err != nil {
		return fmt.Errorf("path %q is not comparable to the storage root: %w", candidate, err)
	}
	// rel == "." means the candidate IS the root. That is not "contained": the
	// callers derive siblings from it — copyArtifact does
	// os.MkdirTemp(filepath.Dir(dest)), so dest==root writes into the root's
	// PARENT, which in production is a host directory — and os.RemoveAll(dest)
	// then removes the whole store. Rel reports "." for that case and the first
	// cut of this validator accepted it.
	if rel == "." {
		return fmt.Errorf("path %q is the storage root itself, not a location within it", candidate)
	}

	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path %q resolves outside the storage root (relative: %q)", candidate, rel)
	}

	return nil
}

// peerURL builds an outbound request URL from a key this daemon has already
// validated.
//
// The old form was fmt.Sprintf("%s://%s:%d/stream-in/%s", …, key), which splices
// the key in raw. That matters because the receiving peer does
// strings.TrimPrefix(r.URL.Path, "/stream-in/") and gets whatever we sent,
// decoded — so an unescaped key could inject path structure into a peer's route.
// This daemon writing outside a PEER's root is the confused-deputy half of the
// same defect the inbound validators close.
//
// url.URL.String() escapes what must be escaped and leaves "/" structural, so a
// conforming key is BYTE-IDENTICAL to what Sprintf produced. That is the
// rolling-upgrade guarantee: an escaping daemon and a non-escaping one agree on
// the wire for every legitimate key, and the only keys that differ are ones
// validateRequestKey refuses before they are sent.
func peerURL(scheme, host string, port int, prefix, key string) string {
	u := url.URL{
		Scheme: scheme,
		Host:   fmt.Sprintf("%s:%d", host, port),
		Path:   prefix + key,
	}
	return u.String()
}
