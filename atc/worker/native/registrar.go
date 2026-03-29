package native

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"code.cloudfoundry.org/lager/v3"
	concourse "github.com/concourse/concourse"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/tracing"
)

const (
	// heartbeatTTL matches the K8s registrar. The worker must heartbeat
	// before this expires or it will be reaped.
	heartbeatTTL = 30 * time.Second
)

// Registrar handles registering and heartbeating a native worker in the
// Concourse database. It writes directly to the DB via WorkerFactory, the
// same pattern as the K8s registrar.
type Registrar struct {
	logger        lager.Logger
	cfg           Config
	workerFactory db.WorkerFactory
}

// NewRegistrar creates a Registrar for the native worker.
func NewRegistrar(logger lager.Logger, cfg Config, workerFactory db.WorkerFactory) *Registrar {
	return &Registrar{
		logger:        logger,
		cfg:           cfg,
		workerFactory: workerFactory,
	}
}

// Run implements component.Runnable. It registers the worker and refreshes
// its TTL on each invocation.
func (r *Registrar) Run(ctx context.Context) error {
	return r.Register(ctx)
}

// Register saves the native worker to the database with a fresh TTL.
func (r *Registrar) Register(ctx context.Context) error {
	logger := r.logger.Session("register")

	ctx, span := tracing.StartSpan(ctx, "native.registrar.register", tracing.Attrs{
		"worker-name": r.cfg.WorkerName,
		"platform":    r.cfg.Platform,
	})
	var spanErr error
	defer func() { tracing.End(span, spanErr) }()

	activeContainers := r.countActiveContainers()

	worker := atc.Worker{
		Name:             r.cfg.WorkerName,
		Platform:         r.cfg.Platform,
		State:            "running",
		Version:          concourse.WorkerVersion,
		ActiveContainers: activeContainers,
	}

	_, err := r.workerFactory.SaveWorker(worker, heartbeatTTL)
	if err != nil {
		logger.Error("failed-to-save-worker", err)
		spanErr = err
		return fmt.Errorf("saving native worker: %w", err)
	}

	return nil
}

// Heartbeat refreshes the worker's TTL by re-registering.
func (r *Registrar) Heartbeat(ctx context.Context) error {
	return r.Register(ctx)
}

// countActiveContainers counts container directories under the WorkDir.
// This mirrors the K8s registrar's pod-counting approach.
func (r *Registrar) countActiveContainers() int {
	containersDir := filepath.Join(r.cfg.WorkDir, "containers")
	entries, err := os.ReadDir(containersDir)
	if err != nil {
		return 0
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() {
			count++
		}
	}
	return count
}
