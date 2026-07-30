// Package nodes serves the reusable, atomic node-definition catalog.
package nodes

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc/api/accessor"
)

// maxManifestRequestBytes admits a fully valid 10 MiB manifest even when
// every source byte needs JSON's six-byte control-character escape, while
// retaining a fixed upper bound on the request body.
const maxManifestRequestBytes = 64 << 20

type Handler struct{ store workflow.NodeStore }

func NewHandler(store workflow.NodeStore) *Handler { return &Handler{store: store} }

// NodeSummary is the latest imported version for a node name.
type NodeSummary struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	LatestVersion int    `json:"latest_version"`
	ContentHash   string `json:"content_hash"`
	CreatedAt     int64  `json:"created_at"`
}

func requestUser(r *http.Request) string {
	claims := accessor.GetAccessor(r).Claims()
	if claims.PreferredUsername != "" {
		return claims.PreferredUsername
	}
	return claims.UserName
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (h *Handler) List(w http.ResponseWriter, _ *http.Request) {
	definitions, err := h.store.List()
	if err != nil {
		http.Error(w, "failed to list nodes", http.StatusInternalServerError)
		return
	}
	summaries := make([]NodeSummary, 0, len(definitions))
	for _, definition := range definitions {
		summaries = append(summaries, NodeSummary{
			Name: definition.Name, Description: definition.Description, LatestVersion: definition.Version,
			ContentHash: definition.ContentHash, CreatedAt: definition.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, summaries)
}

func (h *Handler) Versions(w http.ResponseWriter, r *http.Request) {
	request, err := parseVersionPageRequest(r.URL.Query())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	page, err := h.store.Versions(r.Context(), r.FormValue(":node_name"), request)
	if err != nil {
		http.Error(w, "failed to list node versions", http.StatusInternalServerError)
		return
	}
	if !page.Found {
		http.Error(w, "unknown node", http.StatusNotFound)
		return
	}
	if page.NextCursor > 0 {
		cursor := strconv.Itoa(page.NextCursor)
		w.Header().Set("X-Next-Cursor", cursor)
		query := url.Values{"cursor": {cursor}, "limit": {strconv.Itoa(request.Limit)}}
		w.Header().Set("Link", "<"+r.URL.EscapedPath()+"?"+query.Encode()+`>; rel="next"`)
	}
	writeJSON(w, http.StatusOK, page.Definitions)
}

func parseVersionPageRequest(values url.Values) (workflow.VersionPageRequest, error) {
	request := workflow.VersionPageRequest{Limit: workflow.DefaultVersionPageSize}
	if raw, ok := values["limit"]; ok {
		if len(raw) != 1 {
			return workflow.VersionPageRequest{}, errors.New("limit must be specified once")
		}
		limit, err := strconv.Atoi(raw[0])
		if err != nil || limit <= 0 || limit > workflow.MaxVersionPageSize {
			return workflow.VersionPageRequest{}, fmt.Errorf("limit must be an integer between 1 and %d", workflow.MaxVersionPageSize)
		}
		request.Limit = limit
	}
	if raw, ok := values["cursor"]; ok {
		if len(raw) != 1 {
			return workflow.VersionPageRequest{}, errors.New("cursor must be specified once")
		}
		cursor, err := strconv.Atoi(raw[0])
		if err != nil || cursor <= 0 || cursor > workflow.MaxWorkflowVersion {
			return workflow.VersionPageRequest{}, fmt.Errorf("cursor must be a node version between 1 and %d", workflow.MaxWorkflowVersion)
		}
		request.Cursor = cursor
	}
	return request, nil
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	version, err := parseVersion(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	definition, found, err := h.store.Get(r.FormValue(":node_name"), version)
	if err != nil {
		http.Error(w, "failed to get node version", http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "unknown node version", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, definition)
}

type importRequest struct {
	Files workflow.Manifest `json:"files"`
}

func (h *Handler) Import(w http.ResponseWriter, r *http.Request) {
	if !isJSONContentType(r) {
		http.Error(w, `node import requires application/json {"files": {...}}`, http.StatusBadRequest)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxManifestRequestBytes+1))
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}
	if len(raw) > maxManifestRequestBytes {
		http.Error(w, "manifest exceeds 64 MiB", http.StatusRequestEntityTooLarge)
		return
	}
	var request importRequest
	if err := decodeStrictJSON(raw, &request); err != nil {
		http.Error(w, "malformed manifest body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(request.Files) == 0 {
		http.Error(w, `manifest body must carry a non-empty "files" map`, http.StatusBadRequest)
		return
	}
	definition, err := h.store.ImportManifest(r.FormValue(":node_name"), request.Files, requestUser(r))
	if err != nil {
		var invalid workflow.InvalidDefinitionError
		if errors.As(err, &invalid) {
			http.Error(w, invalid.Error(), http.StatusBadRequest)
			return
		}
		http.Error(w, "failed to import node", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, definition)
}

type releaseRequest struct {
	Compatibility workflow.ReleaseCompatibility `json:"compatibility"`
}

func (h *Handler) Release(w http.ResponseWriter, r *http.Request) {
	version, err := parseVersion(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var request releaseRequest
	if err := decodeJSONRequest(r, &request); err != nil {
		http.Error(w, "malformed release body: "+err.Error(), http.StatusBadRequest)
		return
	}
	release, err := h.store.Release(r.FormValue(":node_name"), version, request.Compatibility, requestUser(r))
	if errors.Is(err, workflow.ErrVersionNotFound) {
		http.Error(w, "unknown node version", http.StatusNotFound)
		return
	}
	if errors.Is(err, workflow.ErrInvalidCompatibility) {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	if err != nil {
		http.Error(w, "failed to release node", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, release)
}

type deprecationRequest struct {
	Deprecated *bool `json:"deprecated"`
}

func (h *Handler) Deprecate(w http.ResponseWriter, r *http.Request) {
	version, err := parseVersion(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var request deprecationRequest
	if err := decodeJSONRequest(r, &request); err != nil || request.Deprecated == nil {
		if err == nil {
			err = errors.New(`body must carry "deprecated"`)
		}
		http.Error(w, "malformed deprecation body: "+err.Error(), http.StatusBadRequest)
		return
	}
	name := r.FormValue(":node_name")
	if err := h.store.Deprecate(name, version, *request.Deprecated, requestUser(r)); err != nil {
		if errors.Is(err, workflow.ErrVersionNotFound) {
			http.Error(w, "unknown node version", http.StatusNotFound)
			return
		}
		http.Error(w, "failed to update node deprecation", http.StatusInternalServerError)
		return
	}
	definition, found, err := h.store.Get(name, version)
	if err != nil {
		http.Error(w, "failed to get node version", http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "unknown node version", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, definition)
}

func parseVersion(r *http.Request) (int, error) {
	version, err := strconv.Atoi(r.FormValue(":version"))
	if err != nil || version <= 0 {
		return 0, errors.New("version must be a positive integer")
	}
	return version, nil
}

func decodeJSONRequest(r *http.Request, value any) error {
	if !isJSONContentType(r) {
		return errors.New("request requires application/json")
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20+1))
	if err != nil {
		return err
	}
	if len(raw) > 1<<20 {
		return errors.New("request body exceeds 1 MiB")
	}
	return decodeStrictJSON(raw, value)
}

func isJSONContentType(r *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mediaType == "application/json"
}

func decodeStrictJSON(raw []byte, value any) error {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return errors.New("request body must contain one JSON value")
		}
		return err
	}
	return nil
}
