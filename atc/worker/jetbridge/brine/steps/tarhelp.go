package steps

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"path"
)

// tarOfOneFile builds the archive a caller would hand to StreamIn.
func tarOfOneFile(name, content string) (io.Reader, error) {
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	if err := tw.WriteHeader(&tar.Header{
		Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg,
	}); err != nil {
		return nil, fmt.Errorf("write tar header: %w", err)
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		return nil, fmt.Errorf("write tar body: %w", err)
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("close tar: %w", err)
	}

	var gzipped bytes.Buffer
	zw := gzip.NewWriter(&gzipped)
	if _, err := io.Copy(zw, &raw); err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("close gzip: %w", err)
	}
	return &gzipped, nil
}

// filesInGzippedTar reads back what StreamOut produced.
func filesInGzippedTar(r io.Reader) (map[string]string, error) {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("open gzip: %w", err)
	}
	defer zr.Close()

	files := map[string]string{}
	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return files, nil
		}
		if err != nil {
			return nil, fmt.Errorf("read tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("read %q: %w", hdr.Name, err)
		}
		// tar names a member relative to -C, so "./x" and "x" are the same
		// member. path.Clean normalises tar's own convention; it does not
		// weaken the assertion, which is about content.
		files[path.Clean(hdr.Name)] = string(body)
	}
}
