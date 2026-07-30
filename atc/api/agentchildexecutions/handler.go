package agentchildexecutions

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/broker"
	"github.com/concourse/concourse/agent/broker/transport"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/google/uuid"
)

const maxBrokerRequestBytes int64 = 4 << 20

type HandlerConfig struct {
	Signer                 *CapabilitySigner
	Verifier               *CapabilityVerifier
	Store                  ExecutionStore
	Sealer                 ResultSealer
	ExecutionCapabilityTTL time.Duration
	Now                    func() time.Time
}

type Handler struct{ config HandlerConfig }

func NewHandler(config HandlerConfig) (*Handler, error) {
	if config.Signer == nil || config.Verifier == nil || config.Store == nil || config.Sealer == nil || config.ExecutionCapabilityTTL <= 0 {
		return nil, fmt.Errorf("agent child authority handler: signer, verifier, store, sealer, and execution capability TTL are required")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &Handler{config: config}, nil
}

func (handler *Handler) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	if handler == nil {
		writeSafeError(w, http.StatusInternalServerError)
		return
	}
	if request.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeSafeError(w, http.StatusMethodNotAllowed)
		return
	}
	if !isJSON(request) {
		writeSafeError(w, http.StatusUnsupportedMediaType)
		return
	}
	path, executionID, action, found := handler.route(request.URL.Path)
	if !found {
		writeSafeError(w, http.StatusNotFound)
		return
	}
	token, ok := bearer(request.Header.Get("Authorization"))
	if !ok {
		writeSafeError(w, http.StatusUnauthorized)
		return
	}
	now := handler.config.Now().UTC()
	if path == "admit" {
		scope, profiles, err := handler.config.Verifier.Bootstrap(token, now)
		if err != nil {
			writeSafeError(w, http.StatusUnauthorized)
			return
		}
		service, err := handler.service(scope, profiles)
		if err != nil {
			writeSafeError(w, http.StatusUnauthorized)
			return
		}
		var body transport.AdmitRequest
		if !decodeStrictJSON(w, request, &body) {
			return
		}
		id, err := service.Admit(request.Context(), broker.AdmissionRequest{IdempotencyKey: body.IdempotencyKey, Tool: body.Tool, Selector: body.Selector, ProfileID: body.ProfileID, ProfileDigest: body.ProfileDigest, InputDigest: body.InputDigest, Attachments: append([]string(nil), body.Attachments...)})
		if err != nil {
			writeServiceError(w, err)
			return
		}
		profile, err := profileForAdmission(profiles, body.Tool, body.Selector, body.ProfileID, body.ProfileDigest)
		if err != nil {
			writeSafeError(w, http.StatusUnauthorized)
			return
		}
		capability, err := handler.config.Signer.MintExecution(scope, id, profile, now.Add(-time.Second), now.Add(handler.config.ExecutionCapabilityTTL))
		if err != nil {
			writeSafeError(w, http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, transport.AdmitResponse{ExecutionID: id, ExecutionCapability: capability})
		return
	}
	if _, err := uuid.Parse(executionID); err != nil {
		writeSafeError(w, http.StatusNotFound)
		return
	}
	scope, profile, err := handler.config.Verifier.Execution(token, action, executionID, now)
	if err != nil {
		writeSafeError(w, http.StatusUnauthorized)
		return
	}
	if !handler.executionMatchesScope(w, request, scope, profile, executionID) {
		return
	}
	service, err := handler.service(scope, []broker.Profile{profile})
	if err != nil {
		writeSafeError(w, http.StatusUnauthorized)
		return
	}
	switch action {
	case ActionPhase:
		var body transport.PhaseRequest
		if !decodeStrictJSON(w, request, &body) {
			return
		}
		err = service.Phase(request.Context(), executionID, body.Phase)
	case ActionUpdate:
		var body transport.UpdateRequest
		if !decodeStrictJSON(w, request, &body) {
			return
		}
		err = service.Update(request.Context(), executionID, body.Update)
	case ActionTerminal:
		var body transport.TerminalRequest
		if !decodeStrictJSON(w, request, &body) {
			return
		}
		err = service.Terminal(request.Context(), executionID, body.Terminal)
	case ActionSeal:
		var body transport.SealRequest
		if !decodeStrictJSON(w, request, &body) {
			return
		}
		if body.Request.ExecutionID != executionID {
			writeSafeError(w, http.StatusBadRequest)
			return
		}
		var sealed snapshot.SnapshotRef
		sealed, err = service.Seal(request.Context(), body.Request)
		if err == nil {
			writeJSON(w, http.StatusOK, sealed)
			return
		}
	}
	if err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (handler *Handler) route(path string) (string, string, CapabilityAction, bool) {
	if path == transport.AdmitPath {
		return "admit", "", ActionAdmit, true
	}
	prefix := "/api/v1/internal/agent-child-executions/"
	if !strings.HasPrefix(path, prefix) {
		return "", "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(path, prefix), "/")
	if len(parts) != 2 || parts[0] == "" {
		return "", "", "", false
	}
	switch parts[1] {
	case "phase":
		return "lifecycle", parts[0], ActionPhase, true
	case "update":
		return "lifecycle", parts[0], ActionUpdate, true
	case "terminal":
		return "lifecycle", parts[0], ActionTerminal, true
	case "seal":
		return "lifecycle", parts[0], ActionSeal, true
	default:
		return "", "", "", false
	}
}

func (handler *Handler) service(scope Scope, profiles []broker.Profile) (*Service, error) {
	catalog, err := catalogFromResolved(profiles)
	if err != nil {
		return nil, err
	}
	return NewService(Config{Scope: scope, Catalog: catalog, Store: handler.config.Store, Sealer: handler.config.Sealer})
}

func catalogFromResolved(profiles []broker.Profile) (*broker.Catalog, error) {
	input := make([]broker.Profile, len(profiles))
	for index, profile := range profiles {
		if err := broker.ValidateResolvedProfile(profile); err != nil {
			return nil, err
		}
		profile.Digest = ""
		input[index] = profile
	}
	return broker.NewCatalog(input)
}

func profileForAdmission(profiles []broker.Profile, tool broker.Tool, selector broker.Selector, id, digest string) (broker.Profile, error) {
	catalog, err := catalogFromResolved(profiles)
	if err != nil {
		return broker.Profile{}, err
	}
	profile, err := catalog.Resolve(tool, selector)
	if err != nil || profile.ID != id || profile.Digest != digest {
		return broker.Profile{}, ErrInvalidCapability
	}
	return profile, nil
}

func (handler *Handler) executionMatchesScope(w http.ResponseWriter, request *http.Request, scope Scope, profile broker.Profile, executionID string) bool {
	execution, found, err := handler.config.Store.Find(request.Context(), scope.TeamID, executionID)
	if err != nil {
		writeSafeError(w, http.StatusInternalServerError)
		return false
	}
	if !found || execution.TeamID != scope.TeamID || execution.WorkflowRunID != scope.WorkflowRunID || execution.NodePlanID != scope.NodePlanID || execution.ParentAttempt != scope.ParentAttempt || execution.BrokerInstance != scope.BrokerInstance || execution.ProfileID != profile.ID || execution.ProfileDigest != profile.Digest {
		writeSafeError(w, http.StatusUnauthorized)
		return false
	}
	return true
}

func isJSON(request *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	return err == nil && mediaType == "application/json"
}
func bearer(header string) (string, bool) {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) || strings.TrimSpace(strings.TrimPrefix(header, prefix)) == "" {
		return "", false
	}
	return strings.TrimPrefix(header, prefix), true
}
func decodeStrictJSON(w http.ResponseWriter, request *http.Request, target any) bool {
	request.Body = http.MaxBytesReader(w, request.Body, maxBrokerRequestBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeSafeError(w, http.StatusRequestEntityTooLarge)
		} else {
			writeSafeError(w, http.StatusBadRequest)
		}
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeSafeError(w, http.StatusBadRequest)
		return false
	}
	return true
}
func writeSafeError(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"error":"agent child execution request rejected"}`))
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeServiceError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if strings.Contains(err.Error(), "not found") {
		status = http.StatusNotFound
	} else if strings.Contains(err.Error(), "sequence conflict") || strings.Contains(err.Error(), "transition") || strings.Contains(err.Error(), "expected") {
		status = http.StatusConflict
	}
	writeSafeError(w, status)
}

var _ http.Handler = (*Handler)(nil)
