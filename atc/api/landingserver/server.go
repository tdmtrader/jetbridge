// Package landingserver is the landing queue's API (ADR-0009): a team sets a
// queue, submits an entry, and reads the queue's status. The engine is the
// component; nothing here composes or lands.
package landingserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/db"
)

// maxBodyBytes bounds a queue config or a submission: both are a few lines.
const maxBodyBytes = 1 << 20

type Server struct {
	logger lager.Logger
	queues db.LandingQueueFactory
}

func NewServer(logger lager.Logger, queues db.LandingQueueFactory) *Server {
	return &Server{logger: logger, queues: queues}
}

// SetLandingQueue sets the named queue's config from the YAML body.
func (s *Server) SetLandingQueue(team db.Team) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger := s.logger.Session("set-landing-queue")
		name := r.FormValue(":queue_name")
		if !atc.ValidLandingEntryID(name) {
			http.Error(w, "landing queue name is not a safe name", http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		config, err := atc.ParseLandingQueueConfig(body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_, found, err := s.queues.Queue(r.Context(), team.ID(), name)
		if err != nil {
			logger.Error("failed-to-read-landing-queue", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if err := s.queues.SetQueue(r.Context(), team.ID(), name, config); err != nil {
			logger.Error("failed-to-set-landing-queue", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if found {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusCreated)
		}
	})
}

// SubmitLanding queues an entry. 201 queues it, 200 says the same commit
// was already queued, 409 names an id already taken by another commit.
func (s *Server) SubmitLanding(team db.Team) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger := s.logger.Session("submit-landing")
		queue, ok := s.queue(w, r, team)
		if !ok {
			return
		}
		var submission atc.LandingSubmission
		if err := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes)).Decode(&submission); err != nil {
			http.Error(w, "submission: "+err.Error(), http.StatusBadRequest)
			return
		}
		created, err := s.queues.Submit(r.Context(), queue.ID, submission, accessor.GetAccessor(r).UserInfo().DisplayUserId)
		switch {
		case errors.Is(err, db.ErrLandingEntryExists):
			http.Error(w, fmt.Sprintf("entry %q is already queued or settled for another commit", submission.ID), http.StatusConflict)
			return
		case errors.Is(err, db.ErrInvalidLandingSubmission):
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		case err != nil:
			logger.Error("failed-to-submit", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if created {
			w.WriteHeader(http.StatusCreated)
		} else {
			w.WriteHeader(http.StatusOK)
		}
	})
}

// GetLandingQueue is the queue's status: config, entries and the state of
// its landings.
func (s *Server) GetLandingQueue(team db.Team) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger := s.logger.Session("get-landing-queue")
		queue, ok := s.queue(w, r, team)
		if !ok {
			return
		}
		status, err := s.queues.Status(r.Context(), queue)
		if err != nil {
			logger.Error("failed-to-read-status", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(status); err != nil {
			logger.Error("failed-to-encode-status", err)
		}
	})
}

func (s *Server) queue(w http.ResponseWriter, r *http.Request, team db.Team) (db.LandingQueue, bool) {
	queue, found, err := s.queues.Queue(r.Context(), team.ID(), r.FormValue(":queue_name"))
	if err != nil {
		s.logger.Error("failed-to-read-landing-queue", err)
		w.WriteHeader(http.StatusInternalServerError)
		return db.LandingQueue{}, false
	}
	if !found {
		w.WriteHeader(http.StatusNotFound)
		return db.LandingQueue{}, false
	}
	return queue, true
}
