package native_test

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"

	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/native"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Volume", func() {
	var (
		ctx    context.Context
		tmpDir string
		volume *native.Volume
	)

	BeforeEach(func() {
		ctx = context.Background()
		tmpDir = GinkgoT().TempDir()
		volume = native.NewVolume("vol-handle", "native-worker", tmpDir, nil)
	})

	Describe("Handle", func() {
		It("returns the handle string", func() {
			Expect(volume.Handle()).To(Equal("vol-handle"))
		})

		Context("when dbVolume is present", func() {
			It("delegates to dbVolume", func() {
				fakeDBVolume := new(dbfakes.FakeCreatedVolume)
				fakeDBVolume.HandleReturns("db-handle")
				vol := native.NewVolume("fallback", "worker", tmpDir, fakeDBVolume)
				Expect(vol.Handle()).To(Equal("db-handle"))
			})
		})
	})

	Describe("Source", func() {
		It("returns the worker name", func() {
			Expect(volume.Source()).To(Equal("native-worker"))
		})

		Context("when dbVolume is present", func() {
			It("delegates to dbVolume", func() {
				fakeDBVolume := new(dbfakes.FakeCreatedVolume)
				fakeDBVolume.WorkerNameReturns("db-worker")
				vol := native.NewVolume("h", "fallback", tmpDir, fakeDBVolume)
				Expect(vol.Source()).To(Equal("db-worker"))
			})
		})
	})

	Describe("StreamIn", func() {
		It("extracts tar to target directory", func() {
			tarData := createTar(map[string]string{
				"hello.txt": "world",
				"sub/nested.txt": "content",
			})

			err := volume.StreamIn(ctx, ".", nil, 0, bytes.NewReader(tarData))
			Expect(err).ToNot(HaveOccurred())

			By("verifying files on disk")
			data, err := os.ReadFile(filepath.Join(tmpDir, "hello.txt"))
			Expect(err).ToNot(HaveOccurred())
			Expect(string(data)).To(Equal("world"))

			data, err = os.ReadFile(filepath.Join(tmpDir, "sub", "nested.txt"))
			Expect(err).ToNot(HaveOccurred())
			Expect(string(data)).To(Equal("content"))
		})

		It("decompresses gzip stream before extracting", func() {
			tarData := createTar(map[string]string{"gzipped.txt": "compressed"})
			gzipComp := compression.NewGzipCompression()

			// Compress the tar data.
			var compressed bytes.Buffer
			compWriter := newTestCompressWriter(&compressed)
			_, err := compWriter.Write(tarData)
			Expect(err).ToNot(HaveOccurred())
			Expect(compWriter.Close()).To(Succeed())

			err = volume.StreamIn(ctx, ".", gzipComp, 0, bytes.NewReader(compressed.Bytes()))
			Expect(err).ToNot(HaveOccurred())

			data, err := os.ReadFile(filepath.Join(tmpDir, "gzipped.txt"))
			Expect(err).ToNot(HaveOccurred())
			Expect(string(data)).To(Equal("compressed"))
		})

		It("handles raw encoding without decompression", func() {
			tarData := createTar(map[string]string{"raw.txt": "uncompressed"})
			rawComp := compression.NewNoCompression()

			err := volume.StreamIn(ctx, ".", rawComp, 0, bytes.NewReader(tarData))
			Expect(err).ToNot(HaveOccurred())

			data, err := os.ReadFile(filepath.Join(tmpDir, "raw.txt"))
			Expect(err).ToNot(HaveOccurred())
			Expect(string(data)).To(Equal("uncompressed"))
		})

		It("enforces limitInMB", func() {
			// Create a tar with content larger than limit.
			bigContent := make([]byte, 2*1024*1024) // 2 MB
			for i := range bigContent {
				bigContent[i] = 'x'
			}
			tarData := createTar(map[string]string{"big.txt": string(bigContent)})

			err := volume.StreamIn(ctx, ".", nil, 1, bytes.NewReader(tarData)) // 1 MB limit
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("exceeded limit"))
		})

		It("rejects directory traversal", func() {
			tarData := createTarWithPaths([]tarEntry{
				{name: "../etc/passwd", content: "hacked"},
			})

			err := volume.StreamIn(ctx, ".", nil, 0, bytes.NewReader(tarData))
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("invalid tar path"))
		})

		It("handles symlinks", func() {
			tarData := createTarWithPaths([]tarEntry{
				{name: "target.txt", content: "target content"},
				{name: "link.txt", linkTarget: "target.txt", typeflag: tar.TypeSymlink},
			})

			err := volume.StreamIn(ctx, ".", nil, 0, bytes.NewReader(tarData))
			Expect(err).ToNot(HaveOccurred())

			linkTarget, err := os.Readlink(filepath.Join(tmpDir, "link.txt"))
			Expect(err).ToNot(HaveOccurred())
			Expect(linkTarget).To(Equal("target.txt"))
		})

		It("creates target directory if missing", func() {
			tarData := createTar(map[string]string{"file.txt": "data"})

			subDir := filepath.Join(tmpDir, "new", "sub", "dir")
			subVol := native.NewVolume("h", "w", subDir, nil)
			err := subVol.StreamIn(ctx, ".", nil, 0, bytes.NewReader(tarData))
			Expect(err).ToNot(HaveOccurred())

			data, err := os.ReadFile(filepath.Join(subDir, "file.txt"))
			Expect(err).ToNot(HaveOccurred())
			Expect(string(data)).To(Equal("data"))
		})

		It("handles sub-path parameter", func() {
			tarData := createTar(map[string]string{"file.txt": "data"})

			err := volume.StreamIn(ctx, "sub/dir", nil, 0, bytes.NewReader(tarData))
			Expect(err).ToNot(HaveOccurred())

			data, err := os.ReadFile(filepath.Join(tmpDir, "sub", "dir", "file.txt"))
			Expect(err).ToNot(HaveOccurred())
			Expect(string(data)).To(Equal("data"))
		})
	})

	Describe("StreamOut", func() {
		BeforeEach(func() {
			Expect(os.WriteFile(filepath.Join(tmpDir, "out.txt"), []byte("output"), 0644)).To(Succeed())
			Expect(os.MkdirAll(filepath.Join(tmpDir, "subdir"), 0755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(tmpDir, "subdir", "nested.txt"), []byte("nested"), 0644)).To(Succeed())
		})

		It("creates tar from directory contents", func() {
			reader, err := volume.StreamOut(ctx, ".", nil)
			Expect(err).ToNot(HaveOccurred())
			defer reader.Close()

			files := extractTar(reader)
			Expect(files).To(HaveKey("out.txt"))
			Expect(files["out.txt"]).To(Equal("output"))
			Expect(files).To(HaveKey(filepath.Join("subdir", "nested.txt")))
		})

		It("compresses with gzip when compression specified", func() {
			gzipComp := compression.NewGzipCompression()
			reader, err := volume.StreamOut(ctx, ".", gzipComp)
			Expect(err).ToNot(HaveOccurred())
			defer reader.Close()

			// Should be valid gzip — decompress then read tar.
			compressed, err := io.ReadAll(reader)
			Expect(err).ToNot(HaveOccurred())

			decompressed, err := gzipComp.NewReader(io.NopCloser(bytes.NewReader(compressed)))
			Expect(err).ToNot(HaveOccurred())
			defer decompressed.Close()

			files := extractTar(decompressed)
			Expect(files).To(HaveKey("out.txt"))
			Expect(files["out.txt"]).To(Equal("output"))
		})

		It("returns raw tar when compression is nil", func() {
			reader, err := volume.StreamOut(ctx, ".", nil)
			Expect(err).ToNot(HaveOccurred())
			defer reader.Close()

			files := extractTar(reader)
			Expect(files).To(HaveKey("out.txt"))
		})

		It("returns ErrFileNotFound for nonexistent path", func() {
			_, err := volume.StreamOut(ctx, "nonexistent", nil)
			Expect(err).To(Equal(runtime.ErrFileNotFound))
		})

		It("handles single file", func() {
			reader, err := volume.StreamOut(ctx, "out.txt", nil)
			Expect(err).ToNot(HaveOccurred())
			defer reader.Close()

			files := extractTar(reader)
			Expect(files).To(HaveKey("out.txt"))
			Expect(files["out.txt"]).To(Equal("output"))
		})
	})

	Describe("StreamIn/StreamOut round-trip", func() {
		It("preserves file contents through round-trip", func() {
			By("writing files to source volume")
			srcDir := filepath.Join(GinkgoT().TempDir(), "src")
			Expect(os.MkdirAll(srcDir, 0755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(srcDir, "a.txt"), []byte("alpha"), 0644)).To(Succeed())
			Expect(os.MkdirAll(filepath.Join(srcDir, "deep", "path"), 0755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(srcDir, "deep", "path", "b.txt"), []byte("beta"), 0644)).To(Succeed())

			srcVol := native.NewVolume("src", "w", srcDir, nil)

			By("streaming out from source")
			reader, err := srcVol.StreamOut(ctx, ".", nil)
			Expect(err).ToNot(HaveOccurred())

			By("streaming into destination")
			dstDir := filepath.Join(GinkgoT().TempDir(), "dst")
			dstVol := native.NewVolume("dst", "w", dstDir, nil)
			err = dstVol.StreamIn(ctx, ".", nil, 0, reader)
			reader.Close()
			Expect(err).ToNot(HaveOccurred())

			By("verifying files are identical")
			data, err := os.ReadFile(filepath.Join(dstDir, "a.txt"))
			Expect(err).ToNot(HaveOccurred())
			Expect(string(data)).To(Equal("alpha"))

			data, err = os.ReadFile(filepath.Join(dstDir, "deep", "path", "b.txt"))
			Expect(err).ToNot(HaveOccurred())
			Expect(string(data)).To(Equal("beta"))
		})

		It("preserves file contents through compressed round-trip", func() {
			srcDir := filepath.Join(GinkgoT().TempDir(), "src")
			Expect(os.MkdirAll(srcDir, 0755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(srcDir, "file.txt"), []byte("compressed round-trip"), 0644)).To(Succeed())

			srcVol := native.NewVolume("src", "w", srcDir, nil)
			gzipComp := compression.NewGzipCompression()

			reader, err := srcVol.StreamOut(ctx, ".", gzipComp)
			Expect(err).ToNot(HaveOccurred())

			dstDir := filepath.Join(GinkgoT().TempDir(), "dst")
			dstVol := native.NewVolume("dst", "w", dstDir, nil)
			err = dstVol.StreamIn(ctx, ".", gzipComp, 0, reader)
			reader.Close()
			Expect(err).ToNot(HaveOccurred())

			data, err := os.ReadFile(filepath.Join(dstDir, "file.txt"))
			Expect(err).ToNot(HaveOccurred())
			Expect(string(data)).To(Equal("compressed round-trip"))
		})
	})

	Describe("InitializeResourceCache", func() {
		It("returns nil when dbVolume is nil", func() {
			result, err := volume.InitializeResourceCache(ctx, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(BeNil())
		})
	})

	Describe("InitializeTaskCache", func() {
		It("returns nil when dbVolume is nil", func() {
			err := volume.InitializeTaskCache(ctx, 1, "step", "path", false)
			Expect(err).ToNot(HaveOccurred())
		})
	})
})

// --- Test helpers ---

// createTar builds a tar archive from a map of path → content.
func createTar(files map[string]string) []byte {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, content := range files {
		// Create parent directory entries.
		dir := filepath.Dir(name)
		if dir != "." {
			_ = tw.WriteHeader(&tar.Header{
				Name:     dir + "/",
				Typeflag: tar.TypeDir,
				Mode:     0755,
			})
		}
		_ = tw.WriteHeader(&tar.Header{
			Name:     name,
			Size:     int64(len(content)),
			Mode:     0644,
			Typeflag: tar.TypeReg,
		})
		_, _ = tw.Write([]byte(content))
	}
	_ = tw.Close()
	return buf.Bytes()
}

type tarEntry struct {
	name       string
	content    string
	linkTarget string
	typeflag   byte
}

// createTarWithPaths builds a tar with explicit control over entry types.
func createTarWithPaths(entries []tarEntry) []byte {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		hdr := &tar.Header{
			Name:     e.name,
			Mode:     0644,
			Typeflag: e.typeflag,
		}
		if hdr.Typeflag == 0 {
			hdr.Typeflag = tar.TypeReg
		}
		if e.typeflag == tar.TypeSymlink {
			hdr.Linkname = e.linkTarget
			hdr.Size = 0
		} else {
			hdr.Size = int64(len(e.content))
		}
		_ = tw.WriteHeader(hdr)
		if e.typeflag != tar.TypeSymlink && e.typeflag != tar.TypeLink {
			_, _ = tw.Write([]byte(e.content))
		}
	}
	_ = tw.Close()
	return buf.Bytes()
}

// extractTar reads a tar stream and returns a map of path → content.
func extractTar(r io.Reader) map[string]string {
	files := make(map[string]string)
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		if hdr.Typeflag == tar.TypeReg {
			data, _ := io.ReadAll(tr)
			files[hdr.Name] = string(data)
		}
	}
	return files
}

// newTestCompressWriter creates a gzip writer for test data.
func newTestCompressWriter(w io.Writer) io.WriteCloser {
	return native.ExportNewCompressWriter(w, compression.GzipEncoding)
}
