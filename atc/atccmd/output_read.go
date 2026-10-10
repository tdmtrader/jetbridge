package atccmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runs"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/hangar"
)

const (
	runResultScratchDir   = "/var/concourse/run-result-scratch"
	runResultScratchChild = "run-results"
)

// prepareRunResultScratch returns the directory result reads spool into.
//
// A Kubernetes emptyDir on disk is created 0777 without the sticky bit, which
// the canonicalizer refuses as a temporary parent. So web makes its own 0700
// child, removing whatever a previous process in the same Pod left there: a
// crashed read's spool is otherwise charged to the volume's sizeLimit until the
// Pod is deleted. The parent must not be writable by another uid in the
// container; web is the only process in its container.
func prepareRunResultScratch(parent string) (string, error) {
	child := filepath.Join(parent, runResultScratchChild)
	if err := os.RemoveAll(child); err != nil {
		return "", fmt.Errorf("clear Run result scratch: %w", err)
	}
	if err := os.Mkdir(child, 0700); err != nil {
		return "", fmt.Errorf("create Run result scratch: %w", err)
	}
	// Mkdir is subject to the umask; state the mode rather than trust it.
	if err := os.Chmod(child, 0700); err != nil {
		return "", fmt.Errorf("restrict Run result scratch: %w", err)
	}
	if err := hangar.ValidateTempDir(child); err != nil {
		return "", err
	}
	return child, nil
}

func (cmd *RunCommand) configureOutputReads(conn db.DbConn, source *jetbridge.OutputSource) error {
	if cmd.readWarrantMinter == nil {
		return nil
	}
	scratch, err := prepareRunResultScratch(runResultScratchDir)
	if err != nil {
		return err
	}
	cmd.runResultReader = &runs.ResultReader{Conn: conn, Minter: cmd.readWarrantMinter, Scratch: scratch,
		Source: func(ctx context.Context) (runs.ResultSource, error) {
			return source.ForResultRead(ctx)
		}}
	return nil
}
