package review

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/concourse/concourse/agent/capture"
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
	root, err := os.OpenRoot(current.Dir)
	if err != nil {
		return err
	}
	defer root.Close()
	manifest, err := json.Marshal(current.Manifest)
	if err != nil {
		return err
	}
	err = capture.WriteArchive(ctx, destination, root, RunInputBundleDir, manifest, current.inventory)
	if errors.Is(err, capture.ErrChangedDuringUpload) {
		return errors.New("review input changed during upload")
	}
	return err
}
