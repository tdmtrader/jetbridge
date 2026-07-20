package transcriptserver

import "code.cloudfoundry.org/lager/v3"

// Store reads a persisted agent transcript for a ticket's run. Satisfied by
// db.AgentRunTranscriptFactory.
type Store interface {
	Get(ticketID, buildID int) (ndjson string, found bool, err error)
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
