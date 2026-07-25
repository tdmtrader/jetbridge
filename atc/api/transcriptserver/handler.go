package transcriptserver

import (
	"code.cloudfoundry.org/lager/v3"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc/db"
)

// Store reads the persisted agent transcripts of one durable workflow run.
// Satisfied by db.AgentRunTranscriptFactory. The workflow name is part of the
// lookup, not decoration: it is the authorization scope (a run id addressed
// under the wrong workflow name resolves to nothing).
type Store interface {
	ListByWorkflowRun(workflowName string, runID snapshot.WorkflowRunID) ([]db.AgentRunTranscript, error)
}

type Server struct {
	logger lager.Logger
	store  Store
}

func NewServer(logger lager.Logger, store Store) *Server {
	return &Server{
		logger: logger,
		store:  store,
	}
}
