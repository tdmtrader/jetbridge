package volumeserver

import (
	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db"
)

type Server struct {
	logger     lager.Logger
	repository db.VolumeRepository
}

func NewServer(
	logger lager.Logger,
	volumeRepository db.VolumeRepository,
) *Server {
	return &Server{
		logger:     logger,
		repository: volumeRepository,
	}
}
