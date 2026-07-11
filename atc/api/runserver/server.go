package runserver

import (
	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db"
)

type Server struct {
	logger     lager.Logger
	runFactory db.PipelineRunFactory
}

func NewServer(
	logger lager.Logger,
	runFactory db.PipelineRunFactory,
) *Server {
	return &Server{
		logger:     logger,
		runFactory: runFactory,
	}
}
