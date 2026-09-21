package review

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sort"
)

const RunInputBundleDir = "bundle"

// WriteRunInputArchive streams the verified inventory below bundle/. Hangar
// adds its materialization receipt at the input root, outside the closed bundle.
// Raw repository symlinks remain captured text files; no Git hooks run here.
func (b *Bundle) WriteRunInputArchive(ctx context.Context, destination io.Writer) error {
	current, err := LoadBundle(b.Dir)
	if err != nil {
		return err
	}
	if current.Digest != b.Digest {
		return errors.New("review input changed before upload")
	}
	b = current
	root, err := os.OpenRoot(b.Dir)
	if err != nil {
		return err
	}
	defer root.Close()
	manifest, err := json.Marshal(b.Manifest)
	if err != nil {
		return err
	}
	expected := map[string]string{"manifest.json": digest(manifest), "change.diff": b.Manifest.DiffDigest}
	if b.Manifest.PlanDigest != nil {
		expected["plan.md"] = *b.Manifest.PlanDigest
	}
	for _, file := range b.Manifest.Files {
		expected[file.Side+"/"+file.Path] = file.Digest
	}
	names := make([]string, 0, len(expected))
	for name := range expected {
		names = append(names, name)
	}
	sort.Strings(names)
	archive := tar.NewWriter(destination)
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		data := manifest
		if name != "manifest.json" {
			limit := int64(maxFileBytes)
			if name == "change.diff" {
				limit = maxBundleBytes
			}
			data, err = readRootFile(root, name, limit)
			if err != nil {
				return err
			}
		}
		if digest(data) != expected[name] {
			return errors.New("review input changed during upload")
		}
		if err := archive.WriteHeader(&tar.Header{Name: RunInputBundleDir + "/" + name, Mode: 0600, Size: int64(len(data)), Typeflag: tar.TypeReg, Format: tar.FormatPAX}); err != nil {
			return err
		}
		if _, err := archive.Write(data); err != nil {
			return err
		}
	}
	return archive.Close()
}
