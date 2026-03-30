package native

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/s2"
	"github.com/klauspost/compress/zstd"
)

// Compile-time check that Volume satisfies runtime.Volume.
var _ runtime.Volume = (*Volume)(nil)

// Volume implements runtime.Volume backed by a local filesystem directory.
type Volume struct {
	dbVolume   db.CreatedVolume
	handle     string
	workerName string
	path       string // absolute path on disk
}

// NewVolume creates a Volume backed by a local directory.
func NewVolume(handle, workerName, path string, dbVolume db.CreatedVolume) *Volume {
	return &Volume{
		handle:     handle,
		workerName: workerName,
		path:       path,
		dbVolume:   dbVolume,
	}
}

func (v *Volume) Handle() string {
	if v.dbVolume != nil {
		return v.dbVolume.Handle()
	}
	return v.handle
}

func (v *Volume) Source() string {
	if v.dbVolume != nil {
		return v.dbVolume.WorkerName()
	}
	return v.workerName
}

func (v *Volume) DBVolume() db.CreatedVolume {
	return v.dbVolume
}

// Path returns the absolute filesystem path backing this volume.
func (v *Volume) Path() string {
	return v.path
}

// StreamIn accepts a (possibly compressed) tar stream and extracts it into the
// volume at the given sub-path. The compression parameter determines how to
// decompress the incoming stream. Format is compatible with the K8s volume
// implementation so cross-worker streaming works transparently.
func (v *Volume) StreamIn(ctx context.Context, path string, enc compression.Compression, limitInMB float64, reader io.Reader) error {
	targetPath := v.resolvedPath(path)
	if err := os.MkdirAll(targetPath, 0755); err != nil {
		return fmt.Errorf("stream in: create target dir: %w", err)
	}

	// Decompress if needed.
	actualReader := reader
	if enc != nil && enc.Encoding() != compression.RawEncoding {
		rc, ok := reader.(io.ReadCloser)
		if !ok {
			rc = io.NopCloser(reader)
		}
		decompressed, err := enc.NewReader(rc)
		if err != nil {
			return fmt.Errorf("stream in: create decompressor: %w", err)
		}
		defer decompressed.Close()
		actualReader = decompressed
	}

	// Track bytes written for limit enforcement.
	var bytesWritten int64
	limitBytes := int64(limitInMB * 1024 * 1024)

	tr := tar.NewReader(actualReader)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("stream in: read tar header: %w", err)
		}

		// Sanitize the path to prevent directory traversal.
		cleanName := filepath.Clean(header.Name)
		if strings.HasPrefix(cleanName, "..") {
			return fmt.Errorf("stream in: invalid tar path %q", header.Name)
		}
		target := filepath.Join(targetPath, cleanName)

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(header.Mode)); err != nil {
				return fmt.Errorf("stream in: create dir %q: %w", cleanName, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return fmt.Errorf("stream in: create parent dir for %q: %w", cleanName, err)
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return fmt.Errorf("stream in: create file %q: %w", cleanName, err)
			}
			n, err := io.Copy(f, tr)
			f.Close()
			if err != nil {
				return fmt.Errorf("stream in: write file %q: %w", cleanName, err)
			}
			bytesWritten += n
			if limitBytes > 0 && bytesWritten > limitBytes {
				return fmt.Errorf("stream in: exceeded limit of %.0f MB", limitInMB)
			}
		case tar.TypeSymlink:
			if err := os.Symlink(header.Linkname, target); err != nil {
				return fmt.Errorf("stream in: create symlink %q: %w", cleanName, err)
			}
		case tar.TypeLink:
			linkTarget := filepath.Join(targetPath, filepath.Clean(header.Linkname))
			if err := os.Link(linkTarget, target); err != nil {
				return fmt.Errorf("stream in: create hard link %q: %w", cleanName, err)
			}
		}
	}

	return nil
}

// StreamOut creates a (possibly compressed) tar stream of the volume contents
// at the given sub-path. Format is compatible with the K8s volume so
// cross-worker streaming works transparently.
func (v *Volume) StreamOut(ctx context.Context, path string, enc compression.Compression) (io.ReadCloser, error) {
	targetPath := v.resolvedPath(path)

	// Verify the path exists.
	info, err := os.Stat(targetPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, runtime.ErrFileNotFound
		}
		return nil, fmt.Errorf("stream out: stat %q: %w", targetPath, err)
	}

	pr, pw := io.Pipe()

	needsCompression := enc != nil && enc.Encoding() != compression.RawEncoding

	go func() {
		var tarDest io.Writer = pw
		var compressor io.WriteCloser

		if needsCompression {
			compressor = newCompressWriter(pw, enc.Encoding())
			tarDest = compressor
		}

		tw := tar.NewWriter(tarDest)

		var walkErr error
		if info.IsDir() {
			walkErr = filepath.Walk(targetPath, func(filePath string, fi os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				return addToTar(tw, targetPath, filePath, fi)
			})
		} else {
			// Single file: tar it relative to its parent.
			walkErr = addToTar(tw, filepath.Dir(targetPath), targetPath, info)
		}

		if closeErr := tw.Close(); closeErr != nil && walkErr == nil {
			walkErr = closeErr
		}
		if compressor != nil {
			if closeErr := compressor.Close(); closeErr != nil && walkErr == nil {
				walkErr = closeErr
			}
		}
		pw.CloseWithError(walkErr)
	}()

	return pr, nil
}

func (v *Volume) InitializeResourceCache(ctx context.Context, cache db.ResourceCache) (*db.UsedWorkerResourceCache, error) {
	if v.dbVolume == nil {
		return nil, nil
	}
	return v.dbVolume.InitializeResourceCache(cache)
}

func (v *Volume) InitializeStreamedResourceCache(ctx context.Context, cache db.ResourceCache, sourceWorkerResourceCacheID int) (*db.UsedWorkerResourceCache, error) {
	if v.dbVolume == nil {
		return nil, nil
	}
	return v.dbVolume.InitializeStreamedResourceCache(cache, sourceWorkerResourceCacheID)
}

func (v *Volume) InitializeTaskCache(ctx context.Context, jobID int, stepName string, path string, privileged bool) error {
	if v.dbVolume == nil {
		return nil
	}
	return v.dbVolume.InitializeTaskCache(jobID, stepName, path)
}

func (v *Volume) resolvedPath(path string) string {
	if path == "." || path == "" {
		return v.path
	}
	return filepath.Join(v.path, path)
}

// addToTar adds a single file or directory entry to a tar writer. Paths in
// the archive are relative to baseDir.
func addToTar(tw *tar.Writer, baseDir, filePath string, fi os.FileInfo) error {
	relPath, err := filepath.Rel(baseDir, filePath)
	if err != nil {
		return err
	}

	// Resolve symlink target for the header.
	var link string
	if fi.Mode()&os.ModeSymlink != 0 {
		link, err = os.Readlink(filePath)
		if err != nil {
			return err
		}
	}

	header, err := tar.FileInfoHeader(fi, link)
	if err != nil {
		return err
	}
	header.Name = relPath

	if err := tw.WriteHeader(header); err != nil {
		return err
	}

	if fi.Mode().IsRegular() {
		f, err := os.Open(filePath)
		if err != nil {
			return err
		}
		defer f.Close()
		if _, err := io.Copy(tw, f); err != nil {
			return err
		}
	}

	return nil
}

// newCompressWriter creates a compressing io.WriteCloser for the given encoding.
// Mirrors the jetbridge implementation for format compatibility.
func newCompressWriter(w io.Writer, encoding compression.Encoding) io.WriteCloser {
	switch encoding {
	case compression.ZstdEncoding:
		enc, err := zstd.NewWriter(w)
		if err != nil {
			panic(fmt.Sprintf("zstd.NewWriter: %v", err))
		}
		return enc
	case compression.S2Encoding:
		return s2.NewWriter(w)
	default:
		return gzip.NewWriter(w)
	}
}
