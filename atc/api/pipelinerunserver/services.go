package pipelinerunserver

import "github.com/concourse/concourse/atc/runs"

// Services are configured at startup. A missing admission service keeps Run
// creation unavailable; a missing output plane keeps input intake and
// credential delivery unavailable.
type Services struct {
	Results  ResultReader
	Admitter runs.Admitter
	// HangarOutput reports whether this control plane has an output plane.
	HangarOutput bool
}

// Each read holds an archive and its canonical copy in the fixed disk scratch.
const resultReadConcurrency = 2

func (s *Server) SetServices(services Services) {
	s.services = services
	s.resultSlots = make(chan struct{}, resultReadConcurrency)
}
