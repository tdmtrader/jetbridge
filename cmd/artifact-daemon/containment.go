package main

import (
	"archive/tar"
	"code.cloudfoundry.org/lager/v3"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// ErrRefused marks an error the ARCHIVE is answerable for rather than the
// environment, deciding 4xx versus 5xx.
//
// Attribution, not containment. mirror.go reads any non-201 from a peer as
// "rejected", so a poisoned artifact reported as 500 reads to an operator as
// the peer being at fault rather than the artifact's source. Classify by cause,
// not by which function raised the error.
var ErrRefused = errors.New("artifact-daemon: refused")

// refused marks err as archive-attributable.
//
// Marked at the CALL SITE because os.Root's escape error has no testable
// identity: *fs.PathError wrapping an unexported *errors.errorString, matching
// none of fs.ErrNotExist, fs.ErrPermission or fs.ErrInvalid. Consequence
// accepted: a genuine ENOSPC at such a site is misreported as 400.
func refused(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrRefused}, args...)...)
}

// validateSymlinkTarget decides whether a tar symlink entry may be created.
//
// os.Root contains an extraction's WRITES but deliberately not its links:
// "Symlink does not validate oldname, which may reference a location outside
// the root" (os/root.go). Without this, extraction is safe but leaves an
// outward link on disk for the next ordinary os.ReadFile to follow straight out.
//
// entryName is relative to the extraction destination; linkname may be relative
// to the entry's own directory. Relative targets resolving inside are allowed —
// the daemon's own producer emits them. Absolute targets are refused: they name
// the producing machine's filesystem, so they were never meaningful here.
//
// Every creator of a symlink must call this rather than reimplement it;
// architecture_root_test.go enforces that.
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
// The vector is percent-encoding: Go's ServeMux cleans the UNESCAPED path, so
// "%2e%2e%2f" survives routing and arrives in r.URL.Path already decoded to
// "../". A literal "../" is 301'd and never reaches a handler, so the encoded
// form is the one that matters.
//
// The rule is "a contained relative path", NOT "a single path segment".
// durable.ValidateKey caps at two segments and real keys run to three
// ("steps/build-42/result"), so reusing it would refuse production traffic.
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

	// REFUSE a non-canonical key rather than normalising it. Callers derive
	// guard keys by splitting on "/", so "steps/./h/out" yields handle "."
	// where the sweeper uses "h" — a lock that excludes nobody, and the sweeper
	// then deletes the tree mid-read. Normalising fixes that only for callers
	// who remember to use the return value; refusing makes "a key IS canonical"
	// true by construction. The ATC builds keys by path join and emits none.
	if cleaned != key {
		return fmt.Errorf("request key %q is not canonical (cleans to %q)", key, cleaned)
	}

	// Deliberately MINIMAL. Safety comes from the traversal check below and
	// from resolving against the root, not from a charset — Concourse
	// identifiers admit any Unicode letter (atc/configwarning.go:14), so
	// "café", "_out" and ".git" are legal config and must not 400.
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

	// "." names the storage root, and every route here is a SINGLE-ARTIFACT
	// verb: admitting it turns DELETE into "delete the whole store" and
	// POST /mirror into "tar everything and PUT it to every peer".
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
// Two conditions, both of which otherwise look like this rule being too strict:
// the candidate must NOT be required to exist (a resolve dest is created by the
// call being validated), and both sides must be resolved the same way — macOS
// hands back /var/folders/... symlinks, so resolving one side only rejects
// perfectly contained paths.

// RelKey is a location inside the storage root, relative to it. A defined type
// so that the old absolute usages — os.Stat(rk), filepath.Join(root, rk),
// passing it to a string parameter — fail to build.
//
// A migration instrument, not a standing guarantee: string(rk) compiles
// anywhere and is invisible in a diff.
type RelKey string

// containedRelKey returns the relative form validateContainedPath computes
// anyway, so the representation and the check cannot disagree.
func containedRelKey(root, candidate string) (RelKey, error) {
	if candidate == "" {
		return "", fmt.Errorf("path is empty")
	}

	rel, err := filepath.Rel(resolvePath(root), resolvePath(candidate))
	if err != nil {
		return "", fmt.Errorf("path %q is not comparable to the storage root: %w", candidate, err)
	}
	// The candidate IS the root. Not "contained": callers derive siblings from
	// it, so a temp dir lands in the root's PARENT and RemoveAll takes the
	// whole store.
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
// Splicing the key into a Sprintf'd URL let it inject path structure into the
// peer's route, since the peer TrimPrefixes r.URL.Path and gets it decoded —
// the confused-deputy half of what the inbound validators close.
//
// url.URL.String() leaves "/" structural, so a conforming key is
// BYTE-IDENTICAL to the old form. That is the rolling-upgrade guarantee: the
// only keys that differ are ones validateRequestKey refuses anyway.
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
// Compared case-INSENSITIVELY: APFS and NTFS fold case, so /artifacts/STEPS
// reached the same directory and an exact-string map let it through. A
// guarantee that depends on the filesystem is not a guarantee.
//
// aliases.json is a structural FILE, not an artifact: GET disclosed host paths,
// PUT replaced the alias store, DELETE destroyed it.
var structuralNames = map[string]struct{}{
	"steps":            {},
	"artifacts":        {},
	"resource-caches":  {},
	"caches":           {},
	"aliases.json":     {},
	"aliases.json.tmp": {},
}

// structuralFileNames are the structural entries that are FILES rather than
// trees. Nothing may ever be created beneath them: a path under aliases.json
// makes the daemon materialise it as a directory, and the alias store then has
// no file to write.
var structuralFileNames = map[string]struct{}{
	"aliases.json":     {},
	"aliases.json.tmp": {},
}

// artifactLocation is THE way a key becomes a filesystem path — not "the way
// for /artifacts/", the way, full stop. Four review rounds found the same
// defect four times because fixes landed per-route; making the join itself the
// guarded operation means a new caller cannot get a path without the rule.
//
// The result is derived from a valid key, is not a structural directory, and
// after symlink resolution lies inside the root.
func (s *Server) artifactLocation(root, key string) (string, error) {
	return locateArtifact(root, key)
}

// locateArtifact is a FREE function, not a Server method: Mirror is a separate
// struct and could not reach a method, which is how its join stayed unguarded.

// rejectStructuralName is the AUTHORIZATION half of locateArtifact, split out
// so a caller that gets containment from a handle can still apply it. Fused,
// one could be dropped by replacing the other.
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

// validateResolveDest decides whether a path supplied as a resolve DESTINATION
// may be written to.
//
// Stricter than validateContainedPath, because naming a dest is not "where do
// I put the bytes" — it is "delete whatever is here and put this in its
// place": copyArtifact clears dest before renaming over it, and so does the
// peer promote. "<root>/steps" is perfectly contained, so containment alone
// let one request delete every artifact on the node and answer 200.
//
// Two rules beyond containment:
//
//   - not a structural name. Those directories are the store's SHAPE, not
//     artifacts; rejectStructuralName folds case, because APFS and NTFS do and
//     an exact-string check let /STEPS through.
//
//   - at least two segments. A real destination is an input mount the ATC
//     built — steps/<handle>/<volume>, three deep — so a single segment is
//     either a structural directory or a new top-level one, and neither is a
//     place a resolve belongs. This is what makes the rule hold for a
//     structural directory nobody has thought of yet.
//
// Deliberately NOT a substitute for the capability. This says "no caller may
// name the store's shape"; only a signed capability says "this caller may name
// THIS dest", which is what keeps one build from clearing another's inputs.
// The whole-store delete must not depend on a control that is configuration.
func validateResolveDest(root, dest string) error {
	// THE SAME COMPUTATION THE OPERATION USES. Judging a resolved rel while
	// the copy creates and opens a lexical one is how this rule kept being
	// true of a path nobody wrote: a symlink expanding deeper than its own
	// location makes a following ".." run land differently under the two, so
	// the door approved "steps/mine/vol/d1/aliases.json.tmp/x" while the
	// daemon created "aliases.json.tmp". One dest, one relative form.
	rel, err := lexicalRel(root, dest)
	if err != nil {
		return err
	}

	segments := strings.Split(filepath.ToSlash(filepath.Clean(filepath.FromSlash(string(rel)))), "/")

	// The same segment hygiene validateRequestKey applies to keys. A dest is
	// equally a filesystem path the caller chose, and without this a 300-byte
	// or NUL-bearing component reaches the filesystem, fails there, and leaves
	// the directories created before it behind.
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("destination %q has an empty or relative segment", dest)
		}
		if strings.ContainsRune(segment, 0) {
			return fmt.Errorf("destination %q contains a NUL byte", dest)
		}
		if len(segment) > 255 {
			return fmt.Errorf("destination %q has a segment longer than 255 bytes", dest)
		}
	}

	// One segment is never a real destination, which covers every structural
	// name without naming them: they are all top-level. (rejectStructuralName
	// is deliberately NOT used here — it would be unreachable behind this rule,
	// and an unreachable guard reads as protection that is not there.)
	if len(segments) < 2 {
		return fmt.Errorf(
			"destination %q is a top-level entry in the storage root; a resolve destination is an input mount below it",
			dest,
		)
	}

	// A structural FILE can never be a prefix of a destination, at any depth.
	// aliases.json is the alias store; a destination under it makes the daemon
	// create it as a DIRECTORY, after which the alias store can never be
	// written again. Depth does not help here — "aliases.json/a/b" is three
	// segments — so the name itself has to be refused.
	if _, isFile := structuralFileNames[strings.ToLower(segments[0])]; isFile {
		return fmt.Errorf(
			"destination %q is inside %q, which is a file the store keeps, not a directory tree",
			dest, segments[0],
		)
	}

	// Inside a structural tree, a destination must name a leaf, not a build's
	// own directory: steps/<handle> holds every volume that build produced, so
	// resolving onto it deletes all of them and substitutes someone else's
	// artifact — which then propagates to every downstream step. Real
	// destinations are steps/<handle>/<volume>, three deep.
	if _, structural := structuralNames[strings.ToLower(segments[0])]; structural && len(segments) < 3 {
		return fmt.Errorf(
			"destination %q names a whole %s entry; a resolve destination is a volume inside one",
			dest, segments[0],
		)
	}

	return nil
}

// openDestParent acquires the destination's parent as a HANDLE, by walking to
// it through the storage root rather than reopening the caller's string.
//
// This is what makes the destination rules hold at the moment of USE. The
// checks above resolve symlinks to judge a path, but the filesystem underneath
// is writable by the containers whose inputs live there, so a component that
// was a plain directory during validation can be a symlink by the time the
// copy runs — and re-deriving the parent with os.OpenRoot on the raw string
// followed it. Walking from s.root instead means os.Root refuses any component
// that leaves the store, whatever it became in between.
//
// Containment is not sufficient on its own: a symlink pointing at the store
// ROOT stays inside it, and the base would then name a structural directory.
// So the acquired handle is also required not to BE the storage root, compared
// by identity rather than by name.
// lexicalDestRel is the destination's location relative to the storage root,
// computed WITHOUT resolving symlinks in the destination.
//
// Deliberately not containedRelKey. That resolves the candidate, which is
// right for judging where a path lands but wrong for deciding which path was
// authorized: the capability signs the destination STRING, so resolving it
// through links the caller planted makes authorization and effect name
// different objects. Only the ROOT is resolved — that one is ours, and macOS
// hands back /var -> /private/var for it.
func (s *Server) lexicalDestRel(dest string) (RelKey, error) {
	return lexicalRel(s.storagePath, dest)
}

// lexicalRel is the ONE way a destination becomes a location under the
// storage root. Every rule and every operation derives from this and nothing
// else; two computations of "where does this dest go" is the defect that
// outlived five rounds of fixes here.
func lexicalRel(storagePath, dest string) (RelKey, error) {
	if dest == "" {
		return "", fmt.Errorf("destination is empty")
	}
	if !filepath.IsAbs(dest) {
		return "", fmt.Errorf("destination %q is not absolute", dest)
	}

	cleaned := filepath.Clean(dest)
	for _, root := range []string{filepath.Clean(storagePath), resolvePath(storagePath)} {
		rel, err := filepath.Rel(root, cleaned)
		if err != nil {
			continue
		}
		if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		return RelKey(filepath.ToSlash(rel)), nil
	}
	return "", fmt.Errorf("destination %q is not a location inside the storage root", dest)
}

// descendWithoutSymlinks walks dir one component at a time from the storage
// root, refusing any component that is a symlink and pinning each level by
// identity before descending into it.
//
// Refusing links outright is the rule the destination guards kept approximating
// one attack at a time. A destination is a name the caller was authorized to
// write; if any component is a link the caller controls, the authorized name
// and the written object are different things, and no later check can tell —
// a link at steps/<mine>/vol pointing to steps/<victim>/vol makes the parent
// legitimately BE steps/<victim>, which is not the root, not a structural tree,
// and correctly shaped. Nothing downstream can see it. So it is refused here.
//
// The identity re-check after each open closes the swap between Lstat and
// OpenRoot: if the component became a link in that window, the opened directory
// is the link's target and no longer the inode that was checked.
//
// Costs nothing legitimate: a destination is a hostPath mount the kubelet
// created, and those are real directories.
func (s *Server) descendWithoutSymlinks(dir string) (*os.Root, error) {
	root, _, err := s.walkDestTracked(dir, false)
	return root, err
}

// removeCreated deletes, deepest first, directories a walk created. Plain
// Remove, never RemoveAll: anything that has since been populated came from
// somewhere else and is not ours to take.
func (s *Server) removeCreated(created []string) {
	for i := len(created) - 1; i >= 0; i-- {
		parent := path.Dir(created[i])
		if parent == "." {
			s.root.Remove(created[i])
			continue
		}
		if root, err := s.descendWithoutSymlinks(parent); err == nil {
			root.Remove(path.Base(created[i]))
			root.Close()
		}
	}
}

// createDestParent is descendWithoutSymlinks that CREATES the components it
// does not find. The peer branch needs it: it delivers artifacts for handles
// this node has never seen, so steps/<handle> legitimately does not exist yet.
//
// Creating is a write, so it obeys the same rule as every other write here —
// it follows the authorized name and refuses a symlink rather than following
// one. An os.Root.MkdirAll on the whole path would not: os.Root contains it,
// but a symlink pointing elsewhere INSIDE the store is followed, so a link the
// caller planted in its own volume steered root-owned directory creation to
// any store path it liked, on a request the daemon then refused — a side
// effect from a refusal, which this package forbids outright.
// It returns an undo alongside the handle: the caller's work can still fail
// AFTER the walk succeeds — the destination rules can refuse, or the peer can
// go away mid-fetch — and the directories this created are then residue from a
// request that delivered nothing. Nothing reclaims them: the sweeper walks
// steps/ and artifacts/ only, so an attacker-chosen top-level name is
// permanent, and the route is unauthenticated unless a capability key is
// configured. The caller MUST call undo on any later failure.
func (s *Server) createDestParent(dir string) (*os.Root, func(), error) {
	root, created, err := s.walkDestTracked(dir, true)
	if err != nil {
		return nil, nil, err
	}
	return root, func() { s.removeCreated(created) }, nil
}

func (s *Server) walkDestTracked(dir string, create bool) (_ *os.Root, _ []string, err error) {
	current := s.root
	closeCurrent := func() {
		if current != s.root {
			current.Close()
		}
	}

	// Directories this call created, deepest last. A refused request must
	// leave nothing behind: without this, a destination that fails at its LAST
	// component still left every earlier one on disk, so an attacker-chosen
	// name became a permanent root-owned directory in the store on a request
	// the daemon reported as refused — repeatable without bound.
	var created []string
	defer func() {
		if err != nil {
			s.removeCreated(created)
		}
	}()

	var walked string
	for _, component := range strings.Split(filepath.ToSlash(dir), "/") {
		if component == "" || component == "." {
			continue
		}
		walked = path.Join(walked, component)

		info, lstatErr := current.Lstat(component)
		if create && errors.Is(lstatErr, fs.ErrNotExist) {
			if mkErr := current.Mkdir(component, 0o755); mkErr != nil && !errors.Is(mkErr, fs.ErrExist) {
				closeCurrent()
				return nil, nil, fmt.Errorf("create %q: %w", component, mkErr)
			} else if mkErr == nil {
				created = append(created, walked)
			}
			info, lstatErr = current.Lstat(component)
		}
		if lstatErr != nil {
			closeCurrent()
			return nil, nil, fmt.Errorf("look up %q: %w", component, lstatErr)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			closeCurrent()
			return nil, nil, fmt.Errorf(
				"component %q is a symlink; a destination must be a real path, or the name that was authorized and the directory that is written are different places",
				component,
			)
		}
		if !info.IsDir() {
			closeCurrent()
			return nil, nil, fmt.Errorf("component %q is not a directory", component)
		}

		next, openErr := current.OpenRoot(component)
		if openErr != nil {
			closeCurrent()
			return nil, nil, fmt.Errorf("open %q: %w", component, openErr)
		}
		openedInfo, statErr := next.Stat(".")
		if statErr != nil {
			next.Close()
			closeCurrent()
			return nil, nil, fmt.Errorf("stat %q: %w", component, statErr)
		}
		if !os.SameFile(info, openedInfo) {
			next.Close()
			closeCurrent()
			return nil, nil, fmt.Errorf("component %q changed while it was being opened", component)
		}

		closeCurrent()
		current = next
	}

	if current == s.root {
		return nil, nil, fmt.Errorf("destination parent %q is the storage root", dir)
	}
	return current, created, nil
}

func (s *Server) openDestParent(dest string) (*os.Root, string, error) {
	rel, err := s.lexicalDestRel(dest)
	if err != nil {
		return nil, "", err
	}

	relPath := osName(rel)
	dir, base := filepath.Split(relPath)
	dir = filepath.Clean(dir)
	if base == "" {
		return nil, "", fmt.Errorf("destination %q has no final element", dest)
	}

	parent := s.root
	if dir != "." {
		parent, err = s.descendWithoutSymlinks(dir)
		if err != nil {
			return nil, "", fmt.Errorf("open destination parent %q: %w", dir, err)
		}
	}

	closeParent := func() {
		if parent != s.root {
			parent.Close()
		}
	}

	// The destination itself must not be a symlink either. RemoveAll would take
	// the link rather than its target, so this is not the destructive case the
	// component rule closes — but a caller authorized to write a name has not
	// been authorized to have that name mean somewhere else, and letting it
	// through leaves the daemon delivering an artifact to a directory nobody
	// named.
	if info, err := parent.Lstat(base); err == nil && info.Mode()&os.ModeSymlink != 0 {
		closeParent()
		return nil, "", fmt.Errorf(
			"destination %q is a symlink; a destination must be a real path", dest,
		)
	}

	// Identify the directory that was actually OPENED, and refuse it if it is
	// the store root or one of the structural trees.
	//
	// By IDENTITY, not by the name we asked for. The shape rules applied to the
	// request string cannot bind this: between validating the string and
	// walking to it, a component the caller owns can become a symlink, and the
	// walk then legitimately lands somewhere else inside the store. Comparing
	// the opened directory with os.SameFile is the only form of the rule that a
	// later swap cannot invalidate — whatever path led here, this object either
	// is <store>/steps or it is not.
	//
	// Refusing these is what stops a destination one level under the root:
	// dest "<store>/steps/<handle>" has parent "steps", so its RemoveAll takes
	// every volume another build produced. A real destination sits inside a
	// build's own directory, whose parent is <store>/steps/<handle>.
	parentInfo, err := parent.Stat(".")
	if err != nil {
		closeParent()
		return nil, "", fmt.Errorf("stat destination parent %q: %w", dir, err)
	}

	forbidden := []string{s.storagePath}
	for name := range structuralNames {
		forbidden = append(forbidden, filepath.Join(s.storagePath, name))
	}
	for _, path := range forbidden {
		info, statErr := os.Stat(path)
		if statErr != nil {
			continue // a structural directory that does not exist cannot be the target
		}
		if os.SameFile(parentInfo, info) {
			closeParent()
			return nil, "", fmt.Errorf(
				"destination %q resolves into %q, where every name is a whole artifact tree rather than a place to write one",
				dest, path,
			)
		}
	}

	if parent == s.root {
		// Never hand out the server's own root handle: the caller closes what
		// it is given, and closing s.root breaks every later operation.
		return nil, "", fmt.Errorf("destination %q resolves into the storage root", dest)
	}
	return parent, base, nil
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

// lookupRegistry is THE way a key becomes a path via the registry, as
// artifactLocation is the way it becomes one via a join. The check lives in the
// lookup so a new caller cannot skip it.
//
// Register validates once, at registration; this validates at USE. A directory
// registered legitimately can have a component replaced by a symlink
// afterwards, and aliases.json is reloaded at boot from whatever was persisted.
func (s *Server) lookupRegistry(key string) (RelKey, bool) {
	rel, found := s.registry.Lookup(key)
	if !found {
		return "", false
	}
	path := s.registry.AmbientPath(rel)
	if err := s.validateRegistryPath(path); err != nil {
		// EVICT rather than ignore: left in place, the poison survives the next
		// reload and every lookup pays the check again.
		s.logger.Info("registry-path-evicted", lager.Data{
			"key": key, "path": path, "reason": err.Error(),
		})
		s.registry.Remove(key)
		return "", false
	}
	return rel, true
}

// mkdirTempIn is os.MkdirTemp for a root handle, which os.Root lacks. Taking a
// path instead would hand the boundary around as a string again.
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

// osName is the ONE place a RelKey becomes a string for an os.Root call. Named
// so that a bare string(rel) at a call site stands out as something else.
func osName(rel RelKey) string {
	return filepath.FromSlash(string(rel))
}

// resolvePath answers "where does this path actually land", resolving symlinks
// the way the kernel does: one component at a time, left to right.
//
// THE ORDER IS THE WHOLE POINT. Cleaning the input first collapses "link/.."
// textually, so the symlink is never on the path EvalSymlinks sees and the
// validator reports contained for something the kernel resolves outside.
// Walking down applies ".." only to an already-resolved prefix, which is the
// kernel's own semantics. filepath.Dir cannot be used to walk up for the same
// reason Clean cannot be used on the input: it cleans.
//
// The nearest-existing-ancestor behaviour is retained so that a deeply
// non-existent candidate stays comparable against a root that resolves —
// otherwise macOS's /var -> /private/var makes every contained path an escape.
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

// openParent returns a handle on path's parent and path's base name, so that
// creating, replacing and renaming `path` all go through ONE handle. Re-deriving
// the parent per step lets a symlink swapped in between steps redirect the
// later ones.
func openParent(path string) (*os.Root, string, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, "", fmt.Errorf("open parent of %q: %w", path, err)
	}
	return root, filepath.Base(path), nil
}

// plusRX applies chmod's "a+rX": read for all, execute only where it already
// exists or the entry is a directory. Task containers run as arbitrary UIDs and
// a producing step may leave files at 0600.
func plusRX(mode os.FileMode, isDir bool) os.FileMode {
	mode |= 0o444
	if isDir || mode&0o111 != 0 {
		mode |= 0o111
	}
	return mode
}

// promoteDir makes a fully-built staging directory visible at its final name.
//
// THE CHMOD IS THE POINT, and it is here rather than at each call site because
// every one of them got it wrong the same way. Staging dirs are created 0700
// deliberately — a half-built tree must not be readable — and rename PRESERVES
// the mode, so promoting one without widening it hands the consumer a
// root-owned 0700 directory. On the resolve path that directory is the task
// container's mount point, and images pick their own USER (the pod security
// context sets no runAsUser), so the step fails with EACCES on its own input
// before reading a byte. The entries INSIDE were always widened by plusRX or
// sanitizeMode; the container just could not reach them.
//
// The existing mode is widened rather than replaced, so a caller that
// deliberately made a directory more permissive keeps it.
func promoteDir(parent *os.Root, tmp, base string) error {
	info, err := parent.Stat(tmp)
	if err != nil {
		return fmt.Errorf("stat staged directory %q: %w", tmp, err)
	}
	if err := parent.Chmod(tmp, plusRX(info.Mode().Perm(), true)); err != nil {
		return fmt.Errorf("widen staged directory %q: %w", tmp, err)
	}
	return parent.Rename(tmp, base)
}

// copyTree copies srcRoot/srcName into dstRoot, replacing an exec of `cp -R`
// plus `chmod -R a+rX`. Modes come from plusRX rather than being preserved: the
// daemon holds CAP_DAC_OVERRIDE but not CAP_CHOWN, and `cp -p` as root treats
// the chown failure as fatal.
//
// ctx is checked per entry: the resolve path runs this while holding a
// node-wide semaphore slot, and a caller that has gone away must free the
// slot rather than finish a copy nobody will read.
func copyTree(ctx context.Context, srcRoot *os.Root, srcName string, dstRoot *os.Root) error {
	srcFS := srcRoot.FS()
	return fs.WalkDir(srcFS, srcName, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
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

// tarTree writes a tar archive of root/loc.
//
// The daemon's only tar producer. Two existed — one walking a handle and one
// walking an ambient path, disagreeing about directory entries and file modes —
// which is the "two pieces of code disagreeing about what a path means" shape
// this package exists to remove.
//
// Symlinks are VALIDATED, so the daemon refuses to emit an archive its own
// extractor would refuse to accept. Absolute targets name the producing
// machine's filesystem; carried into a consumer they resolve against that
// consumer's namespace instead.
//
// Directory entries ARE emitted, so empty directories survive transport.
func tarTree(w io.Writer, root *os.Root, loc RelKey) error {
	tw := tar.NewWriter(w)
	name := osName(loc)
	rootFS := root.FS()

	err := fs.WalkDir(rootFS, name, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(name, p)
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

		hdr := &tar.Header{
			Name:    rel,
			Mode:    int64(info.Mode().Perm()),
			ModTime: info.ModTime(),
		}

		switch {
		case d.IsDir():
			hdr.Typeflag = tar.TypeDir
			return tw.WriteHeader(hdr)

		case d.Type()&fs.ModeSymlink != 0:
			link, err := root.Readlink(p)
			if err != nil {
				return err
			}
			if err := validateSymlinkTarget(rel, link); err != nil {
				return err
			}
			hdr.Typeflag = tar.TypeSymlink
			hdr.Linkname = link
			return tw.WriteHeader(hdr)

		case !info.Mode().IsRegular():
			return nil

		default:
			hdr.Typeflag = tar.TypeReg
			hdr.Size = info.Size()
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			f, err := rootFS.Open(p)
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = io.Copy(tw, f)
			return err
		}
	})
	if err != nil {
		// Deliberately skip tw.Close(): the terminator would make a truncated
		// stream parse as a complete archive.
		return err
	}
	return tw.Close()
}

// Refusal reasons. A BOUNDED SET, because they are Prometheus labels: deriving
// one from a key, path or error string would give the metric a series per
// request and eventually take the scrape down.
const (
	reasonInvalidKey     = "invalid_key"
	reasonUncontained    = "uncontained_path"
	reasonStructuralName = "structural_name"
	reasonInvalidJSON    = "invalid_json"
	reasonMissingField   = "missing_field"
	reasonBodyTooLarge   = "body_too_large"
	reasonCapability     = "capability"
	reasonNotConfigured  = "not_configured"
	reasonArchive        = "archive"
	reasonNotFound       = "not_found"
	reasonClientCert     = "client_cert"

	// Hangar's strict-tree routes. Kept in the same bounded set rather than a
	// second one of their own: the metric has one series space, and a Hangar
	// refusal is a refusal.
	reasonMalformed        = "malformed"
	reasonLimitExceeded    = "limit_exceeded"
	reasonConflict         = "conflict"
	reasonTreeVerification = "tree_verification"
	reasonUnavailable      = "unavailable"
)

// refusalRoute is the bounded route label for a refusal.
//
// Taken from r.Pattern — the pattern the request MATCHED in the mux — not from
// r.URL.Path, which contains the artifact key and would give the metric a
// series per request. Using the registered pattern also means a new route
// labels itself correctly instead of falling into a catch-all.
//
// It is why this function does not read the request URL: an architecture guard
// keeps URL reads confined to requestKey so a key is derived in exactly one
// place, and that guard caught an earlier version of this that scanned the path.
func refusalRoute(r *http.Request) string {
	if r == nil || r.Pattern == "" {
		return "unknown"
	}
	return r.Pattern
}
