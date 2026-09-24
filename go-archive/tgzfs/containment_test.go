package tgzfs_test

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"

	"github.com/klauspost/compress/gzip"

	"github.com/concourse/concourse/go-archive/tarfs"
	"github.com/concourse/concourse/go-archive/tgzfs"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func tgzOf(hdrs ...tar.Header) io.Reader {
	buf := new(bytes.Buffer)
	gw := gzip.NewWriter(buf)
	tw := tar.NewWriter(gw)
	for _, hdr := range hdrs {
		body := ""
		if hdr.Typeflag == tar.TypeReg {
			body = "pwned"
			hdr.Size = int64(len(body))
		}
		Expect(tw.WriteHeader(&hdr)).To(Succeed())
		_, err := tw.Write([]byte(body))
		Expect(err).NotTo(HaveOccurred())
	}
	Expect(tw.Close()).To(Succeed())
	Expect(gw.Close()).To(Succeed())
	return buf
}

// Extract is what `fly execute --output` feeds a task's output through, so
// these specs pin containment at that boundary, whatever tools the host has.
var _ = Describe("Extract containment", func() {
	var base, dest string

	BeforeEach(func() {
		var err error
		base, err = os.MkdirTemp("", "tgzfs-containment")
		Expect(err).NotTo(HaveOccurred())
		base, err = filepath.EvalSymlinks(base)
		Expect(err).NotTo(HaveOccurred())
		dest = filepath.Join(base, "dest")
		Expect(os.Mkdir(filepath.Join(base, "outside"), 0755)).To(Succeed())
	})

	AfterEach(func() {
		os.RemoveAll(base)
	})

	expectOutsideUntouched := func() {
		_, err := os.Lstat(filepath.Join(base, "victim.txt"))
		ExpectWithOffset(1, os.IsNotExist(err)).To(BeTrue(), "victim.txt was written outside dest")
		entries, err := os.ReadDir(filepath.Join(base, "outside"))
		ExpectWithOffset(1, err).NotTo(HaveOccurred())
		ExpectWithOffset(1, entries).To(BeEmpty(), "something was written into the outside dir")
	}

	// Extract never shells out, so the host's PATH cannot change these; the
	// full escape matrix lives in tarfs.
	It("refuses a ../ entry name", func() {
		err := tgzfs.Extract(tgzOf(tar.Header{Typeflag: tar.TypeReg, Name: "../victim.txt", Mode: 0644}), dest)
		Expect(err).To(BeAssignableToTypeOf(tarfs.BreakoutError{}))
		expectOutsideUntouched()
	})

	It("refuses a symlink that climbs out, and anything written through it", func() {
		err := tgzfs.Extract(tgzOf(
			tar.Header{Typeflag: tar.TypeSymlink, Name: "evil", Linkname: "../outside", Mode: 0777},
			tar.Header{Typeflag: tar.TypeReg, Name: "evil/pwned", Mode: 0644},
		), dest)
		Expect(err).To(BeAssignableToTypeOf(tarfs.BreakoutError{}))
		expectOutsideUntouched()
	})

	It("refuses an absolute symlink", func() {
		err := tgzfs.Extract(tgzOf(
			tar.Header{Typeflag: tar.TypeSymlink, Name: "abs", Linkname: filepath.Join(base, "outside"), Mode: 0777},
		), dest)
		Expect(err).To(BeAssignableToTypeOf(tarfs.BreakoutError{}))
		_, statErr := os.Lstat(filepath.Join(dest, "abs"))
		Expect(os.IsNotExist(statErr)).To(BeTrue(), "the absolute symlink was left on disk")
	})
})
