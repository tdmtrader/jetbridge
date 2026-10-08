package atccmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/api/pipelinerunserver"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runinput"
	"github.com/concourse/concourse/atc/runs"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/skymarshal/skycmd"
)

func (cmd *RunCommand) validateRunInputSigningKey() error {
	cmd.runInputAuthority = nil
	if cmd.RunInputSigningKey == "" {
		return nil
	}
	if cmd.Kubernetes.Namespace == "" || !cmd.Kubernetes.OutputPlaneEnabled || !cmd.Kubernetes.OutputCaptureEnabled {
		return errors.New("--run-input-signing-key requires the Kubernetes output and capture planes")
	}
	readKey := func(path string) ([]byte, error) {
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		return io.ReadAll(io.LimitReader(file, 33))
	}
	key, err := readKey(cmd.RunInputSigningKey)
	if err != nil {
		return fmt.Errorf("read Run input signing key: %w", err)
	}
	defer clear(key)
	if len(key) != 32 {
		return errors.New("Run input signing key must contain exactly 32 raw bytes")
	}
	for _, path := range []string{cmd.Kubernetes.HangarKey, cmd.Kubernetes.ArtifactDaemonResolveCapabilityKey} {
		if path == "" {
			continue
		}
		other, err := readKey(path)
		if err != nil {
			return fmt.Errorf("check Run input key separation: %w", err)
		}
		same := bytes.Equal(key, other)
		clear(other)
		if same {
			return errors.New("Run input signing key must differ from the Hangar key and the resolve capability key")
		}
	}
	cmd.runInputAuthority, err = runinput.NewAuthority(key, time.Now)
	return err
}

func (cmd *RunCommand) configureRunInputUploads(conn db.DbConn, factory db.PipelineRunFactory, teams db.TeamFactory, source *jetbridge.OutputSource) error {
	cmd.runAdmitter = nil
	if cmd.runInputAuthority == nil {
		return nil
	}
	display, err := skycmd.NewSkyDisplayUserIdGenerator(cmd.DisplayUserIdPerConnector)
	if err != nil {
		return err
	}
	cmd.runAdmitter = runs.NewAdmitter(conn, factory, teams, display, cmd.customRoles)
	cmd.runAdmitter.SetOutputPlane(cmd.outputPlane())
	cmd.runAdmitter.SetSealedInputAuthority(cmd.runInputAuthority)
	cmd.runAdmitter.SetCredentialHandoffConfig(cmd.credentialHandoffConfig(source))
	cmd.runAdmitter.SetInputUploadConfig(runs.InputUploadConfig{Source: func(ctx context.Context) (runs.InputUploadNode, error) {
		client, uid, err := source.ForInputUpload(ctx)
		return runs.InputUploadNode{UID: uid, Publisher: client}, err
	}})
	return nil
}

func (cmd *RunCommand) validateRunCredentialWorkerImages() error {
	for _, image := range cmd.RunCredentialWorkerImages {
		if err := runs.ValidateCredentialWorkerImage(image); err != nil {
			return fmt.Errorf("--run-credential-worker-image: %w", err)
		}
	}
	return nil
}

// credentialHandoffConfig delivers only into the operator's pinned worker
// images. With none pinned, every handoff is refused.
func (cmd *RunCommand) credentialHandoffConfig(source runs.SessionTransport) runs.CredentialHandoffConfig {
	return runs.CredentialHandoffConfig{
		Source: source, Helper: "/usr/local/bin/jb-review-worker", Socket: "/dev/shm/jb-review/auth.sock", Lifetime: 32 * time.Minute,
		WorkerImages: append([]string(nil), cmd.RunCredentialWorkerImages...),
	}
}

// pipelineRunServices is what the v2 routes admit and read through. The port
// is built with the output plane when detached inputs and credential handoff
// are configured; otherwise it is built here without them. It is built even
// with admission off: a held node refuses new Runs through the port, and still
// replays the Run an invocation key already admitted. The fallback is built
// over this boot's connection and not kept on cmd, so a later boot of the same
// command never serves through a closed one.
func (cmd *RunCommand) pipelineRunServices(conn db.DbConn, factory db.PipelineRunFactory, teams db.TeamFactory) pipelinerunserver.Services {
	admitter := cmd.runAdmitter
	if admitter == nil {
		if display, err := skycmd.NewSkyDisplayUserIdGenerator(cmd.DisplayUserIdPerConnector); err == nil {
			admitter = runs.NewAdmitter(conn, factory, teams, display, cmd.customRoles)
			admitter.SetOutputPlane(cmd.outputPlane())
		}
	}
	return pipelinerunserver.Services{Results: cmd.runResultReader, Admitter: admitter, HangarOutput: cmd.outputPlane(),
		ResultReadConcurrency: cmd.RunResultReadConcurrency}
}

// outputPlane reports whether this web has a Hangar output plane, which Run
// admission may bind results and inputs through. It is not the Run activation
// epoch.
func (cmd *RunCommand) outputPlane() bool {
	return cmd.Kubernetes.OutputPlaneEnabled
}

// reconcilePipelineRunActivation moves the Run contract's own activation
// marker to agree with this web node's configuration: a positive
// --pipeline-run-activation-epoch admits Runs born under it, zero stops
// admission. It is the only supported writer of the marker, and the chart sets
// the epoch on every deploy. An epoch older than the recorded one refuses to
// start rather than admit under a downgraded capability.
func (cmd *RunCommand) reconcilePipelineRunActivation(logger lager.Logger, conn db.DbConn) error {
	state, err := db.ReconcilePipelineRunActivation(context.Background(), conn, cmd.PipelineRunActivationEpoch)
	if err != nil {
		return fmt.Errorf("reconciling pipeline run activation: %w", err)
	}
	logger.Info("pipeline-run-activation", lager.Data{"epoch": state.Epoch, "admitting": state.AdmissionEnabled})
	return nil
}
