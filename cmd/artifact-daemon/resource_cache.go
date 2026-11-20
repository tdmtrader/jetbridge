package main

import (
	"errors"
	"io/fs"
	"net/http"

	"code.cloudfoundry.org/lager/v3"

	"github.com/concourse/concourse/artifactwire"
)

// handleHeadResourceCache checks whether a resource cache key exists on this
// daemon. The key is looked up in the registry (registered as an alias after a
// successful get step). Returns 200 with X-Node-Name header if found, 404
// otherwise.
func (s *Server) handleHeadResourceCache(w http.ResponseWriter, r *http.Request) {
	key, err := s.requestKey(r, artifactwire.ResourceCachesPrefix)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	loc, found := s.lookupRegistry(key)
	if !found {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	// Verify the location still exists on disk — aliases can become stale if
	// the sweeper removed the step directory. Through the handle, under the
	// walk's rule: a symlinked location is refused, as GET would refuse it.
	if _, err := statLocation(s.root, osName(loc)); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			s.registry.Remove(key)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if errors.Is(err, ErrRefused) {
			s.refuse(w, r, http.StatusUnprocessableEntity, reasonSymlinkedPath, err)
			return
		}
		s.logger.Error("resource-cache-stat-error", err, lager.Data{"key": key, "rel": string(loc)})
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	if s.nodeName != "" {
		w.Header().Set("X-Node-Name", s.nodeName)
	}
	w.WriteHeader(http.StatusOK)
}

// handleGetResourceCache streams a resource cache as a tar archive. Used by
// peer daemons to fetch cached resource data for cross-node resolution.
func (s *Server) handleGetResourceCache(w http.ResponseWriter, r *http.Request) {
	key, err := s.requestKey(r, artifactwire.ResourceCachesPrefix)
	if err != nil {
		s.refuse(w, r, http.StatusBadRequest, reasonInvalidKey, err)
		return
	}

	loc, found := s.lookupRegistry(key)
	if !found {
		http.NotFound(w, r)
		return
	}

	info, err := statLocation(s.root, osName(loc))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			s.registry.Remove(key)
			http.NotFound(w, r)
			return
		}
		if errors.Is(err, ErrRefused) {
			s.refuse(w, r, http.StatusUnprocessableEntity, reasonSymlinkedPath, err)
			return
		}
		s.logger.Error("resource-cache-stat-error", err, lager.Data{"key": key, "rel": string(loc)})
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if s.nodeName != "" {
		w.Header().Set("X-Node-Name", s.nodeName)
	}

	// The same rule as handleGetArtifact: a resource cache is a directory.
	if !info.IsDir() {
		s.logger.Error("resource-cache-location-is-not-a-directory", errNotADirectory, lager.Data{"key": key, "rel": string(loc)})
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Same guard discipline as handleGetArtifact: no sweeps mid-stream.
	release := s.guard.BeginRead(s.stepHandle(loc))
	defer release()
	s.touchStepDir(loc)

	w.Header().Set("Content-Type", "application/x-tar")
	if err := s.tarDirectory(w, loc); err != nil {
		s.logger.Error("failed-to-tar-resource-cache", err, lager.Data{"key": key, "rel": string(loc)})
		// Headers already sent; abort so the peer sees a hard failure
		// rather than a clean-looking truncated tar.
		panic(http.ErrAbortHandler)
	}
}
