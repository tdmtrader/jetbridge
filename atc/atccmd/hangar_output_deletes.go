package atccmd

import (
	"context"
	"fmt"

	"code.cloudfoundry.org/lager/v3"
	"github.com/google/uuid"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/hangarserver"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/atc/hangaroutput/reclaim"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// reconcileHangarEnabled moves the output plane's in-service row to this web
// node's configuration: --kubernetes-hangar-output-capture-enabled (the chart's
// hangarOutput.webEnabled) puts it in service, and its absence takes it out.
// Admission takes the row FOR SHARE, so the move waits for every admission in
// flight and every admission after it sees the new value. Taking it out is
// the first step of a drain; `fly hangar-status` reports the residue that
// remains.
func (cmd *RunCommand) reconcileHangarEnabled(logger lager.Logger, conn db.DbConn) error {
	enabled := cmd.Kubernetes.OutputPlaneEnabled && cmd.Kubernetes.OutputCaptureEnabled
	previous, moved, err := db.ReconcileHangarEnabled(context.Background(), conn, enabled)
	if err != nil {
		return fmt.Errorf("reconciling the Hangar output plane's in-service flag: %w", err)
	}
	if moved {
		// A flip is an operator act with fleet-wide effect: every admission
		// that needs the output plane starts or stops with it.
		logger.Error("hangar-output-in-service-flipped", fmt.Errorf(
			"hangar_enabled moved from %t to %t at this web's startup", previous, enabled),
			lager.Data{"from": previous, "to": enabled})
		return nil
	}
	logger.Info("hangar-output-enabled", lager.Data{"enabled": enabled})
	return nil
}

// hangarOutputStatusReader is the one status read: the metrics component and
// the admin API both use it.
func (cmd *RunCommand) hangarOutputStatusReader(dbConn db.DbConn) *hangaroutput.StatusReader {
	return &hangaroutput.StatusReader{
		Transactor: hangarOutputTransactor{conn: dbConn},
		Repository: db.NewHangarOutputRepository(db.HangarConsumerPrefixForComponent()),
	}
}

// hangarStatusSource is the admin API's view of the output plane: the same
// status read the metrics component publishes, and resolution of a finding by
// id. Nil on a web node with no output plane, and the routes then say so.
func (cmd *RunCommand) hangarStatusSource(dbConn db.DbConn) hangarserver.Source {
	if !cmd.Kubernetes.OutputPlaneEnabled {
		return nil
	}
	return hangarStatusSource{reader: cmd.hangarOutputStatusReader(dbConn), conn: dbConn}
}

type hangarStatusSource struct {
	reader *hangaroutput.StatusReader
	conn   db.DbConn
}

func (source hangarStatusSource) Read(ctx context.Context) (hangaroutput.Status, error) {
	return source.reader.Read(ctx)
}

func (source hangarStatusSource) Resolve(ctx context.Context, id int64) error {
	return hangaroutput.ResolveFinding(ctx, hangarOutputTransactor{conn: source.conn},
		db.NewHangarOutputRepository(db.HangarConsumerPrefixForComponent()), id)
}

// hangarOutputNamespace derives the output namespace the web's deleting passes
// work in, from the same authenticated configuration the daemons derive it
// from.
func (cmd *RunCommand) hangarOutputNamespace() (output.OutputNamespace, error) {
	return output.DeriveNamespace(output.NamespaceConfig{
		Store:             cmd.Kubernetes.OutputStore,
		StoreID:           cmd.Kubernetes.OutputStoreID,
		Bucket:            cmd.Kubernetes.OutputBucket,
		DeploymentPrefix:  cmd.Kubernetes.OutputPrefix,
		TenantID:          cmd.Kubernetes.OutputTenant,
		CacheBucket:       cmd.Kubernetes.CacheBucket,
		StrictInputBucket: cmd.Kubernetes.InputBucket,
		ActivationEpoch:   executioncontrol.ActivationEpoch(cmd.Kubernetes.OutputActivationEpoch),
	})
}

// hangarOutputDeleteComponents are the web's two deleting passes over the
// output namespace: hangar_reclaim (admission, delete, finalization) and
// hangar_orphan_sweep. Both run under one advisory lock (reclaim.LockID), so
// across every web replica at most one of them deletes at a time.
//
// They run whenever an output bucket is configured, in service or not: a
// drained plane still finalizes its reclaim jobs, and the sweep still
// collects what a crash left behind.
func (cmd *RunCommand) hangarOutputDeleteComponents(dbConn db.DbConn, locker lock.LockFactory) ([]RunnableComponent, error) {
	if !cmd.Kubernetes.OutputPlaneEnabled || cmd.Kubernetes.OutputBucket == "" {
		return nil, nil
	}
	if err := output.ValidatePublicationGrace(cmd.Kubernetes.OutputPublicationGrace); err != nil {
		return nil, fmt.Errorf("--kubernetes-hangar-output-publication-grace: %w", err)
	}
	namespace, err := cmd.hangarOutputNamespace()
	if err != nil {
		return nil, fmt.Errorf("deriving the output namespace: %w", err)
	}
	lister, deletes, closeStore, err := reclaim.OpenStore(context.Background(), reclaim.StoreConfig{
		Store:           cmd.Kubernetes.OutputStore,
		Endpoint:        cmd.Kubernetes.OutputEndpoint,
		StoreID:         cmd.Kubernetes.OutputStoreID,
		CACert:          cmd.Kubernetes.OutputStoreCACert,
		Timeout:         cmd.Kubernetes.OutputDeleteTimeout,
		ListTokenFile:   cmd.Kubernetes.OutputListTokenFile,
		DeleteTokenFile: cmd.Kubernetes.OutputDeleteTokenFile,
	}, namespace)
	if err != nil {
		return nil, fmt.Errorf("opening the output store: %w", err)
	}
	cmd.hangarOutputStoreClose = closeStore

	repository := db.NewHangarOutputRepository(db.HangarConsumerPrefixForComponent())
	transactor := hangarOutputTransactor{conn: dbConn}

	return []RunnableComponent{
		{
			Component: atc.Component{Name: atc.ComponentHangarReclaim},
			Runnable: &reclaim.Pass{
				Locker:        locker,
				Transactor:    transactor,
				Repository:    repository,
				Reclaimer:     deletes,
				Grace:         cmd.Kubernetes.OutputPublicationGrace,
				DeleteTimeout: cmd.Kubernetes.OutputDeleteTimeout,
				Batch:         cmd.Kubernetes.OutputReclaimBatch,
				OwnerID:       uuid.NewString(),
			},
			Interval: cmd.Kubernetes.OutputReclaimInterval,
		},
		{
			Component: atc.Component{Name: atc.ComponentHangarOrphanSweep},
			Runnable: &reclaim.Sweep{
				Locker:          locker,
				Transactor:      transactor,
				Repository:      repository,
				Namespace:       namespace,
				Lister:          lister,
				Reclaimer:       deletes,
				CaptureDeadline: cmd.Kubernetes.OutputCaptureDeadline,
				DeleteTimeout:   cmd.Kubernetes.OutputDeleteTimeout,
			},
			Interval: cmd.Kubernetes.OutputOrphanSweepInterval,
		},
	}, nil
}
