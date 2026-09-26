package implement

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sort"

	"github.com/concourse/concourse/agent/capture"
)

// RunInputSnapshotDir is the directory the snapshot occupies inside its Run
// input. The template reads source/snapshot.
const RunInputSnapshotDir = "snapshot"

// WriteRunInputArchive streams the verified snapshot below snapshot/. Hangar
// adds its materialization receipt at the input root, outside the closed
// snapshot. Every file is read and digested again as it is written, so a
// snapshot that changed after it was loaded is refused rather than uploaded.
func (s *Snapshot) WriteRunInputArchive(ctx context.Context, destination io.Writer) error {
	current, err := LoadSnapshot(s.Dir)
	if err != nil {
		return err
	}
	if current.Digest != s.Digest {
		return errors.New("implementation input changed before upload")
	}
	root, err := os.OpenRoot(current.Dir)
	if err != nil {
		return err
	}
	defer root.Close()
	manifest, err := json.Marshal(current.Manifest)
	if err != nil {
		return err
	}
	expected := map[string]string{"manifest.json": capture.Digest(manifest), "brief.md": current.Manifest.BriefDigest}
	limits := map[string]int64{"brief.md": MaxBriefBytes}
	if d := current.Manifest.PriorPatchDigest; d != nil {
		expected[PriorFile], limits[PriorFile] = *d, MaxPatchBytes
	}
	if d := current.Manifest.FindingsDigest; d != nil {
		expected[FindingsFile], limits[FindingsFile] = *d, MaxFindingsBytes
	}
	for _, file := range current.Manifest.Files {
		expected["base/"+file.Path] = file.Digest
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
			limit, bounded := limits[name]
			if !bounded {
				limit = capture.MaxFileBytes
			}
			data, err = capture.ReadRootFile(root, name, limit)
			if err != nil {
				return err
			}
		}
		if capture.Digest(data) != expected[name] {
			return errors.New("implementation input changed during upload")
		}
		if err := archive.WriteHeader(&tar.Header{Name: RunInputSnapshotDir + "/" + name, Mode: 0600, Size: int64(len(data)), Typeflag: tar.TypeReg, Format: tar.FormatPAX}); err != nil {
			return err
		}
		if _, err := archive.Write(data); err != nil {
			return err
		}
	}
	return archive.Close()
}
