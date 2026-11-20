package main

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path"
	"path/filepath"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3"

	"github.com/concourse/concourse/artifactwire"
	"github.com/concourse/concourse/cmd/artifact-daemon/outputplane"
)

func (s *Server) handleGetArtifact(w http.ResponseWriter, r *http.Request) {
	key, err := s.artifactKey(r)
	if err != nil {
		s.refuse(w, r, http.StatusBadRequest, reasonInvalidKey, err)
		return
	}
	// The request key IS a location under the root: artifactKey has already run
	// it through artifactLocation. This is the boundary where a validated
	// request key becomes a RelKey, and the only reason the conversion is
	// sound.
	loc := RelKey(key)

	// Check filesystem first, then fall back to registry aliases.
	// This enables peer daemons to serve registry-only artifacts
	// (e.g., resource caches registered via POST /register).
	//
	// The fallback used to substitute an ABSOLUTE alias path, which forced this
	// handler to carry a servedFromRegistry flag and branch between os.Open and
	// root.Open — and migrating one branch and not the other is how this broke
	// once already. Registry values are now locations under the same root, so
	// there is one representation and one code path.
	//
	// FILESYSTEM FIRST is what makes a read-only alias shadowable, and the
	// argument that closes it is written here rather than in a phase document,
	// because it is the thing that could stop being true.
	//
	// The shape: a capture-selected output gets a read-only alias so that
	// downstream steps resolve it in the ordinary way, and this handler stats
	// the literal path before consulting the registry. refuseIfCaptureHeld
	// classifies the KEY the caller named -- `steps/<volume handle>` -- which
	// is not the held incarnation `steps/<exec>.<gen>/result`, so the
	// output-plane guard has nothing to say about it: read-only is enforced at
	// the incarnation, not across the name that points at it. A literal planted
	// at the alias key would therefore be served to a peer GET in place of the
	// captured tree, and the held incarnation would be untouched and none the
	// wiser. Demonstrated.
	//
	// What closes it is NOT "there is no production output read path" -- that
	// is true of the MANAGED read, and the alias is not a managed read; it is a
	// production path today and cross-node it comes through here. What closes
	// it is that the alias key is `ArtifactKey(vol.Handle())`, the OUTPUT
	// volume's handle verbatim, and no production route creates a literal
	// there: handleStreamIn, the one route that writes under a key, is
	// mTLS-protected and is driven at INPUT volume handles, which are different
	// volumes with different handles. The shadow needs a literal at the alias
	// key and nothing makes one.
	//
	// If that ever stops being true -- a route that writes at an output volume
	// handle, or an alias keyed on something a caller can also name -- the fix
	// is the daemon-wide precedence rule resolveOne already uses: consult the
	// registry before the filesystem.
	//
	// statLocation, not s.root.Stat: a location reached through a symlink is
	// one tarTree refuses, and refusing it here answers 422 instead of a 200
	// followed by an aborted stream.
	info, err := statLocation(s.root, osName(loc))
	if errors.Is(err, fs.ErrNotExist) {
		if regLoc, found := s.lookupRegistryAlias(r); found {
			loc = regLoc
			info, err = statLocation(s.root, osName(loc))
		}
	}
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		if errors.Is(err, ErrRefused) {
			s.refuse(w, r, http.StatusUnprocessableEntity, reasonSymlinkedPath, err)
			return
		}
		s.logger.Error("failed-to-stat-artifact", err, lager.Data{"rel": string(loc)})
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// An artifact is a directory: stream-in extracts one, a step pod writes
	// one, and /register accepts nothing else. A file at the location is not an
	// artifact the artifact daemon can serve -- it was planted, or the store is damaged
	// -- and that is the daemon's fault to report, not a miss for the caller to
	// go looking elsewhere for.
	if !info.IsDir() {
		s.logger.Error("artifact-location-is-not-a-directory", errNotADirectory, lager.Data{"rel": string(loc)})
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Hold the read guard while serving so the sweeper cannot delete the
	// directory mid-stream. Released via defer: the tar-abort path panics.
	release := s.guard.BeginRead(s.stepHandle(loc))
	defer release()
	s.touchStepDir(loc)

	w.Header().Set("Content-Type", "application/x-tar")
	if err := s.tarDirectory(w, loc); err != nil {
		s.logger.Error("failed-to-tar-artifact", err, lager.Data{"rel": string(loc)})
		// The 200 header and part of the body are already out; abort
		// the connection so the client sees a hard failure instead of
		// a clean-looking truncated tar.
		panic(http.ErrAbortHandler)
	}
}

// errNotADirectory is the one reason a location that exists is still not an
// artifact. /register refuses it at the door; GET and HEAD report it as the
// store's fault when they meet it anyway.
var errNotADirectory = errors.New("is not a directory")

// tarDirectory writes a tar archive of the directory to w. Any error —
// including a file changing or disappearing mid-walk — is returned so the
// caller can abort the response; a silently truncated tar reads as complete
// on the client side.
func (s *Server) tarDirectory(w io.Writer, loc RelKey) error {
	return tarTree(w, s.root, loc)
}

// touchStepDir bumps the mtime of the steps/{handle} directory containing rel
// (when rel is under steps/) so the TTL sweeper treats actively-read artifacts
// as fresh. Best-effort, and through the root handle.
func (s *Server) touchStepDir(rel RelKey) {
	segments := strings.Split(string(rel), "/")
	if len(segments) < 2 || segments[0] != "steps" {
		return
	}
	now := time.Now()
	_ = s.root.Chtimes(path.Join("steps", segments[1]), now, now)
}

// captureRefusal answers a destructive request the output plane's ledger
// refused, in one place so every path refuses with the same status, the same
// reason code and the same log line.
//
// s.refuse already names the route and the reason; this adds the two facts it
// cannot know -- which location, and which class the ledger answered.
func (s *Server) captureRefusal(w http.ResponseWriter, r *http.Request, loc RelKey,
	class outputplane.Class, err error) {
	s.refuse(w, r, http.StatusConflict, reasonCaptureHeld, err)
	s.logger.Info("refused-destructive-operation-on-capture-source", lager.Data{
		"rel": string(loc), "class": string(class),
	})
}

// handleStreamIn accepts a tar stream (optionally gzip-compressed) and extracts
// it to steps/{key}/ so that resolveOne can discover it via the filesystem
// fallback. The key is also registered in the in-memory registry for fast lookups.
//
// Gzip is auto-detected by peeking at the first two bytes for the gzip magic
// number (\x1f\x8b). This allows both raw tar (from DaemonSetVolume.StreamIn)
// and gzipped tar (from fly CLI uploads) to work.
func (s *Server) handleStreamIn(w http.ResponseWriter, r *http.Request) {
	key, err := s.requestKey(r, artifactwire.StreamInPrefix)
	if err != nil {
		s.refuse(w, r, http.StatusBadRequest, reasonInvalidKey, err)
		return
	}

	// Structural-name AUTHORIZATION survives the handle: os.Root has no opinion
	// about WHICH names a per-artifact verb may address and will happily create
	// steps/aliases.json. Containment and authorization were once fused here,
	// and replacing only the containment half let PUT /stream-in/aliases.json
	// return 201.
	if err := rejectStructuralName(key); err != nil {
		s.refuse(w, r, http.StatusBadRequest, reasonStructuralName, err)
		return
	}

	// No containment check here: it is a property of stepsRoot below, where
	// every operation resolves inside the handle or fails.
	//
	// The stream-in key is relative to the STEPS root while the registry and
	// the guard are relative to the storage root, so the location carries the
	// "steps/" segment. Both are RelKey, so getting it wrong is not a compile
	// error — stated once here and derived everywhere else.
	loc := RelKey(path.Join("steps", key))
	dest := filepath.Join(s.storagePath, "steps", key)

	// The output plane's hold. Stream-in REPLACES: it removes whatever is at
	// the key and renames a freshly extracted tree over it, so pointed at a
	// held source it is the whole damage in one call -- and it leaves a tree
	// that looks perfectly healthy.
	if class, err := s.refuseIfCaptureHeld(loc); err != nil {
		s.captureRefusal(w, r, loc, class, err)
		return
	}

	// The steps/ boundary is a HANDLE, not an argument.
	//
	// An earlier track passed the boundary as a parameter and then validated
	// against a different one, so a symlink under steps/ pointing at the store
	// root satisfied "contained" while plainly escaping steps/. A nested root
	// cannot be passed and ignored: every operation below resolves inside it or
	// fails.
	if err := s.root.MkdirAll("steps", 0755); err != nil {
		s.logger.Error("failed-to-create-stream-in-dir", err, lager.Data{"key": key})
		http.Error(w, "create dir: "+err.Error(), http.StatusInternalServerError)
		return
	}
	stepsRoot, err := s.root.OpenRoot("steps")
	if err != nil {
		s.logger.Error("failed-to-open-steps-root", err, lager.Data{"key": key})
		http.Error(w, "open steps root: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer stepsRoot.Close()

	// Extract into a temp sibling and rename into place on success. Readers
	// (resolve, GET/HEAD, peer probes) treat any existing steps/{key} dir as
	// a complete artifact, so partial state — an in-flight extraction or a
	// failed upload — must never be visible at the final path.
	tmpName, err := mkdirTempIn(stepsRoot, ".in-tmp-")
	if err != nil {
		s.logger.Error("failed-to-create-stream-in-tmp-dir", err, lager.Data{"key": key})
		http.Error(w, "create tmp dir: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// No-op after the successful rename; cleans up on every error path.
	defer stepsRoot.RemoveAll(tmpName)

	// Auto-detect gzip by peeking at the first 2 bytes.
	br := bufio.NewReader(r.Body)
	var tarSource io.Reader = br
	if magic, err := br.Peek(2); err == nil && magic[0] == 0x1f && magic[1] == 0x8b {
		gr, err := gzip.NewReader(br)
		if err != nil {
			s.logger.Error("failed-to-open-gzip", err, lager.Data{"key": key})
			s.refuse(w, r, http.StatusBadRequest, reasonArchive, fmt.Errorf("gzip: %w", err))
			return
		}
		defer gr.Close()
		tarSource = gr
	}

	// One extraction implementation, not two. This handler carried its own
	// tar loop with a strings.HasPrefix boundary and an unvalidated
	// os.Symlink; extractTarToDir has driven every entry through an os.Root
	// handle since Track 1 and has been adversarially reviewed five times.
	//
	// What stays here: the gzip peek above (extractTarToDir takes a plain
	// reader), the temp-dir/rename machinery below, and the registry
	// registration. Only the loop goes.
	tmpRoot, err := stepsRoot.OpenRoot(tmpName)
	if err != nil {
		s.logger.Error("failed-to-open-tmp-root", err, lager.Data{"key": key})
		http.Error(w, "open tmp root: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer tmpRoot.Close()

	if err := extractTarToRoot(tarSource, tmpRoot); err != nil {
		// Attribution, not containment. mirror.go reads any non-201 from a
		// peer as "rejected", so an archive-attributable failure reported as
		// 500 makes a poisoned artifact look like a peer fault.
		if errors.Is(err, ErrRefused) {
			s.refuse(w, r, http.StatusBadRequest, reasonArchive, err)
			return
		}
		s.logger.Error("failed-to-extract-stream-in", err, lager.Data{"key": key})
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Move the fully-extracted tree into place. Remove any previous copy
	// first (rename onto a non-empty dir fails). The replace is destructive
	// like a sweep: take the handle's exclusive lock so in-flight reads
	// (resolve copies, GET/mirror tar walks) never see a half-removed tree.
	if dir := path.Dir(key); dir != "." {
		if err := stepsRoot.MkdirAll(dir, 0755); err != nil {
			s.logger.Error("failed-to-create-stream-in-parent", err, lager.Data{"key": key})
			http.Error(w, "create parent: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	renameErr := func() error {
		// stepHandle(loc) is the same key the sweeper derives from the handle
		// directory's name. That equality is what makes this lock exclude the
		// sweeper at all; when the two representations disagreed it did not.
		release := s.guard.BeginSweep(s.stepHandle(loc))
		defer release()
		stepsRoot.RemoveAll(key)
		return promoteDir(stepsRoot, tmpName, key)
	}()
	if renameErr != nil {
		s.logger.Error("failed-to-rename-stream-in", renameErr, lager.Data{"key": key, "tmp": tmpName})
		http.Error(w, "rename: "+renameErr.Error(), http.StatusInternalServerError)
		return
	}

	if _, err := s.registry.Register(key, dest); err != nil {
		// The tree is already in place under the root handle, so this cannot
		// mean an escape — only that storagePath itself stopped resolving.
		// Serving it would mean reporting 201 for an artifact no lookup can
		// find, which presents later as an unexplained cache miss.
		s.logger.Error("failed-to-register-stream-in", err, lager.Data{"key": key, "dest": dest})
		http.Error(w, "register: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.logger.Info("stream-in-complete", lager.Data{"key": key, "rel": string(loc)})

	// Schedule outbound mirror to peer daemons so the new artifact survives
	// loss of this node. Best-effort: the trigger queues a background job
	// and returns immediately; mirror is disabled by passing a nil trigger.
	// Writes that themselves arrived via a peer's mirror must NOT re-trigger
	// (see MirrorOriginHeader) — the origin daemon already fanned out, and
	// re-mirroring makes daemons ping-pong the same key indefinitely.
	if s.mirrorTrigger != nil && r.Header.Get(MirrorOriginHeader) == "" {
		s.mirrorTrigger(r.Context(), key)
	}

	w.WriteHeader(http.StatusCreated)
}

func (s *Server) handleDeleteArtifact(w http.ResponseWriter, r *http.Request) {
	key, err := s.artifactKey(r)
	if err != nil {
		s.refuse(w, r, http.StatusBadRequest, reasonInvalidKey, err)
		return
	}
	loc := RelKey(key) // validated by artifactKey

	// The output plane's hold, before anything is removed. An ordinary path is
	// unchanged; a capture-held or sealed one is refused, and so is a path this
	// daemon could not classify.
	if class, err := s.refuseIfCaptureHeld(loc); err != nil {
		s.refuse(w, r, http.StatusConflict, reasonCaptureHeld, err)
		s.logger.Info("refused-delete-of-capture-source", lager.Data{
			"rel": string(loc), "class": string(class),
		})
		return
	}

	// Deletion is destructive like a sweep: wait out in-flight reads so a
	// concurrent copy never sees a half-removed tree.
	release := s.guard.BeginSweep(s.stepHandle(loc))
	defer release()

	if err := s.root.RemoveAll(osName(loc)); err != nil {
		s.logger.Error("failed-to-delete-artifact", err, lager.Data{"rel": string(loc)})
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// refuseIfCaptureHeld is the one place this daemon asks the output plane's
// ledger, so every destructive path asks the same question the same way.
//
// The key is namespaced -- artifacts live under "steps/" -- and the ledger
// speaks in incarnations relative to that directory, so the prefix is stripped
// here rather than at each call site. A key outside steps/ is not a source
// incarnation and cannot be held.
func (s *Server) refuseIfCaptureHeld(loc RelKey) (outputplane.Class, error) {
	if s.sourceLedger == nil {
		// Tests may omit the classifier; NewServer always installs it.
		return outputplane.Unmanaged, nil
	}

	relative, inSteps := strings.CutPrefix(string(loc), artifactwire.StepsPrefix)
	if !inSteps {
		return outputplane.Unmanaged, nil
	}

	class := s.sourceLedger.Classify(relative)
	if class.Destructive() {
		return class, nil
	}

	return class, s.sourceLedger.Reason(relative, class)
}

// handleCaptureClass answers what the output ledger says about a step
// directory, for the one caller that cannot ask any other way.
//
// The `rm -rf` cleanup init container is the most destructive thing in a Pod
// and it is the only destructive path on a node that reached no guard: every
// other one -- DELETE /artifacts, the sweeper, a stream-in replacement, a
// registry remap or reuse -- goes through refuseIfCaptureHeld. That container
// holds no client certificate, so /artifacts/ is closed to it; this is the
// question it asks instead, and the script refuses to remove anything the
// answer does not say is unmanaged.
//
// It is deliberately not a delete: giving an unauthenticated route the ability
// to remove bytes is the shape this whole guard exists to prevent. It says what
// the ledger says and the caller decides.
func (s *Server) handleCaptureClass(w http.ResponseWriter, r *http.Request) {
	handle := r.PathValue("handle")
	if handle == "" {
		s.refuse(w, r, http.StatusBadRequest, reasonInvalidKey,
			errors.New("no step directory named"))

		return
	}
	key := artifactwire.StepsPrefix + handle
	if err := validateRequestKey(key); err != nil {
		s.refuse(w, r, http.StatusBadRequest, reasonInvalidKey, err)

		return
	}

	class, reason := s.refuseIfCaptureHeld(RelKey(key))
	body := artifactwire.CaptureClassResponse{Class: string(class), Handle: handle, Reason: outputplane.PublicReason(class)}
	if reason != nil {
		// The detailed reason goes to the LOG and not to the body. This route
		// requires no client certificate -- a pod on this node has to be able
		// to ask about its own step directory -- so its audience is every pod
		// on the node, task pods included, and the ledger's `unavailable` text
		// carries the node's control-directory path and the raw OS error.
		// Those are exactly the values the output plane's own redaction rule
		// forbids any route to emit, and this route was never held to it.
		s.logger.Info("capture-class", lager.Data{
			"class": string(class), "handle": handle, "detail": reason.Error(),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(body)
}

// refuseIfCaptureHeldPath is the same question for a caller holding an ABSOLUTE
// path rather than a key: a resolve destination and a register's local_path are
// both client-supplied absolute paths inside the store.
//
// A path that will not relativize into the store is not a step directory
// and cannot be held. This must not become a second, weaker containment check:
// the callers that take one validate containment separately, and this returns
// "unmanaged" rather than an error for anything it cannot place.
func (s *Server) refuseIfCaptureHeldPath(absolute string) (outputplane.Class, error) {
	if s.sourceLedger == nil {
		return outputplane.Unmanaged, nil
	}

	rel, err := containedRelKey(s.storagePath, absolute)
	if err != nil {
		return outputplane.Unmanaged, nil
	}

	return s.refuseIfCaptureHeld(rel)
}

func (s *Server) handleHeadArtifact(w http.ResponseWriter, r *http.Request) {
	key, err := s.artifactKey(r)
	if err != nil {
		s.refuse(w, r, http.StatusBadRequest, reasonInvalidKey, err)
		return
	}
	loc := RelKey(key) // canonical: validateRequestKey refuses anything else

	// Check filesystem first, then fall back to registry aliases. Both stats go
	// through the root handle now that the registry value is a location under
	// the same root, and both under GET's rule: a symlinked location is refused.
	info, err := statLocation(s.root, osName(loc))
	if errors.Is(err, fs.ErrNotExist) {
		if regLoc, found := s.lookupRegistryAlias(r); found {
			loc = regLoc
			info, err = statLocation(s.root, osName(loc))
		}
	}
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if errors.Is(err, ErrRefused) {
			s.refuse(w, r, http.StatusUnprocessableEntity, reasonSymlinkedPath, err)
			return
		}
		s.logger.Error("failed-to-stat-artifact", err, lager.Data{"key": key})
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	// The same rule GET applies: only a directory is an artifact.
	if !info.IsDir() {
		s.logger.Error("artifact-location-is-not-a-directory", errNotADirectory, lager.Data{"rel": string(loc)})
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// lookupRegistryAlias checks the registry for an artifact key extracted from
// the request URL. Peer probes send URLs like /artifacts/steps/rc-42, yielding
// the key "steps/rc-42" — but the registry stores just "rc-42". We try the
// full key first, then strip common prefixes.
func (s *Server) lookupRegistryAlias(r *http.Request) (RelKey, bool) {
	key, err := s.requestKey(r, artifactwire.ArtifactsPrefix)
	if err != nil {
		return "", false
	}
	if rel, found := s.lookupRegistry(key); found {
		return rel, true
	}
	// Strip "steps/" prefix — peer probes prepend it but aliases don't have it.
	if stripped := strings.TrimPrefix(key, artifactwire.StepsPrefix); stripped != key {
		if rel, found := s.lookupRegistry(stripped); found {
			return rel, true
		}
	}
	return "", false
}
