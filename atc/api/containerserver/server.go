package containerserver

import (
	"context"
	"time"

	"code.cloudfoundry.org/clock"
	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/runtime"
)

type Pool interface {
	LocateContainer(ctx context.Context, teamID int, handle string) (runtime.Container, runtime.Worker, bool, error)
}

type Server struct {
	logger lager.Logger

	workerPool              Pool
	interceptTimeoutFactory InterceptTimeoutFactory
	interceptUpdateInterval time.Duration
	clock                   clock.Clock
}

func NewServer(
	logger lager.Logger,
	workerPool Pool,
	interceptTimeoutFactory InterceptTimeoutFactory,
	interceptUpdateInterval time.Duration,
	clock clock.Clock,
) *Server {
	return &Server{
		logger:                  logger,
		workerPool:              workerPool,
		interceptTimeoutFactory: interceptTimeoutFactory,
		interceptUpdateInterval: interceptUpdateInterval,
		clock:                   clock,
	}
}
