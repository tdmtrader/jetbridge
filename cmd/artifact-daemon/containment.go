package main

import (
	"code.cloudfoundry.org/lager/v3"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// ErrRefused marks an error the ARCHIVE is answerable for, as opposed to one
// the environment is.
//
// It exists for attribution, not for containment. Containment comes from the
// root handle; this decides whether the caller sees 4xx or 5xx. That matters
// because mirror.go classifies any non-201 from a peer as "rejected", so a
// poisoned artifact reported as 500 reads to an operator as the PEER being at
// fault rather than the artifact's source — the exact attribution problem
// Track 2 exists to fix.
//
// Classify by CAUSE, not by which function raised the error. An earlier draft
// wrapped "containment refusals" and sent malformed tar (400 today) and every
// os.Root traversal refusal to 500 — the two most likely hostile inputs both
// landing on the peer-fault side of the very split invented to prevent that.
var ErrRefused = errors.New("artifact-daemon: refused")

// refused marks err as archive-attributable.
//
// os.Root's escape error is *fs.PathError wrapping an unexported
// *errors.errorString — it matches neither fs.ErrNotExist, fs.ErrPermission nor
// fs.ErrInvalid, so there is no identity to test for. Refusals are therefore
// marked at the CALL SITE, by the code that knows the operation was driven by
// an archive entry.
//
// Owned consequence: a genuine ENOSPC at one of those sites is misreported as
// 400. The alternative — a lexical pre-check to attribute the failure — would
// reintroduce a name check whose disagreement with the handle is this whole
// track's subject, and it does not even work uniformly: Root.Symlink returns
// *os.LinkError rather than *fs.PathError, so error-shape discrimination needs
// two type switches resting on an undocumented invariant.
func refused(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrRefused}, args...)...)
}

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
		return refused("symlink entry %q has an empty target", entryName)
	}

	if filepath.IsAbs(linkname) {
		return refused(
			"symlink entry %q targets an absolute path %q: absolute targets name the producing machine's filesystem and are refused",
			entryName, linkname,
		)
	}

	// Resolve the target against the link's own directory, the way the
	// filesystem will, and require the result to stay inside the destination.
	resolved := filepath.Clean(filepath.Join(filepath.Dir(entryName), linkname))

	if resolved == ".." || strings.HasPrefix(resolved, ".."+string(filepath.Separator)) {
		return refused(
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

	// REFUSE a key that is not already canonical, rather than validating the
	// cleaned form and letting the caller keep the raw one.
	//
	// That gap was a working guard bypass. requestKey returned the RAW key;
	// handleGetArtifact and handleDeleteArtifact built a RelKey from it by
	// hand; and stepHandle splits a RelKey on "/", so:
	//
	//	GET /artifacts/steps%2f%2fbuild-42/out   -> stepHandle ""
	//	GET /artifacts/steps%2f%2e%2fbuild-42/out -> stepHandle "."
	//
	// while the sweeper's key for that tree is "build-42". Both spellings
	// routed, both served 200, and both took a lock that excluded nobody — so
	// a DELETE removed the tree while a read of the same artifact was in
	// flight, and filepath.Walk silently omits what vanishes mid-walk. A
	// truncated tar, served and mirrored onward as complete.
	//
	// Normalising here instead of refusing would fix it only for callers that
	// remember to use the return value — the identical discipline that just
	// failed. Refusing makes "a key IS canonical" true by construction.
	//
	// No legitimate producer emits one: the ATC builds keys by path join.
	// %2e and %2f survive routing because ServeMux cleans the ESCAPED path, so
	// this is the first place a decoded key can be inspected at all.
	if cleaned != key {
		return fmt.Errorf("request key %q is not canonical (cleans to %q)", key, cleaned)
	}

	// Segment rule, deliberately MINIMAL.
	//
	// An earlier version required every segment to match durable.ValidateKey's
	// charset (^[a-zA-Z0-9][a-zA-Z0-9._-]{0,254}$). That was a regression worse
	// in user impact than the bug it was part of fixing: Concourse's own
	// identifier rule (atc/configwarning.go:14) admits ANY Unicode letter and
	// is only a warning, and the user-controlled segment reaches the daemon as
	// handle + "/" + subdir. So "café", "_out", "-leading-dash" and ".git" are
	// legal config today, and the daemon began 400ing them — a broken build,
	// surfacing nowhere in the test suite.
	//
	// Safety here does not come from the charset. It comes from the traversal
	// check below and from resolving the joined path against its root. So this
	// rejects only what cannot be a usable single path component.
	if cleaned != "." {
		for _, seg := range strings.Split(cleaned, string(filepath.Separator)) {
			if seg == "" || seg == "." || seg == ".." {
				return fmt.Errorf("request key %q has an empty or relative segment", key)
			}
			if strings.ContainsRune(seg, 0) {
				return fmt.Errorf("request key %q contains a NUL byte", key)
			}
			if len(seg) > 255 {
				return fmt.Errorf("request key %q has a segment longer than 255 bytes", key)
			}
		}
	}

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
//
// RelKey is a location inside the daemon's storage root, expressed relative to
// it. It is a defined type rather than a string alias so that every place the
// old absolute representation was used becomes a COMPILE ERROR: os.Stat(rk),
// filepath.Join(root, rk), passing it to a string parameter, comparing it to a
// path, and using it as a map key of the wrong map all fail to build.
//
// The type is a migration instrument, not a standing guarantee. string(rk)
// compiles anywhere and is invisible in a diff, so the compiler gives a
// complete checklist ONCE — while this change is being made. Every string(...)
// conversion introduced here is a decision: it becomes LookupAmbientPath, or it
// carries a reason.
type RelKey string

// containedRelKey is validateContainedPath's answer, kept rather than thrown
// away.
//
// The validator already computes exactly the relative form the registry now
// stores; returning it means the representation and the check cannot disagree,
// and there is no second implementation of the symlink walk-up to keep in step.
func containedRelKey(root, candidate string) (RelKey, error) {
	if candidate == "" {
		return "", fmt.Errorf("path is empty")
	}

	// Both sides go through resolvePath, which walks DOWN. See its comment for
	// why walking up was wrong.
	resolve := resolvePath

	rootResolved := resolve(root)
	candidateResolved := resolve(candidate)

	rel, err := filepath.Rel(rootResolved, candidateResolved)
	if err != nil {
		return "", fmt.Errorf("path %q is not comparable to the storage root: %w", candidate, err)
	}
	// rel == "." means the candidate IS the root. That is not "contained": the
	// callers derive siblings from it — copyArtifact does
	// os.MkdirTemp(filepath.Dir(dest)), so dest==root writes into the root's
	// PARENT, which in production is a host directory — and os.RemoveAll(dest)
	// then removes the whole store. Rel reports "." for that case and the first
	// cut of this validator accepted it.
	if rel == "." {
		return "", fmt.Errorf("path %q is the storage root itself, not a location within it", candidate)
	}

	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q resolves outside the storage root (relative: %q)", candidate, rel)
	}

	return RelKey(filepath.ToSlash(rel)), nil
}

// validateContainedPath reports whether candidate lies within root, discarding
// the relative form. Callers that want the relative form call containedRelKey.
func validateContainedPath(root, candidate string) error {
	_, err := containedRelKey(root, candidate)
	return err
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

// structuralDirs are the directories that give the store its shape rather than
// naming an artifact. They must never be addressable through a per-artifact
// verb: DELETE /artifacts/steps removed every artifact on the node and returned
// 204, and GET /artifacts/steps tarred the lot.
//
// The first cut of validateRequestKey rejected "." on the reasoning that "every
// route it guards is a SINGLE-ARTIFACT verb". That reasoning is right and was
// applied to exactly one string. These are the rest of it.
// Compared case-INSENSITIVELY: on APFS and NTFS the filesystem folds case, so
// DELETE /artifacts/STEPS reached the same directory as /steps and an
// exact-string map let it through. Not exploitable on a Linux prod node, but a
// guarantee that depends on the filesystem is not a guarantee.
//
// aliases.json is here because it is a structural FILE at the store root, not
// an artifact: GET disclosed host paths, PUT replaced the alias store, and
// DELETE destroyed it — a full arbitrary-read chain needing no symlink at all.
var structuralNames = map[string]struct{}{
	"steps":            {},
	"artifacts":        {},
	"resource-caches":  {},
	"caches":           {},
	"aliases.json":     {},
	"aliases.json.tmp": {},
}

// artifactLocation is THE way a key becomes a filesystem path. Not "the way for
// /artifacts/" — the way, full stop.
//
// Four rounds of review found the same shape of defect four times: a fix landed
// at the site the reproduction happened to use, and the identical escape stayed
// open one route over. /artifacts/ got a containment check and /stream-in/ did
// not; "." was rejected and "steps" was not. The answer is not a fifth patch, it
// is to make the join itself the guarded operation, so a new caller cannot
// acquire a path without acquiring the rule with it.
//
// Callers get a path that is: derived from a syntactically valid key, not a
// structural directory, and — after symlink resolution — inside the root.
func (s *Server) artifactLocation(root, key string) (string, error) {
	return locateArtifact(root, key)
}

// locateArtifact is a FREE function, not a Server method, because the rule must
// not be inaccessible to a caller. Mirror is a separate struct and could not
// reach the method version — which is exactly how its join stayed unguarded
// while every Server-side join was routed.
// rejectStructuralName is the AUTHORIZATION half of what locateArtifact used
// to do, split out so a caller that gets containment from a handle can still
// apply it.
//
// Keeping them fused is what let phase 5 drop one by replacing the other.
func rejectStructuralName(key string) error {
	if _, ok := structuralNames[strings.ToLower(filepath.Clean(key))]; ok {
		return fmt.Errorf("key %q names a structural path, not an artifact", key)
	}
	return nil
}

func locateArtifact(root, key string) (string, error) {
	if err := validateRequestKey(key); err != nil {
		return "", err
	}

	cleaned := filepath.Clean(key)
	if err := rejectStructuralName(cleaned); err != nil {
		return "", err
	}

	path := filepath.Join(root, cleaned)

	// Resolution matters as much as syntax. A lexically fine key can still land
	// on a symlink planted under the root by an earlier legitimate stream-in,
	// and every write, read and delete then follows it out.
	//
	// Validated against the ROOT THE CALLER PASSED, not against s.storagePath.
	// The first version checked s.storagePath, which made containment vacuous
	// for any caller with a narrower boundary: stream-in passes
	// storagePath/steps, so a symlink planted under steps/ pointing at the
	// store root passed the check and let PUT /stream-in/x/link/aliases.json
	// destroy the alias file. A guard must enforce the boundary its caller
	// means, not the widest one available.
	if err := validateContainedPath(root, path); err != nil {
		return "", err
	}

	return path, nil
}

// validateRegistryPath checks a path the registry hands back, at the moment it
// is USED.
//
// Registering a contained path is not enough. The registry is a cache of
// strings: an alias registered legitimately can have its target swapped for a
// symlink afterwards, and aliases.json is reloaded at boot from whatever was
// persisted — including entries written before any of this existed. Validating
// only at registration is a snapshot; this is the check that actually guards
// the read.
func (s *Server) validateRegistryPath(path string) error {
	return validateContainedPath(s.storagePath, path)
}

// lookupRegistry is THE way a key becomes a path via the registry, exactly as
// artifactLocation is the way it becomes one via a join.
//
// The first attempt at this added validateRegistryPath at the two sites the
// reproduction happened to use and left three — including the mTLS-exempt
// /resolve — reading the registry raw. That is the same site-not-class mistake
// this proposal has now made five times, so the check moved into the lookup
// where it cannot be skipped by writing a new caller.
//
// A registry entry is a cached string. It can be poisoned before this code
// existed (aliases.json is reloaded at boot), or registered legitimately and
// have its target swapped afterwards. Validating at registration is a snapshot;
// this is the check that guards the use.
// The value is now a RelKey, refused at Register if it did not relativize —
// but the check below STAYS. Register validates once, at registration; this
// validates at use. A directory registered legitimately can have a component
// replaced by a symlink afterwards, and the relative form says nothing about
// what its components resolve to today. The relativization removed the class of
// entry that was never contained; it did not make containment a property of the
// string.
func (s *Server) lookupRegistry(key string) (RelKey, bool) {
	rel, found := s.registry.Lookup(key)
	if !found {
		return "", false
	}
	path := s.registry.AmbientPath(rel)
	if err := s.validateRegistryPath(path); err != nil {
		// EVICT, don't merely ignore. An entry resolving outside the root is
		// poison — it can only have come from a pre-existing aliases.json or a
		// target swapped after registration — and leaving it in place means
		// every later lookup pays the check again and the poison survives the
		// next reload.
		//
		// Evicting also preserves behaviour this refusal would otherwise have
		// silently removed: the resource-cache handlers used to prune stale
		// entries after a failed Stat, and short-circuiting before that turned
		// the registry into an accumulator of dead keys.
		s.logger.Info("registry-path-evicted", lager.Data{
			"key": key, "path": path, "reason": err.Error(),
		})
		s.registry.Remove(key)
		return "", false
	}
	return rel, true
}

// mkdirTempIn is os.MkdirTemp for a root handle.
//
// os.Root has no MkdirTemp. Hand-rolled rather than reached for by path,
// because taking the path would mean re-joining storagePath and handing the
// boundary around as a string again — the shape this track exists to remove.
//
// Same contract as os.MkdirTemp: a private-mode directory with an
// unpredictable name, retrying on collision.
func mkdirTempIn(root *os.Root, prefix string) (string, error) {
	for attempt := 0; attempt < 10000; attempt++ {
		name := prefix + strconv.FormatUint(rand.Uint64(), 36)
		err := root.Mkdir(name, 0o700)
		if err == nil {
			return name, nil
		}
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		return "", err
	}
	return "", fmt.Errorf("mkdirTempIn %q: exhausted attempts", prefix)
}

// osName is the ONE place a RelKey becomes a string for an os.Root call.
//
// Root methods take a slash-or-native relative name; RelKey is canonically
// slash-separated so that guard keys compare identically everywhere. Funnelling
// the conversion through a named function keeps `string(rel)` out of the call
// sites, where it would be indistinguishable from a conversion that drops the
// boundary on purpose.
func osName(rel RelKey) string {
	return filepath.FromSlash(string(rel))
}

// resolvePath answers "where does this path actually land", resolving symlinks
// the way the kernel does: one component at a time, left to right.
//
// THE ORDER IS THE WHOLE POINT, and getting it wrong made the containment rule
// a lie. The previous version called filepath.Clean on the input first, then
// walked UP looking for an ancestor that resolved. Clean collapses "link/.."
// textually, so the symlink was never on the path EvalSymlinks saw:
//
//	candidate = <store>/steps/h/link/../victim.txt   (link -> /outside/deep)
//	validator said:  contained, "steps/h/victim.txt"
//	kernel said:     /outside/victim.txt
//
// Every caller that then used the RAW path escaped — including copyArtifact's
// dest, reached from the mTLS-exempt /resolve, where it becomes os.RemoveAll
// and os.Rename. Unauthenticated arbitrary delete and write outside the store.
//
// Walking down fixes it because ".." is only ever applied to a prefix that has
// ALREADY been resolved, which is exactly the kernel's semantics. filepath.Dir
// could not be used for the upward walk for the same reason Clean could not be
// used on the input: it cleans.
//
// The nearest-existing-ancestor behaviour is retained, and still matters: a
// deeply-nonexistent candidate must still be comparable against a root that
// resolves, or macOS's /var -> /private/var makes every contained path look
// like an escape. That is the trap the plan review named, and the differential
// test in containment_resolve_test.go covers both it and the escape above.
func resolvePath(p string) string {
	sep := string(filepath.Separator)
	resolved := "."
	if filepath.IsAbs(p) {
		resolved = sep
	}
	parts := strings.Split(p, sep)
	for i, part := range parts {
		if part == "" || part == "." {
			continue
		}
		r, err := filepath.EvalSymlinks(filepath.Join(resolved, part))
		if err != nil {
			// Nothing exists from here down. No symlink can live at a path
			// that does not exist, so the remainder is safe to join lexically.
			return filepath.Join(append([]string{resolved}, parts[i:]...)...)
		}
		resolved = r
	}
	return resolved
}

// openParent returns a handle on path's parent directory and path's base name.
//
// Callers that create, replace or rename `path` do all of it through this one
// handle. The alternative — re-deriving the parent as a string for each step —
// lets a symlink swapped in between two steps redirect the later ones.
func openParent(path string) (*os.Root, string, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, "", fmt.Errorf("open parent of %q: %w", path, err)
	}
	return root, filepath.Base(path), nil
}

// plusRX applies chmod's "a+rX" to mode: read for all, execute for all only
// where execute already exists or the entry is a directory.
//
// Artifact copies must be readable by task containers running as arbitrary
// UIDs, and a producing step may leave files at 0600.
func plusRX(mode os.FileMode, isDir bool) os.FileMode {
	mode |= 0o444
	if isDir || mode&0o111 != 0 {
		mode |= 0o111
	}
	return mode
}

// copyTree copies the contents of srcRoot/srcName into dstRoot.
//
// Replaces an exec of `cp -R` followed by `chmod -R a+rX`. Modes are set from
// plusRX rather than preserved: the daemon holds CAP_DAC_OVERRIDE but not
// CAP_CHOWN, so ownership cannot be preserved and `cp -p` as root treats the
// chown failure as fatal.
func copyTree(srcRoot *os.Root, srcName string, dstRoot *os.Root) error {
	srcFS := srcRoot.FS()
	return fs.WalkDir(srcFS, srcName, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcName, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}

		switch {
		case d.IsDir():
			return dstRoot.Mkdir(rel, plusRX(info.Mode().Perm(), true))

		case d.Type()&fs.ModeSymlink != 0:
			target, err := srcRoot.Readlink(path.Join(srcName, rel))
			if err != nil {
				return err
			}
			// `cp -R` copied these verbatim. The daemon must not: dest is a
			// container mount, so an outward link resolves against the
			// container's filesystem, not ours. Extraction already refuses
			// absolute targets, so anything that arrived by stream-in passes
			// this for free; it bites only trees written straight through the
			// hostPath mount.
			if err := validateSymlinkTarget(rel, target); err != nil {
				return err
			}
			return dstRoot.Symlink(target, rel)

		case !info.Mode().IsRegular():
			return nil // devices, sockets, fifos: never in an artifact

		default:
			in, err := srcFS.Open(p)
			if err != nil {
				return err
			}
			defer in.Close()
			out, err := dstRoot.OpenFile(rel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, plusRX(info.Mode().Perm(), false))
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, in); err != nil {
				out.Close()
				return err
			}
			return out.Close()
		}
	})
}
