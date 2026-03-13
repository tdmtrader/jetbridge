package infoserver

import (
	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/creds"
	"github.com/concourse/concourse/atc/db"
)

//counterfeiter:generate . DBPinger
type DBPinger interface {
	Ping() error
}

type Server struct {
	logger           lager.Logger
	version          string
	workerVersion    string
	externalURL      string
	clusterName      string
	credsManagers    creds.Managers
	jetBridgeVersion string
	concourseVersion string
	dbPinger         DBPinger
	workerFactory    db.WorkerFactory
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
	dbPinger DBPinger,
	workerFactory db.WorkerFactory,
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
		dbPinger:         dbPinger,
		workerFactory:    workerFactory,
	}
}
