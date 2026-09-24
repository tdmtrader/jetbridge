package tarfs

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// BreakoutError reports an entry that would write, or link, outside the
// extraction destination. LinkName is empty when the entry's own name is what
// escapes.
type BreakoutError struct {
	HeaderName string
	LinkName   string
}

func (err BreakoutError) Error() string {
	if err.LinkName == "" {
		return fmt.Sprintf("entry '%s' resolves outside of target directory", err.HeaderName)
	}
	return fmt.Sprintf("entry '%s' links outside of target directory: %s", err.HeaderName, err.LinkName)
}

// Extract writes the tar stream src into dest, creating dest if needed.
//
// The archive is untrusted: `fly execute --output` feeds it a task output that
// anyone able to run a task in the team can shape, and writes it onto a
// developer's workstation. So every write goes through one os.Root opened on
// dest, which refuses any path, however it is spelled and through whatever
// symlinks already exist, that resolves outside dest. os.Root does not vet a
// symlink's own target, so link targets are checked here: absolute targets and
// targets that climb out are refused as they are met, and once extraction
// stops, whether at the end of the archive or on an error, every link this
// extraction created is re-resolved against the real tree, because a later
// entry can change what an earlier, contained-looking link means ("a" -> "."
// then "esc" -> "a/..").
//
// Directories are extracted owner-writable and get their archived mode and
// times only after every entry is written, deepest first, as tar does, so a
// read-only directory can hold files.
//
// On error, dest may hold a partial extraction, but nothing outside dest has
// been written and no link this extraction created points out of dest; the
// error extraction stopped on is joined with any the link sweep reports.
// Directories are left owner-writable rather than given their archived mode,
// so the partial tree can be removed. Symlinks that were already in dest are
// the caller's and are left alone.
func Extract(src io.Reader, dest string) error {
	dest, err := filepath.Abs(dest)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dest, 0755); err != nil {
		return err
	}

	root, err := os.OpenRoot(dest)
	if err != nil {
		return err
	}
	defer root.Close()

	x := &extractor{
		root:  root,
		chown: runtime.GOOS != "windows" && os.Getuid() == 0,
	}
	defer x.dropParent()
	defer x.dropCursor()

	extractErr := x.extractAll(tar.NewReader(src))

	// The sweep runs however extraction ended. A rejected or truncated entry
	// stops the loop, but every link written before it has already passed
	// only the lexical checks, and a chain of them can already point out.
	if sweepErr := x.refuseEscapingLinks(); sweepErr != nil {
		if extractErr == nil {
			return sweepErr
		}
		return errors.Join(extractErr, sweepErr)
	}

	if extractErr != nil {
		// Directories keep the owner-writable mode they were extracted
		// with: restricting a half-filled tree would only stop the caller
		// removing it.
		return extractErr
	}

	return x.finishDirs()
}

func (x *extractor) extractAll(tarReader *tar.Reader) error {
	for {
		hdr, err := tarReader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}

		if err := x.extractEntry(hdr, tarReader); err != nil {
			return err
		}
	}
}

type extractor struct {
	root  *os.Root
	chown bool

	// links holds the PHYSICAL root-relative location (every directory
	// component resolved) of each symlink and hard link this extraction
	// created. A name alone is not enough: replacing a symlinked directory
	// later in the archive would orphan a link created through it.
	links []string

	// dirs holds each archive directory's final mode and times, keyed by its
	// PHYSICAL root-relative path, applied by finishDirs once every entry is
	// written. Applying a read-only mode as the header is met would refuse
	// the directory's own children, and the children's writes would move its
	// mtime anyway.
	dirs map[string]dirAttrs

	// parent is an os.Root on parentName, the directory the previous entry
	// went into, reused while entries stay in it: every os.Root call opens
	// each directory on its path, so addressing entries by base name within
	// their parent keeps a deep tree's cost per entry constant. staleParent
	// is set when anything is removed, the one change that can make the
	// same name reach a different directory ("s" -> "." holding "s/s").
	parent      *os.Root
	parentName  string
	staleParent bool

	// realDirs holds physical root-relative paths already seen to be
	// directories rather than symlinks, so resolve need not stat them again.
	// That stays true for the whole extraction: no entry removes a directory.
	realDirs map[string]bool

	// cursor is an os.Root on the physical directory cursorRel, the one
	// resolve last looked inside. Links are stat'ed and read by base name
	// through it, and a directory below it is opened from it, so resolving
	// down a deep tree opens each directory once rather than every directory
	// on its path. Like realDirs, it stays valid: it is a real directory,
	// and none is removed.
	cursor    *os.Root
	cursorRel string
}

type dirAttrs struct {
	perm         fs.FileMode
	aTime, mTime time.Time
}

// extractingDirPerm is the mode a directory holds while entries are still
// being written into it: its own bits plus owner rwx, so a read-only
// directory can still be filled.
func extractingDirPerm(perm fs.FileMode) fs.FileMode {
	return perm | 0700
}

func (x *extractor) extractEntry(header *tar.Header, input io.Reader) error {
	root := x.root

	name, err := entryName(header.Name)
	if err != nil {
		return err
	}
	if name == "." {
		// The destination itself: it already exists, and its mode is the
		// caller's, not the archive's.
		return nil
	}

	// Only permission bits. os.Root refuses a creation mode carrying anything
	// else, and setuid/setgid from a task output have no business on a
	// workstation, least of all with chown when fly runs as root.
	perm := header.FileInfo().Mode().Perm()

	// Regular files and symlinks are made, and their attributes set, by base
	// name inside their parent directory.
	at, err := x.parentOf(filepath.Dir(name), header.Name)
	if err != nil {
		return err
	}
	base := filepath.Base(name)

	switch header.Typeflag {
	case tar.TypeDir:
		// By full name: through its parent's root, a link to ".." could not
		// be followed.
		if err := mkdirAll(root, name, extractingDirPerm(perm), header.Name); err != nil {
			return err
		}

		// From here on the directory is addressed where it physically is:
		// through a symlink ("a" -> "." then "a/"), its name can reach
		// dest itself.
		rel, err := x.physicalRel(name, header.Name)
		if err != nil {
			return err
		}
		if rel == "." {
			// dest is the caller's, however the archive spells it: its mode,
			// times and owner are left alone, as for "./". Refusing instead
			// would fail archives that merely hold a link to "." or "..",
			// over an entry that asks for nothing a skip does not give it.
			return nil
		}
		at, base = root, rel

	case tar.TypeReg, tar.TypeRegA:
		// O_EXCL: never write through whatever holds the name, a symlink
		// least of all; replace it.
		var file *os.File
		if err := x.replacing(at, base, header.Name, "", func() (err error) {
			file, err = at.OpenFile(base, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
			return err
		}); err != nil {
			return err
		}

		if _, err := io.Copy(file, input); err != nil {
			file.Close()
			return err
		}

		if err := file.Close(); err != nil {
			return err
		}

	case tar.TypeSymlink:
		// Resolved against the link's own directory, as the kernel will.
		if err := validateLinkTarget(filepath.Dir(name), header.Name, header.Linkname); err != nil {
			return err
		}
		if err := x.replacing(at, base, header.Name, header.Linkname, func() error {
			return at.Symlink(header.Linkname, base)
		}); err != nil {
			return err
		}
		if err := x.recordLink(at, base, name, header); err != nil {
			return err
		}

	case tar.TypeLink:
		// A hard link names another archive member, relative to the archive
		// root. The lexical check refuses absolute and climbing names;
		// root.Link refuses one that reaches outside through a symlink.
		if err := validateLinkTarget(".", header.Name, header.Linkname); err != nil {
			return err
		}
		// It names its target from the archive root, so it is made there.
		if err := x.replacing(root, name, header.Name, header.Linkname, func() error {
			return root.Link(filepath.FromSlash(header.Linkname), name)
		}); err != nil {
			return err
		}
		// Hard-linking a symlink yields a second symlink with the same
		// relative target in a different directory, so it is checked as one.
		if err := x.recordLink(root, name, name, header); err != nil {
			return err
		}

	case tar.TypeBlock, tar.TypeChar, tar.TypeFifo:
		// A device node from a task output is a raw-device handle on the
		// developer's machine (root-owned, with chown), and os.Root offers no
		// way to create any special file inside the boundary. Refuse rather
		// than skip: a silent drop hands back a tree the caller believes is
		// complete.
		return fmt.Errorf("%s: device and FIFO entries are not extracted (type %c)", header.Name, header.Typeflag)

	case tar.TypeXGlobalHeader:
		// skip
		return nil

	default:
		return fmt.Errorf("%s: unsupported entry type (%c)", header.Name, header.Typeflag)
	}

	if header.Typeflag == tar.TypeLink {
		// The link shares its target's inode, whose owner, mode and times were
		// set when the target was extracted. Applying this header's would
		// rewrite the target.
		return nil
	}

	if x.chown {
		if err := at.Lchown(base, header.Uid, header.Gid); err != nil {
			return err
		}
	}

	if header.Typeflag == tar.TypeSymlink {
		// Chmod and Chtimes follow links; a link has nothing of its own to
		// set portably.
		return nil
	}

	aTime := header.AccessTime
	mTime := header.ModTime
	if aTime.Before(mTime) {
		aTime = mTime
	}

	if header.Typeflag == tar.TypeDir {
		// Writable now (MkdirAll leaves an existing directory's mode alone),
		// final mode and times once the tree is written.
		if err := at.Chmod(base, extractingDirPerm(perm)); err != nil {
			return err
		}
		x.recordDir(base, dirAttrs{perm: perm, aTime: aTime, mTime: mTime})
		return nil
	}

	// must be done after chown, which can clear mode bits
	if err := at.Chmod(base, perm); err != nil {
		return err
	}

	// must be done after everything
	return at.Chtimes(base, aTime, mTime)
}

// parentOf returns a root on the archive directory dir, creating it first.
// It is the previous entry's when dir is the same name and nothing has been
// removed since: creating entries only adds names below a directory, and
// every directory on dir's path already exists, so only a removal can change
// where dir leads.
func (x *extractor) parentOf(dir, headerName string) (*os.Root, error) {
	if dir == "." {
		return x.root, nil
	}
	if x.parent != nil && x.parentName == dir && !x.staleParent {
		return x.parent, nil
	}
	x.dropParent()

	if err := mkdirAll(x.root, dir, 0755, headerName); err != nil {
		return nil, err
	}
	parent, err := x.root.OpenRoot(dir)
	if err != nil {
		return nil, breakoutOr(err, headerName, "")
	}
	x.parent, x.parentName = parent, dir
	return parent, nil
}

func (x *extractor) dropParent() {
	if x.parent != nil {
		x.parent.Close()
	}
	x.parent, x.parentName, x.staleParent = nil, "", false
}

// physicalRel is where name really sits, relative to the root, with every
// symlink along it followed. A name that lands outside is refused.
func (x *extractor) physicalRel(name, headerName string) (string, error) {
	rel, err := x.resolve(".", name)
	if errors.Is(err, errEscapes) {
		return "", BreakoutError{HeaderName: headerName}
	}
	if err != nil {
		return "", fmt.Errorf("%s: %w", headerName, err)
	}
	return rel, nil
}

// recordDir defers a directory's final attributes. The key, rel, is
// physical, as for links: the same directory reached through a symlinked
// parent is one directory, and its depth for finishDirs is where it really
// sits. No later entry can turn a directory component of that path into a
// symlink, since entries never remove directories.
func (x *extractor) recordDir(rel string, attrs dirAttrs) {
	if x.dirs == nil {
		x.dirs = map[string]dirAttrs{}
	}
	x.dirs[rel] = attrs // a repeated header wins, as in tar
}

// finishDirs applies each archive directory's final mode and times, deepest
// first, so restricting a parent (to one without owner search, even) cannot
// block reaching a child, and a child's update cannot move a parent's mtime.
func (x *extractor) finishDirs() error {
	sep := string(filepath.Separator)

	rels := make([]string, 0, len(x.dirs))
	for rel := range x.dirs {
		rels = append(rels, rel)
	}
	sort.Slice(rels, func(i, j int) bool {
		di, dj := strings.Count(rels[i], sep), strings.Count(rels[j], sep)
		if di != dj {
			return di > dj
		}
		return rels[i] < rels[j]
	})

	for _, rel := range rels {
		attrs := x.dirs[rel]
		if err := x.root.Chmod(rel, attrs.perm); err != nil {
			return err
		}
		if err := x.root.Chtimes(rel, attrs.aTime, attrs.mTime); err != nil {
			return err
		}
	}
	return nil
}

// entryName turns a header name into a root-relative path. A leading "/" is
// dropped, as tar does and as filepath.Join(dest, name) used to; a name that
// climbs out of the root is refused before touching the filesystem.
func entryName(headerName string) (string, error) {
	slashed := filepath.ToSlash(headerName)
	if clean := path.Clean(slashed); clean == ".." || strings.HasPrefix(clean, "../") {
		return "", BreakoutError{HeaderName: headerName}
	}

	// Cleaning a rooted path drops the leading "/" and any ".." above it.
	name := strings.TrimPrefix(path.Clean("/"+slashed), "/")
	if name == "" {
		return ".", nil
	}
	return filepath.FromSlash(name), nil
}

// validateLinkTarget is the lexical fast path for a link target: refuse an
// absolute one (it names the producing machine's filesystem, and a link out of
// dest is exactly what must not be left behind) and one that climbs above the
// root once joined to fromDir. It cannot see symlinks already on the path;
// os.Root and refuseEscapingLinks cover those.
func validateLinkTarget(fromDir, headerName, linkname string) error {
	if linkname == "" {
		return fmt.Errorf("%s: link entry has an empty target", headerName)
	}

	slashed := filepath.ToSlash(linkname)
	if filepath.IsAbs(linkname) || path.IsAbs(slashed) || filepath.VolumeName(linkname) != "" {
		return BreakoutError{HeaderName: headerName, LinkName: linkname}
	}

	resolved := path.Clean(path.Join(filepath.ToSlash(fromDir), slashed))
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		return BreakoutError{HeaderName: headerName, LinkName: linkname}
	}

	return nil
}

// replacing runs create, which makes name exclusively. If name is taken, it
// clears whatever non-directory holds it, as tar does, so the entry replaces
// it rather than writing through it (through a symlink, that could be a file
// outside dest), and runs create once more. Creating first spares the common
// case a lookup: each os.Root call opens every directory on the path.
func (x *extractor) replacing(root *os.Root, name, headerName, linkname string, create func() error) error {
	err := create()
	if errors.Is(err, fs.ErrExist) {
		removed, removeErr := removeNonDir(root, name, headerName)
		if removeErr != nil {
			return removeErr
		}
		if removed {
			x.staleParent = true
		}
		err = create()
	}
	if err != nil {
		return breakoutOr(err, headerName, linkname)
	}
	return nil
}

// removeNonDir clears whatever non-directory sits at name. A directory is
// left alone, and the create that follows fails on it.
func removeNonDir(root *os.Root, name, headerName string) (bool, error) {
	info, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, breakoutOr(err, headerName, "")
	}
	if info.IsDir() {
		return false, nil
	}
	return true, root.Remove(name)
}

// mkdirAll is root.MkdirAll with its refusals classified. When the last
// component is a symlink that resolves outside the root, os.Root refuses it
// but reports only EEXIST; asking the root to stat the same path surfaces
// the escape.
func mkdirAll(root *os.Root, name string, perm fs.FileMode, headerName string) error {
	err := root.MkdirAll(name, perm)
	if err == nil {
		return nil
	}
	if _, statErr := root.Stat(name); statErr != nil {
		if breakout, ok := breakoutOr(statErr, headerName, "").(BreakoutError); ok {
			return breakout
		}
	}
	return breakoutOr(err, headerName, "")
}

// breakoutOr reports an os.Root containment refusal as a BreakoutError and
// passes any other error through. os.Root's escape error is unexported, so its
// message is the only handle on it.
func breakoutOr(err error, headerName, linkname string) error {
	if err != nil && strings.Contains(err.Error(), "path escapes from parent") {
		return BreakoutError{HeaderName: headerName, LinkName: linkname}
	}
	return err
}

// recordLink notes where the link just created as created in at, under
// archive name name, physically sits, for refuseEscapingLinks. A link whose
// place cannot be found could not be checked, so it is taken back and the
// entry fails.
func (x *extractor) recordLink(at *os.Root, created, name string, header *tar.Header) error {
	dir, err := x.resolve(".", filepath.Dir(name))
	if err != nil {
		x.staleParent = true
		if rmErr := at.Remove(created); rmErr != nil {
			return errors.Join(err, rmErr)
		}
		if errors.Is(err, errEscapes) {
			return BreakoutError{HeaderName: header.Name, LinkName: header.Linkname}
		}
		return fmt.Errorf("%s: %w", header.Name, err)
	}
	x.links = append(x.links, joinRel(dir, filepath.Base(name)))
	return nil
}

// refuseEscapingLinks is the authoritative check, run once extraction has
// stopped, however it stopped: each symlink this extraction created (hard
// links to symlinks included) is resolved from its real location, one
// component at a time the way the kernel does, and any that lands outside the
// root is removed and reported. So is any this check cannot follow: a link
// that cannot be stat'ed, read or resolved is one the kernel may still follow
// out. Every lookup goes through a directory handle, never an absolute path,
// so no depth of tree puts a link beyond the check's reach. It runs before
// finishDirs, while every archive directory is still owner-writable, so a
// link can always be removed; and it does not stop at the first link it
// cannot check, so one failure does not shield the links after it.
func (x *extractor) refuseEscapingLinks() error {
	var escaped error
	var failures []error
	for _, rel := range x.links {
		dir, base := filepath.Dir(rel), filepath.Base(rel)

		target, err := x.linkTarget(dir, base)
		if err == nil && target == "" {
			continue // gone, or not a symlink: nothing to follow
		}
		if err == nil {
			_, err = x.resolve(dir, target)
		}
		if err == nil {
			continue
		}

		if rmErr := x.root.Remove(rel); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
			failures = append(failures, errors.Join(err, rmErr))
			continue
		}
		if !errors.Is(err, errEscapes) {
			failures = append(failures, err) // why it could not be followed
		}
		if escaped == nil {
			escaped = BreakoutError{HeaderName: filepath.ToSlash(rel), LinkName: target}
		}
	}

	if len(failures) == 0 {
		return escaped // nil, or a plain BreakoutError callers can type-check
	}
	return errors.Join(append([]error{escaped}, failures...)...)
}

// linkTarget reads the symlink base in the physical directory dir. It
// returns "" and no error when base is gone (a later entry removed or
// replaced it) or is not a symlink (a hard link to a regular file).
func (x *extractor) linkTarget(dir, base string) (string, error) {
	at, err := x.dirAt(dir)
	if err != nil {
		return "", err
	}
	info, err := at.Lstat(base)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if info.Mode()&fs.ModeSymlink == 0 {
		return "", nil
	}
	return at.Readlink(base)
}

// maxLinkHops bounds the symlinks one resolution follows, as the kernel's
// ELOOP limit does; a path needing more resolves nowhere on the real system.
const maxLinkHops = 255

// errEscapes is resolve's answer for a path that leaves the root: one that
// climbs above it, or meets an absolute link target, which names the
// producing machine's filesystem. os.Root refuses both alike.
var errEscapes = errors.New("path resolves outside the destination")

// resolve answers where p, taken from the physical root-relative directory
// dir, actually lands, as a physical root-relative path, resolving symlinks
// left to right the way the kernel does. Cleaning p first would collapse
// "link/.." textually and report contained for a path the kernel resolves
// outside; walking down applies ".." only to an already-resolved prefix.
// Once a component does not exist, or is not a directory, nothing below it
// can be a symlink, so the rest joins lexically; so does the rest past a
// symlink loop, which the kernel refuses to follow at all. Any other error
// is returned: a component that cannot be examined is not assumed to be a
// plain directory.
//
// Every component is examined by name inside its parent's directory handle
// (see dirAt), never by a path from the filesystem root, so no path length
// limit applies however deep the tree. Each costs at most one Lstat, and
// none once it is in realDirs, so checking many links deep in one tree stays
// linear in the path's depth rather than re-resolving every prefix for every
// link.
func (x *extractor) resolve(dir, p string) (string, error) {
	if isRooted(p) {
		return "", errEscapes
	}

	sep := string(filepath.Separator)
	resolved := dir // "" while it is the root itself
	if resolved == "." {
		resolved = ""
	}
	pending := splitPath(p)
	hops := 0

	for len(pending) > 0 {
		part := pending[0]
		pending = pending[1:]

		switch part {
		case "", ".":
			continue
		case "..":
			if resolved == "" {
				return "", errEscapes
			}
			if resolved = filepath.Dir(resolved); resolved == "." {
				resolved = ""
			}
			continue
		}

		// Concatenated, not Joined: resolved is already clean and part is a
		// plain name, and re-cleaning the whole path per component is
		// quadratic in its depth.
		next := part
		if resolved != "" {
			next = resolved + sep + part
		}
		if x.realDirs[next] {
			resolved = next
			continue
		}

		at, err := x.dirAt(resolved)
		if err != nil {
			return "", err
		}
		info, err := at.Lstat(part)
		if errors.Is(err, fs.ErrNotExist) {
			return lexical(resolved, append([]string{part}, pending...))
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&fs.ModeSymlink == 0 {
			if !info.IsDir() {
				// Nothing is below a file; the kernel goes no further.
				return lexical(next, pending)
			}
			if x.realDirs == nil {
				x.realDirs = map[string]bool{}
			}
			x.realDirs[next] = true
			resolved = next
			continue
		}

		hops++
		if hops > maxLinkHops {
			return lexical(resolved, append([]string{part}, pending...))
		}
		target, err := at.Readlink(part)
		if err != nil {
			return "", err
		}
		// The target stands in for the link's component, taken from the
		// link's directory.
		if isRooted(target) {
			return "", errEscapes
		}
		pending = append(splitPath(target), pending...)
	}

	if resolved == "" {
		return ".", nil
	}
	return resolved, nil
}

// dirAt returns a handle on the physical root-relative directory rel, every
// component of which is a real directory. The cursor is reused, or rel opened
// from it when rel lies below it, so walking down a tree costs one open per
// level; anything else is opened from the root.
func (x *extractor) dirAt(rel string) (*os.Root, error) {
	if rel == "" || rel == "." {
		return x.root, nil
	}
	if x.cursor != nil && x.cursorRel == rel {
		return x.cursor, nil
	}

	from, name := x.root, rel
	if x.cursor != nil && strings.HasPrefix(rel, x.cursorRel+string(filepath.Separator)) {
		from, name = x.cursor, rel[len(x.cursorRel)+1:]
	}
	opened, err := from.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	x.dropCursor()
	x.cursor, x.cursorRel = opened, rel
	return opened, nil
}

func (x *extractor) dropCursor() {
	if x.cursor != nil {
		x.cursor.Close()
	}
	x.cursor, x.cursorRel = nil, ""
}

// lexical joins the rest of a path, which no symlink can redirect any more,
// onto the physical root-relative prefix resolved, refusing one that climbs
// out.
func lexical(resolved string, rest []string) (string, error) {
	joined := filepath.Join(append([]string{resolved}, rest...)...)
	if joined == "" {
		return ".", nil
	}
	if joined == ".." || strings.HasPrefix(joined, ".."+string(filepath.Separator)) {
		return "", errEscapes
	}
	return joined, nil
}

// isRooted reports a path that does not start from wherever it is resolved:
// absolute, rooted ("\x" on Windows) or carrying a volume.
func isRooted(p string) bool {
	return filepath.IsAbs(p) || filepath.VolumeName(p) != "" || strings.HasPrefix(filepath.ToSlash(p), "/")
}

// joinRel places base inside the root-relative directory dir.
func joinRel(dir, base string) string {
	if dir == "." {
		return base
	}
	return dir + string(filepath.Separator) + base
}

func splitPath(p string) []string {
	return strings.Split(filepath.FromSlash(p), string(filepath.Separator))
}
