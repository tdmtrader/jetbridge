package infoserver

import (
	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/creds"
)

type Server struct {
	logger           lager.Logger
	version          string
	workerVersion    string
	externalURL      string
	clusterName      string
	credsManagers    creds.Managers
	jetBridgeVersion string
	concourseVersion string
}

func NewServer(
	logger lager.Logger,
	version string,
	workerVersion string,
	externalURL string,
	clusterName string,
	credsManagers creds.Managers,
	jetBridgeVersion string,
	concourseVersion string,
) *Server {
	return &Server{
		logger:           logger,
		version:          version,
		workerVersion:    workerVersion,
		externalURL:      externalURL,
		clusterName:      clusterName,
		credsManagers:    credsManagers,
		jetBridgeVersion: jetBridgeVersion,
		concourseVersion: concourseVersion,
	}
}
