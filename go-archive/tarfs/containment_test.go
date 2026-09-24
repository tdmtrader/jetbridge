package tarfs_test

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/concourse/concourse/go-archive/tarfs"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// entry is one tar header plus its body. archivetest cannot express hard
// links, devices or setuid modes, which is exactly what these specs need.
type entry struct {
	hdr  tar.Header
	body string
}

func dir(name string, mode int64) entry {
	return entry{hdr: tar.Header{Typeflag: tar.TypeDir, Name: name, Mode: mode}}
}

func file(name string, mode int64, body string) entry {
	return entry{hdr: tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: mode, Size: int64(len(body))}, body: body}
}

func symlink(name, target string) entry {
	return entry{hdr: tar.Header{Typeflag: tar.TypeSymlink, Name: name, Linkname: target, Mode: 0777}}
}

func hardlink(name, target string) entry {
	return entry{hdr: tar.Header{Typeflag: tar.TypeLink, Name: name, Linkname: target, Mode: 0644}}
}

func tarOf(entries ...entry) io.Reader {
	buf := new(bytes.Buffer)
	w := tar.NewWriter(buf)
	for _, e := range entries {
		hdr := e.hdr
		Expect(w.WriteHeader(&hdr)).To(Succeed())
		_, err := w.Write([]byte(e.body))
		Expect(err).NotTo(HaveOccurred())
	}
	Expect(w.Close()).To(Succeed())
	return buf
}

var _ = Describe("Extract containment", func() {
	var (
		base string // the directory CONTAINING dest; escapes land here
		dest string
	)

	BeforeEach(func() {
		var err error
		base, err = os.MkdirTemp("", "tarfs-containment")
		Expect(err).NotTo(HaveOccurred())
		// Resolve so a symlinked tmp (macOS /var -> /private/var) cannot make
		// an absolute-target assertion pass or fail for the wrong reason.
		base, err = filepath.EvalSymlinks(base)
		Expect(err).NotTo(HaveOccurred())

		dest = filepath.Join(base, "dest")
		Expect(os.Mkdir(dest, 0755)).To(Succeed())

		Expect(os.Mkdir(filepath.Join(base, "outside"), 0755)).To(Succeed())
		Expect(os.Mkdir(filepath.Join(base, "dest-evil"), 0755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(base, "dest-evil", "secret"), []byte("secret"), 0644)).To(Succeed())
	})

	AfterEach(func() {
		os.RemoveAll(base)
	})

	extract := func(entries ...entry) error {
		return tarfs.Extract(tarOf(entries...), dest)
	}

	expectBreakout := func(err error) {
		ExpectWithOffset(1, err).To(HaveOccurred())
		var breakout tarfs.BreakoutError
		ExpectWithOffset(1, err).To(BeAssignableToTypeOf(breakout), "got %T: %v", err, err)
	}

	// expectLinkRefused asserts err carries the sweep's refusal of link: the
	// error extraction stopped on can sit beside it, joined.
	expectLinkRefused := func(err error, link string) {
		ExpectWithOffset(1, err).To(HaveOccurred())
		refused := false
		var walk func(error)
		walk = func(e error) {
			var b tarfs.BreakoutError
			if errors.As(e, &b) && b.HeaderName == link && b.LinkName != "" {
				refused = true
			}
			if j, ok := e.(interface{ Unwrap() []error }); ok {
				for _, inner := range j.Unwrap() {
					walk(inner)
				}
			}
		}
		walk(err)
		ExpectWithOffset(1, refused).To(BeTrue(), "link %q was not refused: %v", link, err)
	}

	// restoreWritable lets AfterEach remove a tree holding read-only
	// directories.
	restoreWritable := func() {
		filepath.WalkDir(dest, func(p string, d os.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				os.Chmod(p, 0755)
			}
			return nil
		})
	}

	expectOutsideUntouched := func() {
		_, err := os.Lstat(filepath.Join(base, "victim.txt"))
		ExpectWithOffset(1, os.IsNotExist(err)).To(BeTrue(), "victim.txt was written outside dest")

		entries, err := os.ReadDir(filepath.Join(base, "outside"))
		ExpectWithOffset(1, err).NotTo(HaveOccurred())
		ExpectWithOffset(1, entries).To(BeEmpty(), "something was written into the outside dir")

		secret, err := os.ReadFile(filepath.Join(base, "dest-evil", "secret"))
		ExpectWithOffset(1, err).NotTo(HaveOccurred())
		ExpectWithOffset(1, string(secret)).To(Equal("secret"))
	}

	// expectNoEscapingLinks walks dest and fails if any symlink left on disk
	// resolves outside it: a refusal must not strand an outward link for the
	// developer's next command to follow.
	expectNoEscapingLinks := func() {
		err := filepath.WalkDir(dest, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.Type()&os.ModeSymlink == 0 {
				return nil
			}
			resolved, err := filepath.EvalSymlinks(p)
			if err != nil {
				return nil // dangling: resolves nowhere
			}
			rel, err := filepath.Rel(dest, resolved)
			Expect(err).NotTo(HaveOccurred())
			Expect(rel).NotTo(HavePrefix(".."), "symlink %s resolves outside dest to %s", p, resolved)
			return nil
		})
		ExpectWithOffset(1, err).NotTo(HaveOccurred())
	}

	Describe("escape vectors", func() {
		It("refuses a ../ entry name", func() {
			expectBreakout(extract(file("../victim.txt", 0644, "pwned")))
			expectOutsideUntouched()
		})

		It("refuses a relative symlink that climbs out, and anything written through it", func() {
			err := extract(
				symlink("evil", "../outside"),
				file("evil/pwned", 0644, "pwned"),
			)
			expectBreakout(err)
			expectOutsideUntouched()
			expectNoEscapingLinks()
		})

		It("refuses an absolute symlink, and anything written through it", func() {
			err := extract(
				symlink("abs", filepath.Join(base, "outside")),
				file("abs/pwned", 0644, "pwned"),
			)
			expectBreakout(err)
			expectOutsideUntouched()
			_, statErr := os.Lstat(filepath.Join(dest, "abs"))
			Expect(os.IsNotExist(statErr)).To(BeTrue(), "the absolute symlink was left on disk")
		})

		It("refuses an absolute symlink even when it points inside dest", func() {
			Expect(os.WriteFile(filepath.Join(dest, "inside"), []byte("x"), 0644)).To(Succeed())
			expectBreakout(extract(symlink("abs", filepath.Join(dest, "inside"))))
		})

		It("refuses a symlink chain whose parts each look contained", func() {
			// "a" -> "." is contained, and "a/.." lexically cleans to ".", but
			// the kernel resolves esc -> dest/./.. = base.
			err := extract(
				symlink("a", "."),
				symlink("esc", "a/.."),
			)
			expectBreakout(err)
			expectNoEscapingLinks()
		})

		It("refuses a contained-looking symlink chain even when a later entry is rejected", func() {
			// Both links pass the per-entry checks; the ../x entry stops
			// extraction before the tree is complete. The escaping-link sweep
			// must still run, or esc is left pointing at base.
			err := extract(
				symlink("a", "."),
				symlink("esc", "a/.."),
				file("../x", 0644, "pwned"),
			)
			var breakout tarfs.BreakoutError
			Expect(errors.As(err, &breakout)).To(BeTrue(), "got %T: %v", err, err)
			Expect(err.Error()).To(ContainSubstring("../x"), "the original refusal was lost")
			expectLinkRefused(err, "esc")
			expectNoEscapingLinks()
			expectOutsideUntouched()
		})

		It("refuses a contained-looking symlink chain even when the stream is truncated", func() {
			whole := tarOf(
				symlink("a", "."),
				symlink("esc", "a/.."),
				file("after", 0644, "x"),
			).(*bytes.Buffer).Bytes()
			// Two 512-byte link headers, then a partial third header.
			truncated := bytes.NewReader(whole[:2*512+100])

			err := tarfs.Extract(truncated, dest)
			Expect(errors.Is(err, io.ErrUnexpectedEOF)).To(BeTrue(), "the truncation was lost: %v", err)
			expectLinkRefused(err, "esc")
			expectNoEscapingLinks()
		})

		It("refuses a write through a contained-looking symlink chain at the os.Root boundary", func() {
			// Every link passes the lexical check ("a/../outside" cleans to
			// "outside"), so esc/x is the entry that reaches os.Root, which
			// must refuse it: the kernel resolves esc to base/outside.
			err := extract(
				symlink("a", "."),
				symlink("esc", "a/../outside"),
				file("esc/x", 0644, "pwned"),
			)
			var breakout tarfs.BreakoutError
			Expect(errors.As(err, &breakout)).To(BeTrue(), "got %T: %v", err, err)
			Expect(breakout.HeaderName).To(Equal("esc/x"), "the write-through entry was not the one refused: %v", err)
			Expect(breakout.LinkName).To(BeEmpty())
			expectLinkRefused(err, "esc")
			expectOutsideUntouched()
			expectNoEscapingLinks()
		})

		Describe("directory entries that resolve to dest itself", func() {
			// dest's mode, times and owner are the caller's (`fly execute -o
			// out=.` extracts into the working directory). A directory entry
			// reaching dest through a symlink is skipped, exactly as "./" is.
			// (Creating the link legitimately bumps dest's mtime, so the check
			// is that the header's own mtime was not applied.)
			archivedMTime := time.Unix(12345, 0)

			BeforeEach(func() {
				if runtime.GOOS == "windows" {
					Skip("directory permission bits are not modelled on windows")
				}
				Expect(os.Chmod(dest, 0750)).To(Succeed())
			})

			takeover := func(name string, perm int64) entry {
				e := dir(name, perm)
				e.hdr.ModTime = archivedMTime
				e.hdr.AccessTime = archivedMTime
				return e
			}

			expectDestUntouched := func() {
				info, err := os.Stat(dest)
				ExpectWithOffset(1, err).NotTo(HaveOccurred())
				ExpectWithOffset(1, info.Mode().Perm()).To(Equal(os.FileMode(0750)), "dest's mode was taken over")
				ExpectWithOffset(1, info.ModTime()).NotTo(Equal(archivedMTime), "dest's mtime was taken over")
			}

			for _, perm := range []int64{0777, 0000} {
				It("leaves dest alone for a directory entry through a link to . (mode "+os.FileMode(perm).String()+")", func() {
					DeferCleanup(restoreWritable)
					Expect(extract(symlink("a", "."), takeover("a/", perm))).To(Succeed())
					expectDestUntouched()
				})
			}

			It("leaves dest alone for a directory entry through a nested link to ..", func() {
				DeferCleanup(restoreWritable)
				Expect(extract(dir("d/", 0755), symlink("d/up", ".."), takeover("d/up/", 0777))).To(Succeed())
				expectDestUntouched()
			})

			It("still applies a directory entry reached through a link to a subdirectory", func() {
				Expect(extract(dir("real/", 0755), symlink("alias", "real"), takeover("alias/", 0700))).To(Succeed())
				info, err := os.Stat(filepath.Join(dest, "real"))
				Expect(err).NotTo(HaveOccurred())
				Expect(info.Mode().Perm()).To(Equal(os.FileMode(0700)))
				Expect(info.ModTime()).To(Equal(archivedMTime))
				expectDestUntouched()
			})
		})

		It("refuses a link created through a symlinked directory that is later replaced", func() {
			// d/up -> .. is dest itself, so d/up/x physically lives at dest/x,
			// where ../z is outside. Replacing d/up afterwards leaves no name
			// that reaches dest/x, so the final check must track where the
			// link physically went, not what it was called.
			err := extract(
				dir("d/", 0755),
				symlink("d/up", ".."),
				symlink("d/up/x", "../z"),
				file("d/up", 0644, "replaced"),
			)
			expectBreakout(err)
			expectNoEscapingLinks()
		})

		It("refuses a hard link to a contained symlink that escapes from its new directory", func() {
			// d/s -> ../x resolves to dest/x. A hard link to it is the same
			// symlink inode at dest/top, where ../x is outside.
			err := extract(
				dir("d/", 0755),
				symlink("d/s", "../x"),
				hardlink("top", "d/s"),
			)
			expectBreakout(err)
			expectNoEscapingLinks()
		})

		It("refuses a hard link to a sibling directory sharing dest's prefix", func() {
			// The old guard was strings.HasPrefix(target, dest), which
			// base/dest-evil/secret satisfies. A later regular entry of the same
			// name would then truncate the outside file through the shared inode.
			err := extract(
				hardlink("stolen", "../dest-evil/secret"),
				file("stolen", 0644, "overwritten"),
			)
			expectBreakout(err)
			expectOutsideUntouched()
		})

		It("refuses a hard link with an absolute target", func() {
			err := extract(
				hardlink("stolen", filepath.Join(base, "dest-evil", "secret")),
				file("stolen", 0644, "overwritten"),
			)
			expectBreakout(err)
			expectOutsideUntouched()
		})

		It("refuses a hard link reached through a pre-existing symlinked directory", func() {
			Expect(os.Symlink(filepath.Join(base, "dest-evil"), filepath.Join(dest, "preexisting"))).To(Succeed())
			err := extract(
				hardlink("stolen", "preexisting/secret"),
				file("stolen", 0644, "overwritten"),
			)
			expectBreakout(err)
			expectOutsideUntouched()
		})

		It("replaces a pre-existing symlink rather than writing through it", func() {
			Expect(os.Symlink(filepath.Join(base, "dest-evil", "secret"), filepath.Join(dest, "config"))).To(Succeed())
			Expect(extract(file("config", 0644, "from-archive"))).To(Succeed())
			expectOutsideUntouched()

			info, err := os.Lstat(filepath.Join(dest, "config"))
			Expect(err).NotTo(HaveOccurred())
			Expect(info.Mode().IsRegular()).To(BeTrue())
		})

		It("refuses device nodes and FIFOs", func() {
			if runtime.GOOS == "windows" {
				Skip("no device nodes on windows")
			}
			for _, typ := range []byte{tar.TypeChar, tar.TypeBlock, tar.TypeFifo} {
				err := extract(entry{hdr: tar.Header{Typeflag: typ, Name: "node", Mode: 0666, Devmajor: 1, Devminor: 3}})
				Expect(err).To(HaveOccurred(), "type %q", typ)
				_, statErr := os.Lstat(filepath.Join(dest, "node"))
				Expect(os.IsNotExist(statErr)).To(BeTrue(), "type %q left a node on disk", typ)
			}
		})

		Describe("links whose absolute path exceeds the OS path limit", func() {
			// A path longer than PATH_MAX (darwin 1024, linux 4096) cannot be
			// handed to a path-taking syscall at all, yet the kernel follows
			// a short link into such a tree, one component at a time, with no
			// limit on the total. So a check that stats or reads links by
			// absolute path is blind exactly where the archive chooses to put
			// them.
			const width = 250 // under NAME_MAX, so every component is creatable
			const perGroup = 3
			const groups = 6 // 18 components, over 4096 bytes on any platform

			var (
				group [groups]string // perGroup components each
				deep  string         // every group, in order
			)
			for g := range group {
				parts := make([]string, perGroup)
				for i := range parts {
					parts[i] = strings.Repeat(string(rune('a'+g*perGroup+i)), width)
				}
				group[g] = strings.Join(parts, "/")
			}
			deep = strings.Join(group[:], "/")
			depth := groups * perGroup

			// relays lets the kernel reach the deep end through short link
			// targets: s -> the first group, and in each group's last
			// directory t -> the next group. "s/t/t/t/t/t" is deep.
			relays := func() []entry {
				entries := []entry{dir(deep+"/", 0755), symlink("s", group[0])}
				prefix := group[0]
				for g := 1; g < groups; g++ {
					entries = append(entries, symlink(prefix+"/t", group[g]))
					prefix += "/" + group[g]
				}
				return entries
			}
			relayed := "s" + strings.Repeat("/t", groups-1)

			// expectNoEscapingLinksThroughRoot is expectNoEscapingLinks for a
			// tree too deep for absolute paths: it walks dest through an
			// os.Root and asks the root, which resolves one component at a
			// time, whether each symlink leads out.
			expectNoEscapingLinksThroughRoot := func() {
				root, err := os.OpenRoot(dest)
				ExpectWithOffset(1, err).NotTo(HaveOccurred())
				defer root.Close()

				links := 0
				err = fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
					if err != nil {
						return err
					}
					if d.Type()&fs.ModeSymlink == 0 {
						return nil
					}
					links++
					_, statErr := root.Stat(p)
					if statErr != nil && strings.Contains(statErr.Error(), "path escapes from parent") {
						return fmt.Errorf("symlink %s resolves outside dest", p)
					}
					return nil
				})
				ExpectWithOffset(1, err).NotTo(HaveOccurred())
				ExpectWithOffset(1, links).To(BeNumerically(">", 0), "the walk saw no links; it proves nothing")
			}

			BeforeEach(func() {
				if len(dest)+len(deep) <= 4096 {
					Fail("the deep tree does not exceed PATH_MAX")
				}
			})

			It("refuses a link that climbs out through a link the absolute path cannot reach", func() {
				// deep/z -> dest, so deep/z/.. is base. Lexically, esc is
				// deep/z/.. = deep, inside; only resolving deep/z shows it.
				entries := append(relays(),
					symlink(deep+"/z", strings.TrimSuffix(strings.Repeat("../", depth), "/")),
					symlink("esc", relayed+"/z/.."),
				)
				err := extract(entries...)

				expectLinkRefused(err, "esc")
				expectNoEscapingLinksThroughRoot()

				// What the refusal protects: a write through what the
				// extraction left must not land beside dest.
				_ = os.WriteFile(filepath.Join(dest, "esc", "victim.txt"), []byte("pwned"), 0644)
				expectOutsideUntouched()
			})

			It("refuses a link created through a link the absolute path cannot reach", func() {
				// deep/z -> dest, so deep/z/y is physically dest/y, where
				// ../outside, contained from deep/z, is beside dest. Only
				// resolving deep/z finds where y really is.
				entries := append(relays(),
					symlink(deep+"/z", strings.TrimSuffix(strings.Repeat("../", depth), "/")),
					symlink(deep+"/z/y", "../outside"),
				)
				err := extract(entries...)

				expectLinkRefused(err, "y")
				expectNoEscapingLinksThroughRoot()

				_ = os.WriteFile(filepath.Join(dest, "y", "victim.txt"), []byte("pwned"), 0644)
				expectOutsideUntouched()
			})
		})
	})

	Describe("legitimate archives", func() {
		It("extracts nested files, directories, modes, internal symlinks and hard links", func() {
			Expect(extract(
				dir("./", 0755),
				dir("real/", 0750),
				file("real/file", 0640, "real-contents"),
				dir("real/sub/", 0755),
				file("real/sub/nested", 0600, "nested-contents"),
				symlink("lib", "./real"),
				symlink("real/sub/up", "../file"),
				symlink("real/sub/top", ".."),
				hardlink("hard", "real/file"),
				hardlink("real/sub/hard", "real/file"),
				file("implicit/parent/file", 0755, "exe"),
			)).To(Succeed())

			read := func(p string) string {
				b, err := os.ReadFile(filepath.Join(dest, p))
				ExpectWithOffset(1, err).NotTo(HaveOccurred())
				return string(b)
			}
			Expect(read("real/file")).To(Equal("real-contents"))
			Expect(read("real/sub/nested")).To(Equal("nested-contents"))
			Expect(read("lib/file")).To(Equal("real-contents"))
			Expect(read("lib/sub/nested")).To(Equal("nested-contents"))
			Expect(read("real/sub/up")).To(Equal("real-contents"))
			Expect(read("real/sub/top/file")).To(Equal("real-contents"))
			Expect(read("hard")).To(Equal("real-contents"))
			Expect(read("real/sub/hard")).To(Equal("real-contents"))
			Expect(read("implicit/parent/file")).To(Equal("exe"))

			target, err := os.Readlink(filepath.Join(dest, "lib"))
			Expect(err).NotTo(HaveOccurred())
			Expect(target).To(Equal("./real"))

			if runtime.GOOS != "windows" {
				mode := func(p string) os.FileMode {
					info, err := os.Lstat(filepath.Join(dest, p))
					ExpectWithOffset(1, err).NotTo(HaveOccurred())
					return info.Mode().Perm()
				}
				Expect(mode("real")).To(Equal(os.FileMode(0750)))
				Expect(mode("real/sub/nested")).To(Equal(os.FileMode(0600)))
				Expect(mode("implicit/parent/file")).To(Equal(os.FileMode(0755)))
				// The hard link shares real/file's inode; its own header mode
				// (0644) must not re-chmod the file it links to.
				Expect(mode("hard")).To(Equal(os.FileMode(0640)))

				realStat, err := os.Stat(filepath.Join(dest, "real/file"))
				Expect(err).NotTo(HaveOccurred())
				hardStat, err := os.Stat(filepath.Join(dest, "hard"))
				Expect(err).NotTo(HaveOccurred())
				Expect(os.SameFile(realStat, hardStat)).To(BeTrue())
			}

			expectNoEscapingLinks()
		})

		It("preserves the modification time of files", func() {
			e := file("timed", 0644, "x")
			e.hdr.ModTime = time.Unix(98765, 0)
			e.hdr.AccessTime = time.Unix(12345, 0)
			Expect(extract(e)).To(Succeed())

			info, err := os.Stat(filepath.Join(dest, "timed"))
			Expect(err).NotTo(HaveOccurred())
			Expect(info.ModTime()).To(Equal(time.Unix(98765, 0)))
		})

		It("strips setuid and setgid bits but keeps the permission bits", func() {
			if runtime.GOOS == "windows" {
				Skip("no setuid on windows")
			}
			Expect(extract(file("suid", 06755, "x"), dir("sgid-dir/", 02775))).To(Succeed())

			info, err := os.Stat(filepath.Join(dest, "suid"))
			Expect(err).NotTo(HaveOccurred())
			Expect(info.Mode() & (os.ModeSetuid | os.ModeSetgid)).To(BeZero())
			Expect(info.Mode().Perm()).To(Equal(os.FileMode(0755)))

			info, err = os.Stat(filepath.Join(dest, "sgid-dir"))
			Expect(err).NotTo(HaveOccurred())
			Expect(info.Mode() & os.ModeSetgid).To(BeZero())
		})

		It("fills read-only directories before making them read-only", func() {
			if runtime.GOOS == "windows" {
				Skip("directory permission bits do not restrict writes on windows")
			}
			if os.Getuid() == 0 {
				Skip("root bypasses directory permissions, so a read-only directory cannot block the write this spec is about")
			}
			DeferCleanup(restoreWritable)

			readonly := dir("readonly/", 0555)
			readonly.hdr.ModTime = time.Unix(55555, 0)
			Expect(extract(
				readonly,
				file("readonly/file", 0644, "contents"),
				dir("readonly/nested/", 0500),
				file("readonly/nested/deep", 0400, "deep"),
				symlink("readonly/link", "file"),
			)).To(Succeed())

			b, err := os.ReadFile(filepath.Join(dest, "readonly", "nested", "deep"))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(b)).To(Equal("deep"))

			info, err := os.Stat(filepath.Join(dest, "readonly"))
			Expect(err).NotTo(HaveOccurred())
			Expect(info.Mode().Perm()).To(Equal(os.FileMode(0555)))
			// Applied last, so writing the children does not bump it.
			Expect(info.ModTime()).To(Equal(time.Unix(55555, 0)))

			info, err = os.Stat(filepath.Join(dest, "readonly", "nested"))
			Expect(err).NotTo(HaveOccurred())
			Expect(info.Mode().Perm()).To(Equal(os.FileMode(0500)))
		})

		It("leaves read-only directories owner-writable when extraction fails, so the partial tree can be removed", func() {
			if runtime.GOOS == "windows" {
				Skip("directory permission bits do not restrict writes on windows")
			}
			if os.Getuid() == 0 {
				Skip("root bypasses directory permissions, so removability cannot be observed")
			}
			DeferCleanup(restoreWritable)

			readonly := dir("readonly/", 0555)
			// Filled before it is restricted, so this fails on ../x, not on
			// the write into readonly/.
			Expect(extract(
				readonly,
				file("readonly/file", 0644, "contents"),
				file("../x", 0644, "pwned"),
			)).NotTo(Succeed())

			Expect(os.RemoveAll(dest)).To(Succeed())
		})

		It("checks many links in a deep tree in bounded time", func() {
			// Each link's physical location and target are resolved against
			// the real tree. Re-resolving every growing prefix of a
			// 400-deep path for each of 200 links took minutes.
			deep := strings.TrimSuffix(strings.Repeat("a/", 400), "/")
			entries := []entry{dir(deep+"/", 0755)}
			for i := 0; i < 200; i++ {
				entries = append(entries, symlink(fmt.Sprintf("%s/l%d", deep, i), "x"))
			}
			archive := tarOf(entries...)

			start := time.Now()
			Expect(tarfs.Extract(archive, dest)).To(Succeed())
			Expect(time.Since(start)).To(BeNumerically("<", 3*time.Second))

			target, err := os.Readlink(filepath.Join(dest, filepath.FromSlash(deep), "l199"))
			Expect(err).NotTo(HaveOccurred())
			Expect(target).To(Equal("x"))
		})

		It("follows a directory name to where it leads now, not where it led for the previous entry", func() {
			// s/s replaces the link s itself (s -> . puts s/s at dest/s), so
			// s no longer names a directory and s/y has nowhere to go.
			err := extract(
				symlink("s", "."),
				file("s/s", 0644, "replaced"),
				file("s/y", 0644, "misplaced"),
			)
			Expect(err).To(HaveOccurred())
			_, statErr := os.Lstat(filepath.Join(dest, "y"))
			Expect(os.IsNotExist(statErr)).To(BeTrue(), "s/y was written through the replaced link")
		})

		It("tolerates symlink loops, which resolve nowhere", func() {
			Expect(extract(
				symlink("a", "b"),
				symlink("b", "a"),
				symlink("c", "a/x"),
			)).To(Succeed())
			expectNoEscapingLinks()
		})

		It("creates dest when it does not exist yet", func() {
			Expect(os.RemoveAll(dest)).To(Succeed())
			Expect(extract(file("a/b", 0644, "x"))).To(Succeed())
			b, err := os.ReadFile(filepath.Join(dest, "a", "b"))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(b)).To(Equal("x"))
		})
	})
})
