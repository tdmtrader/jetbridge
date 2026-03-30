package remote

import (
	"context"
	"fmt"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/worker/native/agentpb"
)

const heartbeatTTL = 30 * time.Second

// Registrar handles registering a remote native worker in the database by
// pinging the agent to discover its platform and arch. Implements
// component.Runnable.
type Registrar struct {
	logger        lager.Logger
	client        agentpb.NativeAgentClient
	workerName    string
	workerFactory db.WorkerFactory
}

func NewRegistrar(logger lager.Logger, client agentpb.NativeAgentClient, workerName string, workerFactory db.WorkerFactory) *Registrar {
	return &Registrar{
		logger:        logger,
		client:        client,
		workerName:    workerName,
		workerFactory: workerFactory,
	}
}

// Run implements component.Runnable. It pings the remote agent and saves the
// worker to the database with a fresh TTL.
func (r *Registrar) Run(ctx context.Context) error {
	logger := r.logger.Session("register")

	resp, err := r.client.Ping(ctx, &agentpb.PingRequest{})
	if err != nil {
		logger.Error("failed-to-ping-agent", err)
		return fmt.Errorf("ping remote native agent: %w", err)
	}

	worker := atc.Worker{
		Name:     r.workerName,
		Platform: resp.Platform,
		State:    "running",
		Version:  resp.Version,
	}

	_, err = r.workerFactory.SaveWorker(worker, heartbeatTTL)
	if err != nil {
		logger.Error("failed-to-save-worker", err)
		return fmt.Errorf("saving remote native worker: %w", err)
	}

	return nil
}
