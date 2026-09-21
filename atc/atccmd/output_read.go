package atccmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/atc/runs"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// runResultScratchChild is the private directory result reads spool into,
// inside --run-result-scratch-dir.
const runResultScratchChild = "run-results"

func (cmd *RunCommand) validateRunResultReads() error {
	if cmd.RunResultReadConcurrency < 1 {
		return errors.New("--run-result-read-concurrency must be at least 1")
	}
	if cmd.RunResultScratchDir == "" {
		return nil
	}
	if !filepath.IsAbs(cmd.RunResultScratchDir) {
		return fmt.Errorf("--run-result-scratch-dir %q must be absolute", cmd.RunResultScratchDir)
	}
	info, err := os.Stat(cmd.RunResultScratchDir)
	if err != nil {
		return fmt.Errorf("--run-result-scratch-dir: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("--run-result-scratch-dir %q is not a directory", cmd.RunResultScratchDir)
	}
	return nil
}

// prepareRunResultScratch returns the directory result reads spool into.
//
// A Kubernetes emptyDir on disk is created 0777 without the sticky bit, which
// the canonicalizer refuses as a temporary parent. So web makes its own 0700
// child, removing whatever a previous process in the same Pod left there: a
// crashed read's spool is otherwise charged to the volume's sizeLimit until the
// Pod is deleted. The parent must not be writable by another uid in the
// container; web is the only process in its container.
func (cmd *RunCommand) prepareRunResultScratch() (string, error) {
	if cmd.RunResultScratchDir == "" {
		return "", nil
	}
	child := filepath.Join(cmd.RunResultScratchDir, runResultScratchChild)
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
	if cmd.outputReadSigner == nil {
		return nil
	}
	scratch, err := cmd.prepareRunResultScratch()
	if err != nil {
		return err
	}
	cmd.runResultReader = &runs.ResultReader{Conn: conn, Minter: cmd.outputReadSigner, Scratch: scratch,
		Source: func(ctx context.Context, epoch executioncontrol.ActivationEpoch) (runs.ResultSource, error) {
			return source.ForResultRead(ctx, epoch)
		}}
	control := &hangaroutput.LeaseControl{Transactor: hangarOutputTransactor{conn: conn},
		Leases:   db.NewHangarOutputRepository(db.HangarConsumerPrefixForComponent()),
		Warrants: cmd.outputReadVerifier, Minter: cmd.outputReadSigner, Clock: output.ClockFunc(func() time.Time { return time.Now().UTC() }),
		Keys: &hangaroutput.ReadNodeKeys{Nodes: source, Membership: db.OutputNodeKeys{Conn: conn}, Ring: cmd.hangarOutputControlKeys}}
	cmd.outputLeaseHandler = control.Handler()
	return nil
}
