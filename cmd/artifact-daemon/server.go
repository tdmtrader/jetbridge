package main

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"code.cloudfoundry.org/lager/v3"
)

// Server is the artifact-daemon HTTP server that stores and serves
// artifact tar files from local hostPath storage.
type Server struct {
	logger      lager.Logger
	storagePath string
	nodeName    string
	registry    *Registry
	peers       *PeerResolver
}

// NewServer creates a new artifact-daemon server.
func NewServer(logger lager.Logger, storagePath, nodeName string) *Server {
	return &Server{
		logger:      logger,
		storagePath: storagePath,
		nodeName:    nodeName,
		registry:    NewRegistry(logger),
	}
}

// Registry returns the server's artifact registry.
func (s *Server) Registry() *Registry {
	return s.registry
}

// SetPeerResolver configures the peer resolver for cross-node artifact
// resolution. When nil, /resolve only checks local storage.
func (s *Server) SetPeerResolver(peers *PeerResolver) {
	s.peers = peers
}

// Handler returns the HTTP handler for the server.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /artifacts/", s.handleGetArtifact)
	mux.HandleFunc("PUT /artifacts/", s.handlePutArtifact)
	mux.HandleFunc("DELETE /artifacts/", s.handleDeleteArtifact)
	mux.HandleFunc("HEAD /artifacts/", s.handleHeadArtifact)
	mux.HandleFunc("POST /register", s.handleRegister)
	mux.HandleFunc("POST /resolve", s.handleResolve)
	mux.HandleFunc("POST /resolve-batch", s.handleResolveBatch)
	mux.HandleFunc("HEAD /resource-caches/", s.handleHeadResourceCache)
	mux.HandleFunc("GET /resource-caches/", s.handleGetResourceCache)
	return mux
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (s *Server) artifactPath(r *http.Request) string {
	key := strings.TrimPrefix(r.URL.Path, "/artifacts/")
	return filepath.Join(s.storagePath, key)
}

func (s *Server) handleGetArtifact(w http.ResponseWriter, r *http.Request) {
	path := s.artifactPath(r)

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			http.NotFound(w, r)
			return
		}
		s.logger.Error("failed-to-stat-artifact", err, lager.Data{"path": path})
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Directory: tar on-the-fly and stream.
	if info.IsDir() {
		w.Header().Set("Content-Type", "application/x-tar")
		s.tarDirectory(w, path)
		return
	}

	// File: serve as-is (backward compat for legacy tar files).
	f, err := os.Open(path)
	if err != nil {
		s.logger.Error("failed-to-open-artifact", err, lager.Data{"path": path})
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", "application/octet-stream")
	io.Copy(w, f)
}

// tarDirectory writes a tar archive of the directory to w.
func (s *Server) tarDirectory(w http.ResponseWriter, dir string) {
	tw := tar.NewWriter(w)
	defer tw.Close()

	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		hdr := &tar.Header{
			Name:    rel,
			Size:    info.Size(),
			Mode:    int64(info.Mode()),
			ModTime: info.ModTime(),
		}
		if info.Mode()&os.ModeSymlink != 0 {
			link, _ := os.Readlink(path)
			hdr.Typeflag = tar.TypeSymlink
			hdr.Linkname = link
			hdr.Size = 0
			return tw.WriteHeader(hdr)
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
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

	err := os.RemoveAll(path)
	if err != nil {
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

// registerRequest is the JSON body for POST /register.
type registerRequest struct {
	Key       string `json:"key"`
	LocalPath string `json:"local_path"`
}

// resolveRequest is the JSON body for POST /resolve.
type resolveRequest struct {
	Key  string `json:"key"`
	Dest string `json:"dest"`
}

// resolveResponse is the JSON body returned by POST /resolve.
type resolveResponse struct {
	Status   string `json:"status"`
	Source   string `json:"source"`
	Method   string `json:"method"`
	Duration string `json:"duration,omitempty"`
	Error    string `json:"error,omitempty"`
}

// handleRegister accepts POST /register with a JSON body containing
// {key, local_path} and registers the artifact in the daemon's registry.
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
		return
	}
	if req.Key == "" || req.LocalPath == "" {
		http.Error(w, "key and local_path are required", http.StatusBadRequest)
		return
	}

	// Validate that the path exists on disk.
	if _, err := os.Stat(req.LocalPath); err != nil {
		if os.IsNotExist(err) {
			s.logger.Info("register-path-not-found", lager.Data{"key": req.Key, "path": req.LocalPath})
			http.Error(w, fmt.Sprintf("path not found: %s", req.LocalPath), http.StatusNotFound)
			return
		}
		s.logger.Error("register-stat-error", err, lager.Data{"key": req.Key, "path": req.LocalPath})
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	s.registry.RegisterAlias(req.Key, req.LocalPath)

	s.logger.Info("registered", lager.Data{"key": req.Key, "path": req.LocalPath})
	w.WriteHeader(http.StatusCreated)
}

// resolveOne resolves a single artifact key to a destination path.
// It is the core logic shared by handleResolve and handleResolveBatch.
func (s *Server) resolveOne(ctx context.Context, key, dest string) resolveResponse {
	start := time.Now()
	logger := s.logger.Session("resolve", lager.Data{"key": key, "dest": dest})

	// Step 1: Check registry for explicit registration.
	sourcePath, found := s.registry.Lookup(key)
	if found {
		if err := s.copyArtifact(sourcePath, dest); err != nil {
			logger.Error("copy-failed", err, lager.Data{"source": sourcePath})
			return resolveResponse{Status: "error", Source: sourcePath, Method: "local", Error: err.Error()}
		}
		duration := time.Since(start)
		logger.Info("resolved", lager.Data{"method": "registry", "source": sourcePath, "duration": duration.String()})
		return resolveResponse{Status: "ok", Source: sourcePath, Method: "registry", Duration: duration.String()}
	}

	// Step 2: Fallback — check if key maps to a steps/ directory on disk.
	stepsPath := filepath.Join(s.storagePath, "steps", key)
	if info, err := os.Stat(stepsPath); err == nil && info.IsDir() {
		s.registry.Register(key, stepsPath)

		if err := s.copyArtifact(stepsPath, dest); err != nil {
			logger.Error("copy-failed", err, lager.Data{"source": stepsPath})
			return resolveResponse{Status: "error", Source: stepsPath, Method: "filesystem", Error: err.Error()}
		}
		duration := time.Since(start)
		logger.Info("resolved", lager.Data{"method": "filesystem", "source": stepsPath, "duration": duration.String()})
		return resolveResponse{Status: "ok", Source: stepsPath, Method: "filesystem", Duration: duration.String()}
	}

	// Step 3: Query peer daemons for cross-node resolution.
	if s.peers != nil {
		peerIP, found := s.peers.Probe(ctx, key)
		if found {
			if err := s.peers.Fetch(ctx, peerIP, key, dest); err != nil {
				logger.Error("peer-fetch-failed", err, lager.Data{"peer": peerIP})
				return resolveResponse{Status: "error", Source: peerIP, Method: "peer", Error: err.Error()}
			}
			duration := time.Since(start)
			logger.Info("resolved", lager.Data{"method": "peer", "peer": peerIP, "duration": duration.String()})
			return resolveResponse{Status: "ok", Source: peerIP, Method: "peer", Duration: duration.String()}
		}
	}

	// Step 4: Not found anywhere.
	duration := time.Since(start)
	logger.Info("not-found", lager.Data{"duration": duration.String()})
	return resolveResponse{Status: "not_found", Method: "exhausted", Duration: duration.String(), Error: fmt.Sprintf("artifact %q not found on this node or any peer", key)}
}

// handleResolve accepts POST /resolve with a JSON body containing {key, dest}.
// It looks up the artifact by key and copies it to the destination path.
//
// Resolution order:
//  1. Check local registry for an explicit registration
//  2. Fall back to filesystem scan (check if the key maps to a steps/ directory)
//  3. Query peer daemons for cross-node resolution
func (s *Server) handleResolve(w http.ResponseWriter, r *http.Request) {
	var req resolveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
		return
	}
	if req.Key == "" || req.Dest == "" {
		http.Error(w, "key and dest are required", http.StatusBadRequest)
		return
	}

	resp := s.resolveOne(r.Context(), req.Key, req.Dest)

	status := http.StatusOK
	if resp.Status == "error" {
		status = http.StatusInternalServerError
	} else if resp.Status == "not_found" {
		status = http.StatusNotFound
	}
	writeJSON(w, status, resp)
}

// batchResolveRequest is the JSON body for POST /resolve-batch.
type batchResolveRequest struct {
	Items []resolveRequest `json:"items"`
}

// batchResolveResponse is the JSON body returned by POST /resolve-batch.
type batchResolveResponse struct {
	Status  string            `json:"status"`
	Results []resolveResponse `json:"results"`
}

// handleResolveBatch accepts POST /resolve-batch with a JSON body containing
// {"items": [{key, dest}, ...]}. It resolves all artifacts concurrently and
// returns an aggregated response. If any item fails, the overall status is
// "error" and the HTTP status is 500.
func (s *Server) handleResolveBatch(w http.ResponseWriter, r *http.Request) {
	var req batchResolveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	results := make([]resolveResponse, len(req.Items))

	var wg sync.WaitGroup
	for i, item := range req.Items {
		wg.Add(1)
		go func(idx int, key, dest string) {
			defer wg.Done()
			results[idx] = s.resolveOne(r.Context(), key, dest)
		}(i, item.Key, item.Dest)
	}
	wg.Wait()

	overall := "ok"
	for _, res := range results {
		if res.Status != "ok" {
			overall = "error"
			break
		}
	}

	status := http.StatusOK
	if overall == "error" {
		status = http.StatusInternalServerError
	}

	writeJSON(w, status, batchResolveResponse{Status: overall, Results: results})
}

// copyArtifact copies the contents of src directory to dest using cp -a.
// The destination directory is created if it doesn't exist.
func (s *Server) copyArtifact(src, dest string) error {
	if err := os.MkdirAll(dest, 0755); err != nil {
		return fmt.Errorf("create dest dir: %w", err)
	}

	// Use cp -a for atomic, permission-preserving copy.
	// The trailing "/." ensures we copy contents, not the directory itself.
	cmd := exec.Command("cp", "-a", src+"/.", dest+"/")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("cp -a %s/. %s/: %w (output: %s)", src, dest, err, strings.TrimSpace(string(output)))
	}
	return nil
}

// handleHeadResourceCache checks whether a resource cache key exists on this
// daemon. The key is looked up in the registry (registered as an alias after a
// successful get step). Returns 200 with X-Node-Name header if found, 404
// otherwise.
func (s *Server) handleHeadResourceCache(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/resource-caches/")
	if key == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	path, found := s.registry.Lookup(key)
	if !found {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	// Verify the path still exists on disk — aliases can become stale if
	// the sweeper removed the step directory.
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			s.registry.Remove(key)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		s.logger.Error("resource-cache-stat-error", err, lager.Data{"key": key, "path": path})
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
	key := strings.TrimPrefix(r.URL.Path, "/resource-caches/")
	if key == "" {
		http.Error(w, "key required", http.StatusBadRequest)
		return
	}

	path, found := s.registry.Lookup(key)
	if !found {
		http.NotFound(w, r)
		return
	}

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			s.registry.Remove(key)
			http.NotFound(w, r)
			return
		}
		s.logger.Error("resource-cache-stat-error", err, lager.Data{"key": key, "path": path})
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if s.nodeName != "" {
		w.Header().Set("X-Node-Name", s.nodeName)
	}

	if info.IsDir() {
		w.Header().Set("Content-Type", "application/x-tar")
		s.tarDirectory(w, path)
		return
	}

	f, err := os.Open(path)
	if err != nil {
		s.logger.Error("resource-cache-open-error", err, lager.Data{"key": key, "path": path})
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", "application/octet-stream")
	io.Copy(w, f)
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
