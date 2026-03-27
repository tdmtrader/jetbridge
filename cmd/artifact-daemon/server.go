package main

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"code.cloudfoundry.org/lager/v3"
)

// Server is the artifact-daemon HTTP server that stores and serves
// artifact tar files from local hostPath storage.
type Server struct {
	logger      lager.Logger
	storagePath string
}

// NewServer creates a new artifact-daemon server.
func NewServer(logger lager.Logger, storagePath string) *Server {
	return &Server{
		logger:      logger,
		storagePath: storagePath,
	}
}

// Handler returns the HTTP handler for the server.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /artifacts/", s.handleGetArtifact)
	mux.HandleFunc("PUT /artifacts/", s.handlePutArtifact)
	mux.HandleFunc("DELETE /artifacts/", s.handleDeleteArtifact)
	mux.HandleFunc("HEAD /artifacts/", s.handleHeadArtifact)
	return mux
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (s *Server) artifactPath(r *http.Request) string {
	key := strings.TrimPrefix(r.URL.Path, "/artifacts/")
	return filepath.Join(s.storagePath, "artifacts", key)
}

func (s *Server) handleGetArtifact(w http.ResponseWriter, r *http.Request) {
	path := s.artifactPath(r)

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			http.NotFound(w, r)
			return
		}
		s.logger.Error("failed-to-open-artifact", err, lager.Data{"path": path})
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", "application/octet-stream")
	io.Copy(w, f)
}

func (s *Server) handlePutArtifact(w http.ResponseWriter, r *http.Request) {
	path := s.artifactPath(r)

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		s.logger.Error("failed-to-create-artifact-dir", err, lager.Data{"path": path})
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	f, err := os.Create(path)
	if err != nil {
		s.logger.Error("failed-to-create-artifact", err, lager.Data{"path": path})
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	if _, err := io.Copy(f, r.Body); err != nil {
		s.logger.Error("failed-to-write-artifact", err, lager.Data{"path": path})
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
}

func (s *Server) handleDeleteArtifact(w http.ResponseWriter, r *http.Request) {
	path := s.artifactPath(r)

	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		s.logger.Error("failed-to-delete-artifact", err, lager.Data{"path": path})
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleHeadArtifact(w http.ResponseWriter, r *http.Request) {
	path := s.artifactPath(r)

	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		s.logger.Error("failed-to-stat-artifact", err, lager.Data{"path": path})
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}
