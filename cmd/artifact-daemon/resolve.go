package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"code.cloudfoundry.org/lager/v3"

	"github.com/concourse/concourse/artifactwire"
	"github.com/concourse/concourse/cmd/artifact-daemon/outputplane"
)

// acquireDest takes the destination lock for dest, waiting until it is free or
// ctx dies. On success it returns a release func the caller must call exactly
// once.
//
// Registration and reclamation are both done under destLocksMu, so the entry a
// goroutine registers on cannot be deleted out from under it: a releaser only
// deletes an entry it observes at waiters==0, and a newcomer only ever
// increments an entry's waiters while holding the same mutex. That closes the
// lifetime race a lock-free refcount could not — there, the decrement-to-zero
// and the map delete were separate steps, so a newcomer could register in
// between and end up holding a lock no longer in the map while a third
// goroutine minted a fresh one for the same dest.
//
// The mutex is held only for O(1) map bookkeeping, never across the channel
// wait or the copy.
func (s *Server) acquireDest(ctx context.Context, dest string) (func(), error) {
	// Keyed on the RELATIVE form, which is the one identity of a destination
	// this package recognises. filepath.Clean(dest) was a second computation of
	// "which destination is this", and it disagreed with the first: lexicalRel
	// accepts the storage root under either its raw or its resolved spelling,
	// so two dest strings naming ONE directory produced two different lock
	// keys and two writers ran on it at once. That is the very thing
	// peers.Fetch cites this lock for when it treats a failed clear-and-rename
	// as a hard error rather than tolerating it.
	rel, err := s.lexicalDestRel(dest)
	if err != nil {
		return nil, err
	}
	key := string(rel)

	s.destLocksMu.Lock()
	dl, ok := s.destLocks[key]
	if !ok {
		dl = &destLock{ch: make(chan struct{}, 1)}
		s.destLocks[key] = dl
	}
	dl.waiters++
	s.destLocksMu.Unlock()

	release := func() {
		s.destLocksMu.Lock()
		dl.waiters--
		if dl.waiters == 0 && s.destLocks[key] == dl {
			delete(s.destLocks, key)
		}
		s.destLocksMu.Unlock()
	}

	select {
	case dl.ch <- struct{}{}:
		// Hold the lock; release drains the slot and drops our waiter count.
		return func() {
			<-dl.ch
			release()
		}, nil
	case <-ctx.Done():
		release()
		return nil, ctx.Err()
	}
}

// handleMirrorTrigger accepts POST /mirror with a JSON body containing
// {"key": "handle/output"} and schedules an asynchronous mirror of the
// local steps/{key}/ directory to peer daemons. Returns 202 Accepted
// immediately — the caller should not wait on the mirror to complete.
//
// The mirror trigger function is set by SetMirrorTrigger; if unset (mirror
// disabled), the endpoint still accepts the request and returns 202 so
// that ATC callers don't fail when talking to a daemon that has mirror
// off.
func (s *Server) handleMirrorTrigger(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
	var req artifactwire.MirrorRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.refuse(w, r, http.StatusBadRequest, reasonInvalidJSON, fmt.Errorf("invalid JSON: %v", err))
		return
	}
	if req.Key == "" {
		s.refuse(w, r, http.StatusBadRequest, reasonMissingField, errors.New("key is required"))
		return
	}
	// Mirror.run joins this onto the storage root and tars the result to every
	// peer, so an unvalidated key reads a tree outside the root and ships it
	// off-node.
	if err := validateRequestKey(req.Key); err != nil {
		s.refuse(w, r, http.StatusBadRequest, reasonInvalidKey, err)
		return
	}

	if s.mirrorTrigger != nil {
		s.mirrorTrigger(r.Context(), req.Key)
	}

	w.WriteHeader(http.StatusAccepted)
}

// handleRegister accepts POST /register with a JSON body containing
// {key, local_path} and registers the artifact in the daemon's registry.
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
	var req artifactwire.RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.refuse(w, r, http.StatusBadRequest, reasonInvalidJSON, fmt.Errorf("invalid JSON: %v", err))
		return
	}
	if req.Key == "" || req.LocalPath == "" {
		s.refuse(w, r, http.StatusBadRequest, reasonMissingField, errors.New("key and local_path are required"))
		return
	}

	// Validate the key and path before the first side effect.
	if err := validateRequestKey(req.Key); err != nil {
		s.refuse(w, r, http.StatusBadRequest, reasonInvalidKey, err)
		return
	}
	if err := validateContainedPath(s.storagePath, req.LocalPath); err != nil {
		s.refuse(w, r, http.StatusBadRequest, reasonUncontained, err)
		return
	}

	// Validate that the path exists on disk, and is a directory: an alias is a
	// second name for an artifact, and an artifact is a directory. A file-valued
	// alias would make GET a raw file server for anything under the root.
	info, err := os.Stat(req.LocalPath)
	if err != nil {
		if os.IsNotExist(err) {
			s.logger.Info("register-path-not-found", lager.Data{"key": req.Key, "path": req.LocalPath})
			s.refuse(w, r, http.StatusNotFound, reasonNotFound, fmt.Errorf("path not found: %s", req.LocalPath))
			return
		}
		s.logger.Error("register-stat-error", err, lager.Data{"key": req.Key, "path": req.LocalPath})
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !info.IsDir() {
		s.refuse(w, r, http.StatusBadRequest, reasonNotDirectory, fmt.Errorf("path %s %w", req.LocalPath, errNotADirectory))
		return
	}

	// REFUSE here rather than store-and-hope. req.LocalPath is client-supplied,
	// so this is the one Register call whose input is not derived from the
	// daemon's own tree — and 400 is the honest status: the caller named a path
	// outside the store.
	register := s.registry.RegisterAlias
	if req.ReadOnly {
		register = s.registry.RegisterReadOnlyAlias
	}
	_, err = register(req.Key, req.LocalPath)
	if err != nil {
		s.logger.Info("register-refused", lager.Data{"key": req.Key, "path": req.LocalPath, "reason": err.Error()})
		// Two different refusals with two different statuses. An uncontained
		// path is the caller's mistake (400); a capture-held one is a conflict
		// with another authority on this node (409), and answering 400 for it
		// would tell the ATC to stop retrying something that will clear.
		if errors.Is(err, outputplane.ErrRefused) {
			s.captureRefusal(w, r, RelKey(req.LocalPath), outputplane.Held, err)
			return
		}
		s.refuse(w, r, http.StatusBadRequest, reasonUncontained, err)
		return
	}

	s.logger.Info("registered", lager.Data{"key": req.Key, "path": req.LocalPath})
	w.WriteHeader(http.StatusCreated)
}

// failedResolve is the ONE place a resolve failure decides whose fault it is.
//
// ErrRefused (containment.go) means the ARCHIVE is answerable rather than this
// daemon: an absolute symlink target, a key naming the control ledger. That is
// a CONSIDERED ANSWER about these bytes, identical on every retry, so it is
// reported with Method "refused" — the same value the capture-held branch
// below already uses — and resolveHTTPStatus turns that into a 422 instead of
// a 500. Every failing branch of resolveOne goes through here so a new one
// cannot quietly re-acquire the old "everything is a 500" behaviour.
func failedResolve(source, method string, err error) artifactwire.ResolveResponse {
	if errors.Is(err, ErrRefused) {
		method = "refused"
	}
	return artifactwire.ResolveResponse{Status: "error", Source: source, Method: method, Error: err.Error()}
}

// resolveHTTPStatus is the status ONE result would be answered with on its own.
// Batch aggregation uses it to rank each item's outcome.
func resolveHTTPStatus(res artifactwire.ResolveResponse) int {
	switch {
	case res.Status == "ok":
		return http.StatusOK
	case res.Status == "not_found":
		return http.StatusNotFound
	case res.Method == "refused":
		return http.StatusUnprocessableEntity
	default:
		return http.StatusInternalServerError
	}
}

// resolveStatusRank orders those statuses for a batch, worst last. A hard
// error outranks a refusal outranks a miss, because the ranking answers "what
// should the caller DO", and only the 500 says something is wrong with this
// daemon and might be different next time.
var resolveStatusRank = map[int]int{
	http.StatusOK:                  0,
	http.StatusNotFound:            1,
	http.StatusUnprocessableEntity: 2,
	http.StatusInternalServerError: 3,
}

// resolveOne resolves a single artifact key to a destination path.
// It is the core logic shared by handleResolve and handleResolveBatch.
func (s *Server) resolveOne(ctx context.Context, key, dest string) (resp artifactwire.ResolveResponse) {
	start := time.Now()
	defer func() {
		// Every return below builds its own artifactwire.ResolveResponse and none of them
		// carried the key. Set once, here, so a new branch cannot forget it.
		resp.Key = key
		s.metrics.recordResolve(resp.Method, resp.Status, time.Since(start))
	}()
	logger := s.logger.Session("resolve", lager.Data{"key": key, "dest": dest})

	// One resolution per destination at a time, whatever method wins. The lock
	// must cover the PEER branch too: peers.Fetch does its own clear-and-rename
	// on the same dest, and outside the lock a fetch racing a local copy (or
	// another fetch of a DIFFERENT key — dest is caller-supplied and distinct
	// keys legitimately share one) could lose its rename and report success
	// while delivering another artifact's bytes.
	releaseDest, err := s.acquireDest(ctx, dest)
	if err != nil {
		return artifactwire.ResolveResponse{Status: "error", Error: fmt.Sprintf("waiting for destination %q: %v", dest, err)}
	}
	defer releaseDest()

	// The output plane's hold on the DESTINATION, asked once for all three
	// branches below. Every one of them clears dest and renames over it --
	// copyArtifact for the registry and filesystem branches, peers.FetchInto
	// for the peer one -- and dest is caller-supplied. Asking here rather than
	// in each writer is what makes "the peer branch forgot" impossible.
	//
	// Method "refused" here is the same signal failedResolve sets for a
	// containment refusal, so this answers 422 too. That is the right shape: a
	// hold is a considered answer about this destination, not a symptom of a
	// sick daemon, and the register route already reports the same condition as
	// a 4xx. Retrying it ten times inside one init container never released it.
	if class, err := s.refuseIfCaptureHeldPath(dest); err != nil {
		logger.Info("refused-resolve-into-capture-source", lager.Data{"class": string(class)})
		return artifactwire.ResolveResponse{Status: "error", Method: "refused", Error: err.Error()}
	}

	// Step 1: Check registry for explicit registration.
	//
	// artifactwire.ResolveResponse.Source stays the ABSOLUTE path. It is response JSON the
	// ATC logs and surfaces, so it is an external contract, not an internal
	// representation — the one place in this handler where the ambient form is
	// the right answer rather than a leftover.
	sourceLoc, found := s.lookupRegistry(key)
	if found {
		sourcePath := s.registry.AmbientPath(sourceLoc)
		if err := s.copyArtifactGuarded(ctx, sourceLoc, dest); err != nil {
			logger.Error("copy-failed", err, lager.Data{"source": sourcePath})
			return failedResolve(sourcePath, "local", err)
		}
		duration := time.Since(start)
		logger.Info("resolved", lager.Data{"method": "registry", "source": sourcePath, "duration": duration.String()})
		return artifactwire.ResolveResponse{Status: "ok", Source: sourcePath, Method: "registry", Duration: duration.String()}
	}

	// Step 2: Fallback — check if key maps to a steps/ directory on disk.
	stepsPath, keyErr := s.artifactLocation(filepath.Join(s.storagePath, "steps"), key)
	if keyErr != nil {
		return failedResolve("", "", keyErr)
	}
	if info, err := os.Stat(stepsPath); err == nil && info.IsDir() {
		// Take the location Register stored rather than deriving a second one.
		stepsLoc, err := s.registry.Register(key, stepsPath)
		if err != nil {
			logger.Error("register-failed", err, lager.Data{"source": stepsPath})
			return failedResolve(stepsPath, "filesystem", err)
		}

		if err := s.copyArtifactGuarded(ctx, stepsLoc, dest); err != nil {
			logger.Error("copy-failed", err, lager.Data{"source": stepsPath})
			return failedResolve(stepsPath, "filesystem", err)
		}
		duration := time.Since(start)
		logger.Info("resolved", lager.Data{"method": "filesystem", "source": stepsPath, "duration": duration.String()})
		return artifactwire.ResolveResponse{Status: "ok", Source: stepsPath, Method: "filesystem", Duration: duration.String()}
	}

	// Step 3: Query peer daemons for cross-node resolution.
	if s.peers != nil {
		peerIP, found := s.peers.Probe(ctx, key)
		if found {
			if err := s.fetchFromPeer(ctx, peerIP, key, dest); err != nil {
				logger.Error("peer-fetch-failed", err, lager.Data{"peer": peerIP})
				return failedResolve(peerIP, "peer", err)
			}
			duration := time.Since(start)
			logger.Info("resolved", lager.Data{"method": "peer", "peer": peerIP, "duration": duration.String()})
			return artifactwire.ResolveResponse{Status: "ok", Source: peerIP, Method: "peer", Duration: duration.String()}
		}
	}

	// Step 4: Not found anywhere.
	duration := time.Since(start)
	logger.Info("not-found", lager.Data{"duration": duration.String()})
	return artifactwire.ResolveResponse{Status: "not_found", Method: "exhausted", Duration: duration.String(), Error: fmt.Sprintf("artifact %q not found on this node or any peer", key)}
}

// handleResolveBatch accepts POST /resolve-batch with a JSON body containing
// {"items": [{key, dest}, ...]}. It resolves all artifacts concurrently and
// returns an aggregated response. If any item fails, the overall status is
// "error".
//
// The HTTP status SEPARATES the ways an item can fail, because they are
// different things to do about it. An artifact that is not on this node or any
// peer is 404; an artifact this daemon REFUSED -- an absolute symlink, a key naming the control ledger
// -- is 422, because the request is well formed and the answer is simply no;
// anything that broke while resolving an artifact that IS here is 500.
// Collapsing all of them into 500 is what this endpoint used to do, and the
// cost is not cosmetic: the only thing a BusyBox wget puts in a build log for
// a non-2xx is the status line -- it discards the body -- so "the artifact is
// missing", "this artifact can never be delivered" and "the daemon is sick"
// reached the operator as the same sentence, and the init container spent ten
// retries and twenty seconds on an answer that was never going to change.
func (s *Server) handleResolveBatch(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
	var req artifactwire.BatchResolveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.refuse(w, r, http.StatusBadRequest, reasonInvalidJSON, fmt.Errorf("invalid JSON: %v", err))
		return
	}

	// Validate EVERY item before starting ANY of them. A partial batch that
	// refuses item 3 after item 1 has already copied is a side effect from a
	// refused request, which must never change storage. This endpoint is
	// mTLS-exempt, so it is the least authenticated way into resolveOne.
	for i, item := range req.Items {
		if err := validateRequestKey(item.Key); err != nil {
			s.refuse(w, r, http.StatusBadRequest, reasonInvalidKey, fmt.Errorf("item %d: %v", i, err))
			return
		}
		if err := validateResolveDest(s.storagePath, item.Dest); err != nil {
			s.refuse(w, r, http.StatusBadRequest, reasonUncontained, fmt.Errorf("item %d: %v", i, err))
			return
		}
		// Authorized in the same pass, for the same reason: refusing item 3
		// after item 1 has copied is a side effect from a refused request.
		if !s.authorizeResolve(item) {
			s.logger.Info("resolve-batch-unauthorized", lager.Data{"item": i, "key": item.Key})
			s.refuse(w, r, http.StatusForbidden, reasonCapability, fmt.Errorf("item %d: resolve capability required", i))
			return
		}
	}

	results := make([]artifactwire.ResolveResponse, len(req.Items))

	var wg sync.WaitGroup
	for i, item := range req.Items {
		wg.Add(1)
		go func(idx int, key, dest string) {
			defer wg.Done()
			// Results are index-assigned, so ordering and partial results survive.
			select {
			case s.resolveSem <- struct{}{}:
			case <-r.Context().Done():
				results[idx] = artifactwire.ResolveResponse{Key: key, Status: "error", Error: r.Context().Err().Error()}
				return
			}
			defer func() { <-s.resolveSem }()
			results[idx] = s.resolveOne(r.Context(), key, dest)
		}(i, item.Key, item.Dest)
	}
	wg.Wait()

	overall, status, summary := summariseBatch(results)

	writeJSON(w, status, artifactwire.BatchResolveResponse{Status: overall, Results: results, Error: summary})
}

// summariseBatch reduces per-item results to the batch's status, HTTP status
// and one-line explanation.
//
// The batch takes the WORST item's status, by resolveStatusRank: a hard error
// outranks a refusal outranks a miss. A batch holding both an absent artifact
// and one that broke mid-copy is a 500, because the 500 is the one that says
// something is wrong with this daemon and might not say it again.
func summariseBatch(results []artifactwire.ResolveResponse) (overall string, status int, summary string) {
	overall, status = "ok", http.StatusOK

	var failures []string
	for _, res := range results {
		if res.Status == "ok" {
			continue
		}
		overall = "error"
		if itemStatus := resolveHTTPStatus(res); resolveStatusRank[itemStatus] > resolveStatusRank[status] {
			status = itemStatus
		}
		reason := res.Error
		if reason == "" {
			reason = res.Status
		}
		failures = append(failures, fmt.Sprintf("%q: %s", res.Key, reason))
	}
	if len(failures) == 0 {
		return overall, status, ""
	}

	return overall, status, fmt.Sprintf("%d of %d artifacts unresolved: %s",
		len(failures), len(results), strings.Join(failures, "; "))
}

// copyArtifactGuarded wraps copyArtifact with the read guard and a
// pre-copy mtime touch: the guard keeps the sweeper from deleting src
// mid-copy (cp -R silently omits files removed before enumeration), and the
// touch makes the sweeper's under-lock re-check spare the directory.
// src is a location under the storage root; dest is an arbitrary absolute path
// on the node (a container's mount), which is why only one of the two is a
// RelKey.
//
// Destination serialisation lives in resolveOne (acquireDest), not here: the
// peer-fetch branch writes the same dest without ever calling this function,
// so a lock taken here covered only two of the three writers.
func (s *Server) copyArtifactGuarded(ctx context.Context, src RelKey, dest string) error {
	release := s.guard.BeginRead(s.stepHandle(src))
	defer release()
	s.touchStepDir(src)

	return s.copyArtifact(ctx, src, dest)
}

// fetchFromPeer is the peer branch's writer, going through the same contained
// destination handle as the local copy. PeerResolver.Fetch would reopen the
// parent from the dest string, which is what let a validated destination be
// swapped for a symlink before the write.
func (s *Server) fetchFromPeer(ctx context.Context, peerIP, key, dest string) (err error) {
	// The staging directory is a sibling of dest, so the parent must exist
	// before the first attempt and the rename must stay on one filesystem.
	//
	// Created component by component along the AUTHORIZED name, refusing
	// symlinks. Neither os.MkdirAll on the raw path nor s.root.MkdirAll on a
	// symlink-resolved one will do: the first escapes the store outright, and
	// the second follows a link the caller planted in its own volume and
	// creates root-owned directories at whatever store path the link names —
	// on a request the daemon then refuses, which is a side effect from a
	// refusal.
	rel, err := s.lexicalDestRel(dest)
	if err != nil {
		return err
	}
	if err := validateResolveDest(s.storagePath, dest); err != nil {
		return err
	}
	if dir := filepath.Dir(osName(rel)); dir != "." {
		created, undo, createErr := s.createDestParent(dir)
		if createErr != nil {
			return fmt.Errorf("create dest parent: %w", createErr)
		}
		created.Close()
		// Everything after this point can still fail — the destination rules
		// can refuse, or the peer can go away across all three attempts — and
		// the directories just created would then be residue from a request
		// that delivered nothing, under a name the caller chose, reclaimed by
		// nothing.
		defer func() {
			if err != nil {
				undo()
			}
		}()
	}

	parent, base, err := s.openDestParent(dest)
	if err != nil {
		return err
	}
	defer parent.Close()

	return s.peers.FetchInto(ctx, peerIP, key, parent, base)
}

// copyArtifact copies the artifact at src into dest, atomically.
//
// Both sides go through a root handle: src through the storage root, dest
// through one handle on its parent. Every later step is relative to that
// handle, so a symlink swapped in mid-copy cannot redirect it.
//
// Copies land in a temp sibling and rename into place, so an interrupted copy
// leaves no partial state at dest to block the retry.
//
// ctx aborts the copy between entries. The caller holds a node-wide resolve
// slot for the whole copy, so a request whose client has already given up
// (the init container's wget timing out) must release that slot instead of
// copying an artifact nobody will read.
func (s *Server) copyArtifact(ctx context.Context, src RelKey, dest string) error {
	// Walked from the storage root, not reopened from dest: a component that
	// passed validation as a directory may be a symlink by now, and only a
	// contained walk refuses it.
	parent, base, err := s.openDestParent(dest)
	if err != nil {
		return err
	}
	defer parent.Close()

	tmp, err := mkdirTempIn(parent, ".cp-tmp-")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	tmpRoot, err := parent.OpenRoot(tmp)
	if err != nil {
		parent.RemoveAll(tmp)
		return fmt.Errorf("open temp dir: %w", err)
	}
	defer tmpRoot.Close()

	if err := copyTree(ctx, s.root, osName(src), tmpRoot); err != nil {
		parent.RemoveAll(tmp)
		return fmt.Errorf("copy %s -> %s: %w", src, dest, err)
	}

	parent.RemoveAll(base)
	if err := promoteDir(parent, tmp, base); err != nil {
		parent.RemoveAll(tmp)
		return fmt.Errorf("rename %s -> %s: %w", tmp, dest, err)
	}
	return nil
}
