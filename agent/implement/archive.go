package implement

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"

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
	err = capture.WriteArchive(ctx, destination, root, RunInputSnapshotDir, manifest, current.inventory)
	if errors.Is(err, capture.ErrChangedDuringUpload) {
		return errors.New("implementation input changed during upload")
	}
	return err
}
