// Package contracttest holds the publisher gateway conformance kit and the
// in-repo reference implementation of the gateway protocol documented in
// docs/agentic/README.md. It is a normal (non-test) package so both
// agent/publisher's tests and out-of-repo gateway implementations can import
// it.
package contracttest

import (
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
)

const (
	gitPublisher      = "git-publisher/v1"
	workItemPublisher = "work-item-publisher/v1"

	maxReferenceOperationBytes = 1 << 20
)

// Fault injects exactly one protocol deviation. The zero Fault is a fully
// conforming gateway.
type Fault struct {
	// BlindLookup answers found:false forever, even after a publish landed.
	// This is the eventual-consistency adversary: a gateway whose read side
	// lags its write side.
	BlindLookup bool
	// FailAfterFirstPublish records the first publish durably and then answers
	// with this status. It reproduces "the write landed, the caller never
	// learned the answer".
	FailAfterFirstPublish int
}

// ReferenceOptions configures the reference gateway.
type ReferenceOptions struct {
	Token       string // required bearer token; defaults to "mounted-token"
	CurrentBase string // /v1/git/current-base answer; must be a full lowercase object ID
	Fault       Fault
}

type referenceResult struct {
	ExternalID string `json:"external_id,omitempty"`
	URL        string `json:"url,omitempty"`
	HeadSHA    string `json:"head_sha,omitempty"`
}

// ReferenceServer is a strict, durably-idempotent gateway over TLS.
type ReferenceServer struct {
	Server *httptest.Server

	mu              sync.Mutex
	options         ReferenceOptions
	paths           []string
	keys            map[string][]string
	results         map[string]referenceResult
	publishRequests int
	effects         int
	failedOnce      bool
}

// NewReferenceServer starts the reference gateway and closes it on cleanup.
func NewReferenceServer(t *testing.T, options ReferenceOptions) *ReferenceServer {
	t.Helper()
	if options.Token == "" {
		options.Token = "mounted-token"
	}
	if options.CurrentBase == "" {
		options.CurrentBase = strings.Repeat("1", 40)
	}
	reference := &ReferenceServer{
		options: options,
		keys:    map[string][]string{},
		results: map[string]referenceResult{},
	}
	reference.Server = httptest.NewTLSServer(http.HandlerFunc(reference.serve))
	t.Cleanup(reference.Server.Close)
	return reference
}

// SetFault replaces the injected fault. It is safe to call between requests.
func (reference *ReferenceServer) SetFault(fault Fault) {
	reference.mu.Lock()
	defer reference.mu.Unlock()
	reference.options.Fault = fault
}

// Paths returns the ordered gateway request log.
func (reference *ReferenceServer) Paths() []string {
	reference.mu.Lock()
	defer reference.mu.Unlock()
	return slices.Clone(reference.paths)
}

// IdempotencyKeys returns the ordered Idempotency-Key values seen at path.
func (reference *ReferenceServer) IdempotencyKeys(path string) []string {
	reference.mu.Lock()
	defer reference.mu.Unlock()
	return slices.Clone(reference.keys[path])
}

// PublishRequests counts publish requests received, deduplicated or not.
func (reference *ReferenceServer) PublishRequests() int {
	reference.mu.Lock()
	defer reference.mu.Unlock()
	return reference.publishRequests
}

// ExternalEffects counts distinct operation keys that produced a result —
// the number of side effects a real provider would have performed.
func (reference *ReferenceServer) ExternalEffects() int {
	reference.mu.Lock()
	defer reference.mu.Unlock()
	return reference.effects
}

func (reference *ReferenceServer) serve(response http.ResponseWriter, request *http.Request) {
	reference.mu.Lock()
	reference.paths = append(reference.paths, request.URL.Path)
	if key := request.Header.Get("Idempotency-Key"); key != "" {
		reference.keys[request.URL.Path] = append(reference.keys[request.URL.Path], key)
	}
	token := reference.options.Token
	reference.mu.Unlock()

	if request.Method != http.MethodPost {
		writeReferenceError(response, http.StatusMethodNotAllowed, "every gateway endpoint is POST")
		return
	}
	if request.Header.Get("Authorization") != "Bearer "+token {
		writeReferenceError(response, http.StatusUnauthorized, "a valid bearer token is required")
		return
	}
	switch request.URL.Path {
	case "/v1/publications/lookup":
		reference.lookup(response, request)
	case "/v1/git/current-base":
		reference.currentBase(response, request)
	case "/v1/git/publish":
		reference.publish(response, request, gitPublisher)
	case "/v1/work-items/publish":
		reference.publish(response, request, workItemPublisher)
	default:
		writeReferenceError(response, http.StatusNotFound, "unknown gateway endpoint")
	}
}

func (reference *ReferenceServer) lookup(response http.ResponseWriter, request *http.Request) {
	var body struct {
		Publisher    string `json:"publisher"`
		OperationKey string `json:"operation_key"`
	}
	if !decodeReferenceJSON(response, request, &body) {
		return
	}
	if body.OperationKey == "" || request.Header.Get("Idempotency-Key") != body.OperationKey {
		writeReferenceError(response, http.StatusBadRequest, "Idempotency-Key must equal operation_key")
		return
	}
	reference.mu.Lock()
	defer reference.mu.Unlock()
	if reference.options.Fault.BlindLookup {
		writeReferenceJSON(response, map[string]any{"found": false})
		return
	}
	result, found := reference.results[body.Publisher+"\x00"+body.OperationKey]
	if !found {
		writeReferenceJSON(response, map[string]any{"found": false})
		return
	}
	writeReferenceJSON(response, map[string]any{"found": true, "result": result})
}

func (reference *ReferenceServer) currentBase(response http.ResponseWriter, request *http.Request) {
	var body struct {
		Destination  string `json:"destination"`
		TargetBranch string `json:"target_branch"`
	}
	if !decodeReferenceJSON(response, request, &body) {
		return
	}
	if body.Destination == "" || body.TargetBranch == "" {
		writeReferenceError(response, http.StatusBadRequest, "destination and target_branch are required")
		return
	}
	// The current-base exemption: this endpoint identifies no operation, so
	// the client sends no Idempotency-Key and the gateway must not demand one.
	reference.mu.Lock()
	base := reference.options.CurrentBase
	reference.mu.Unlock()
	writeReferenceJSON(response, map[string]string{"base_sha": base})
}

func (reference *ReferenceServer) publish(response http.ResponseWriter, request *http.Request, publisherType string) {
	reader, err := request.MultipartReader()
	if err != nil {
		writeReferenceError(response, http.StatusBadRequest, "publish must be multipart/form-data")
		return
	}
	operationPart, err := reader.NextPart()
	if err != nil || operationPart.FormName() != "operation" {
		writeReferenceError(response, http.StatusBadRequest, "the first part must be named operation")
		return
	}
	var operation struct {
		OperationKey string `json:"operation_key"`
		Destination  string `json:"destination"`
		ResultSHA    string `json:"result_sha"`
	}
	if err := json.NewDecoder(io.LimitReader(operationPart, maxReferenceOperationBytes)).Decode(&operation); err != nil {
		writeReferenceError(response, http.StatusBadRequest, "the operation part must be JSON")
		return
	}
	if operation.OperationKey == "" || request.Header.Get("Idempotency-Key") != operation.OperationKey {
		writeReferenceError(response, http.StatusBadRequest, "Idempotency-Key must equal operation_key")
		return
	}
	contentPart, err := reader.NextPart()
	if err != nil || contentPart.FormName() != "snapshot" {
		writeReferenceError(response, http.StatusBadRequest, "the second part must be named snapshot")
		return
	}
	if _, err := io.Copy(io.Discard, contentPart); err != nil {
		writeReferenceError(response, http.StatusBadRequest, "the snapshot part is unreadable")
		return
	}

	reference.mu.Lock()
	defer reference.mu.Unlock()
	reference.publishRequests++
	key := publisherType + "\x00" + operation.OperationKey
	result, found := reference.results[key]
	if !found {
		reference.effects++
		result = referenceResult{
			ExternalID: fmt.Sprintf("publication/%d", reference.effects),
			URL:        fmt.Sprintf("https://gateway.example/publications/%d", reference.effects),
		}
		if publisherType == gitPublisher {
			result.HeadSHA = operation.ResultSHA
		}
		// Durability first: the result is recorded before the response is
		// written, so a caller that never sees the response can still recover
		// it through lookup. That ordering is the whole contract.
		reference.results[key] = result
	}
	if status := reference.options.Fault.FailAfterFirstPublish; status != 0 && !reference.failedOnce {
		reference.failedOnce = true
		writeReferenceError(response, status, "injected failure after the write landed")
		return
	}
	writeReferenceJSON(response, result)
}

func decodeReferenceJSON(response http.ResponseWriter, request *http.Request, value any) bool {
	decoder := json.NewDecoder(io.LimitReader(request.Body, maxReferenceOperationBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		writeReferenceError(response, http.StatusBadRequest, "request body is not the documented JSON object")
		return false
	}
	return true
}

func writeReferenceJSON(response http.ResponseWriter, value any) {
	response.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(response).Encode(value)
}

func writeReferenceError(response http.ResponseWriter, status int, detail string) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(map[string]string{"error": detail})
}

// ReferenceCAPEM is the reference gateway's certificate in the PEM form
// publisher.GatewayConfig.CACertificateFile expects.
func ReferenceCAPEM(reference *ReferenceServer) []byte {
	return pem.EncodeToMemory(&pem.Block{
		Type: "CERTIFICATE", Bytes: reference.Server.Certificate().Raw,
	})
}
