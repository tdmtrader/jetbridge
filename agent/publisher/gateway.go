package publisher

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

const (
	maxGatewayPolicyBytes   int64 = 1 << 20
	maxGatewayTokenBytes    int64 = 64 << 10
	maxGatewayCABundleBytes int64 = 1 << 20
	maxGatewayMetadataBytes int64 = 1 << 20
	maxGatewayResponseBytes int64 = 16 << 20
)

var ErrDestinationNotAllowed = errors.New("publisher gateway: destination is not allowed")

// GatewayConfig contains only non-secret process configuration. TokenFile is
// a mounted file path: the bearer token itself is never accepted as a flag,
// serialized into a publication, or included in an error.
type GatewayConfig struct {
	Endpoint              string
	PolicyFile            string
	TokenFile             string
	CACertificateFile     string
	RequestTimeout        time.Duration
	LeaseDuration         time.Duration
	MaxResponseBytes      int64
	SnapshotCanonicalizer snapshot.Canonicalizer

	// ActionsMode is the cluster-wide action-suppression switch. It is
	// REQUIRED by NewGatewayExecutor (but deliberately NOT by
	// ValidateGatewayConfig, which runs at flag-validation time before a
	// database connection exists).
	ActionsMode ActionsModeReader
}

// ValidateGatewayConfig performs side-effect-free validation suitable for
// command startup checks. Mounted files are opened only during composition.
func ValidateGatewayConfig(config GatewayConfig) error {
	_, err := config.validate()
	return err
}

func (config GatewayConfig) validate() (*url.URL, error) {
	endpoint, err := parseGatewayEndpoint(config.Endpoint)
	if err != nil {
		return nil, err
	}
	for name, path := range map[string]string{
		"policy file": config.PolicyFile,
		"token file":  config.TokenFile,
	} {
		if path == "" || !filepath.IsAbs(path) {
			return nil, fmt.Errorf("publisher gateway: %s must be an absolute path", name)
		}
	}
	if config.CACertificateFile != "" && !filepath.IsAbs(config.CACertificateFile) {
		return nil, fmt.Errorf("publisher gateway: CA certificate file must be an absolute path")
	}
	if config.RequestTimeout <= 0 || config.RequestTimeout > time.Hour {
		return nil, fmt.Errorf("publisher gateway: request timeout must be within 0-1h")
	}
	if config.LeaseDuration <= 0 || config.LeaseDuration > 24*time.Hour {
		return nil, fmt.Errorf("publisher gateway: lease duration must be within 0-24h")
	}
	if config.LeaseDuration <= config.RequestTimeout {
		return nil, fmt.Errorf("publisher gateway: lease duration must exceed request timeout")
	}
	if config.MaxResponseBytes <= 0 || config.MaxResponseBytes > maxGatewayResponseBytes {
		return nil, fmt.Errorf("publisher gateway: maximum response bytes must be within 1-%d", maxGatewayResponseBytes)
	}
	return endpoint, nil
}

func parseGatewayEndpoint(raw string) (*url.URL, error) {
	endpoint, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("publisher gateway: parse endpoint: %w", err)
	}
	if endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil ||
		endpoint.RawQuery != "" || endpoint.Fragment != "" ||
		(endpoint.Path != "" && endpoint.Path != "/") || endpoint.RawPath != "" {
		return nil, fmt.Errorf("publisher gateway: endpoint must be an HTTPS origin without credentials, path, query, or fragment")
	}
	endpoint.Path = ""
	return endpoint, nil
}

// NewGatewayExecutor composes both provider-neutral publisher backends. The
// same durable store, authorization policy, HTTP transport, and exact snapshot
// stores are shared by Git and work-item operations.
func NewGatewayExecutor(
	store Store,
	metadata snapshot.MetadataStore,
	content snapshot.ContentStore,
	config GatewayConfig,
) (Executor, error) {
	if nilInterface(store) || nilInterface(metadata) || nilInterface(content) {
		return nil, fmt.Errorf("publisher gateway: store, snapshot metadata, and snapshot content are required")
	}
	if nilInterface(config.ActionsMode) {
		return nil, fmt.Errorf("publisher gateway: an actions-mode reader is required; without it the cluster-wide action switch cannot suppress this publisher")
	}
	endpoint, err := config.validate()
	if err != nil {
		return nil, err
	}
	policy, err := loadGatewayPolicy(config.PolicyFile)
	if err != nil {
		return nil, err
	}
	credentials := &gatewayCredentialProvider{policy: policy, tokenFile: config.TokenFile}
	if _, err := credentials.readToken(); err != nil {
		return nil, err
	}
	httpClient, err := newGatewayHTTPClient(config)
	if err != nil {
		return nil, err
	}
	client := &gatewayClient{endpoint: endpoint, http: httpClient, maxResponseBytes: config.MaxResponseBytes}
	changes, err := NewSnapshotChangeInspector(metadata, content, config.SnapshotCanonicalizer)
	if err != nil {
		return nil, err
	}
	gitService, err := NewGitService(store, credentials, changes, gatewayGitBackend{client: client}, config.RequestTimeout, config.LeaseDuration, WithActionsGate(config.ActionsMode))
	if err != nil {
		return nil, err
	}
	values, err := NewSnapshotValueInspector(metadata, content, config.SnapshotCanonicalizer)
	if err != nil {
		return nil, err
	}
	workItemService, err := NewWorkItemService(store, credentials, values, gatewayWorkItemBackend{client: client}, config.RequestTimeout, config.LeaseDuration, WithActionsGate(config.ActionsMode))
	if err != nil {
		return nil, err
	}
	return NewRouter(gitService, workItemService)
}

type gatewayPolicy struct {
	SchemaVersion int                 `json:"schema_version"`
	Rules         []gatewayPolicyRule `json:"rules"`
}

type gatewayPolicyRule struct {
	Team                   string           `json:"team"`
	Publisher              snapshot.TypeRef `json:"publisher"`
	Modes                  []Mode           `json:"modes"`
	ApprovalPolicyVersions []string         `json:"approval_policy_versions"`
	TargetBranches         []string         `json:"target_branches,omitempty"`
	Destinations           []string         `json:"destinations,omitempty"`
	DestinationPrefixes    []string         `json:"destination_prefixes,omitempty"`
}

func loadGatewayPolicy(path string) (gatewayPolicy, error) {
	body, err := readBoundedFile(path, maxGatewayPolicyBytes, "policy")
	if err != nil {
		return gatewayPolicy{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var policy gatewayPolicy
	if err := decoder.Decode(&policy); err != nil {
		return gatewayPolicy{}, fmt.Errorf("publisher gateway: decode policy: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return gatewayPolicy{}, fmt.Errorf("publisher gateway: decode policy: %w", err)
	}
	if policy.SchemaVersion != 1 || len(policy.Rules) == 0 {
		return gatewayPolicy{}, fmt.Errorf("publisher gateway: policy requires schema_version 1 and at least one rule")
	}
	for index, rule := range policy.Rules {
		if !boundedText(rule.Team, 256, false) || (rule.Publisher != GitPublisher && rule.Publisher != WorkItemPublisher) {
			return gatewayPolicy{}, fmt.Errorf("publisher gateway: policy rule %d has an invalid team or publisher", index)
		}
		if len(rule.Destinations) == 0 && len(rule.DestinationPrefixes) == 0 {
			return gatewayPolicy{}, fmt.Errorf("publisher gateway: policy rule %d has no destinations", index)
		}
		if len(rule.Modes) == 0 || len(rule.ApprovalPolicyVersions) == 0 {
			return gatewayPolicy{}, fmt.Errorf("publisher gateway: policy rule %d requires modes and approval_policy_versions", index)
		}
		seenModes := make(map[Mode]struct{}, len(rule.Modes))
		for _, mode := range rule.Modes {
			if _, duplicate := seenModes[mode]; duplicate || !gatewayPolicyModeMatchesPublisher(rule.Publisher, mode) {
				return gatewayPolicy{}, fmt.Errorf("publisher gateway: policy rule %d has an invalid or duplicate mode", index)
			}
			seenModes[mode] = struct{}{}
		}
		if rule.Publisher == GitPublisher && len(rule.TargetBranches) == 0 {
			return gatewayPolicy{}, fmt.Errorf("publisher gateway: policy rule %d requires target_branches for Git", index)
		}
		if rule.Publisher == WorkItemPublisher && len(rule.TargetBranches) != 0 {
			return gatewayPolicy{}, fmt.Errorf("publisher gateway: policy rule %d cannot constrain target_branches for work items", index)
		}
		if err := validateGatewayPolicyStrings(rule.ApprovalPolicyVersions, 128); err != nil {
			return gatewayPolicy{}, fmt.Errorf("publisher gateway: policy rule %d has invalid approval_policy_versions", index)
		}
		if err := validateGatewayPolicyStrings(rule.TargetBranches, 1024); err != nil {
			return gatewayPolicy{}, fmt.Errorf("publisher gateway: policy rule %d has invalid target_branches", index)
		}
		for _, destination := range rule.Destinations {
			if !boundedText(destination, 2048, false) {
				return gatewayPolicy{}, fmt.Errorf("publisher gateway: policy rule %d has an invalid destination", index)
			}
		}
		for _, prefix := range rule.DestinationPrefixes {
			if !boundedText(prefix, 2048, false) {
				return gatewayPolicy{}, fmt.Errorf("publisher gateway: policy rule %d has an invalid destination prefix", index)
			}
		}
	}
	return policy, nil
}

func (policy gatewayPolicy) allows(request Request) bool {
	for _, rule := range policy.Rules {
		if rule.Team != request.Authority.TeamName || rule.Publisher != request.Publisher {
			continue
		}
		if !slicesContains(rule.Modes, request.Mode) ||
			!slicesContains(rule.ApprovalPolicyVersions, request.ApprovalPolicyVersion) {
			continue
		}
		if request.Publisher == GitPublisher && !slicesContains(rule.TargetBranches, request.Parameters["target_branch"]) {
			continue
		}
		for _, destination := range rule.Destinations {
			if destination == request.Destination {
				return true
			}
		}
		for _, prefix := range rule.DestinationPrefixes {
			if strings.HasPrefix(request.Destination, prefix) {
				return true
			}
		}
	}
	return false
}

func gatewayPolicyModeMatchesPublisher(publisher snapshot.TypeRef, mode Mode) bool {
	switch publisher {
	case GitPublisher:
		return mode == ModeBranch || mode == ModePullRequest || mode == ModeMerge
	case WorkItemPublisher:
		return mode == ModeComment || mode == ModeState
	default:
		return false
	}
}

func validateGatewayPolicyStrings(values []string, maximum int) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !boundedText(value, maximum, false) {
			return fmt.Errorf("invalid value")
		}
		if _, duplicate := seen[value]; duplicate {
			return fmt.Errorf("duplicate value")
		}
		seen[value] = struct{}{}
	}
	return nil
}

func slicesContains[T comparable](values []T, expected T) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

type gatewayCredentialProvider struct {
	policy    gatewayPolicy
	tokenFile string
}

func (provider *gatewayCredentialProvider) AuthorizeDestination(ctx context.Context, request Request) (Credential, error) {
	if ctx == nil {
		return Credential{}, fmt.Errorf("%w: context is required", ErrInvalidRequest)
	}
	if err := ctx.Err(); err != nil {
		return Credential{}, err
	}
	if err := request.ValidatePersisted(); err != nil {
		return Credential{}, err
	}
	if !provider.policy.allows(request) {
		return Credential{}, ErrDestinationNotAllowed
	}
	token, err := provider.readToken()
	if err != nil {
		return Credential{}, err
	}
	return Credential{Reference: "publisher-gateway/mounted-token", bearerToken: token}, nil
}

func (provider *gatewayCredentialProvider) readToken() (string, error) {
	body, err := readBoundedFile(provider.tokenFile, maxGatewayTokenBytes, "bearer token")
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(body))
	if token == "" || strings.ContainsAny(token, " \t\r\n\x00") {
		return "", fmt.Errorf("publisher gateway: mounted bearer token is invalid")
	}
	return token, nil
}

func readBoundedFile(path string, maximum int64, label string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("publisher gateway: read mounted %s: %w", label, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("publisher gateway: inspect mounted %s: %w", label, err)
	}
	if !info.Mode().IsRegular() || info.Size() > maximum {
		return nil, fmt.Errorf("publisher gateway: mounted %s must be a regular file of at most %d bytes", label, maximum)
	}
	body, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, fmt.Errorf("publisher gateway: read mounted %s: %w", label, err)
	}
	if int64(len(body)) > maximum {
		return nil, fmt.Errorf("publisher gateway: mounted %s exceeds %d bytes", label, maximum)
	}
	return body, nil
}

func newGatewayHTTPClient(config GatewayConfig) (*http.Client, error) {
	roots, err := x509.SystemCertPool()
	if err != nil || roots == nil {
		roots = x509.NewCertPool()
	}
	if config.CACertificateFile != "" {
		certificate, err := readBoundedFile(config.CACertificateFile, maxGatewayCABundleBytes, "CA certificate")
		if err != nil {
			return nil, err
		}
		if !roots.AppendCertsFromPEM(certificate) {
			return nil, fmt.Errorf("publisher gateway: mounted CA certificate contains no certificates")
		}
	}
	dialTimeout := config.RequestTimeout
	if dialTimeout > 10*time.Second {
		dialTimeout = 10 * time.Second
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   dialTimeout,
		ResponseHeaderTimeout: config.RequestTimeout,
		ExpectContinueTimeout: time.Second,
		DisableCompression:    true,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    roots,
		},
	}
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return fmt.Errorf("publisher gateway: redirects are not allowed")
		},
	}, nil
}

type gatewayClient struct {
	endpoint         *url.URL
	http             *http.Client
	maxResponseBytes int64
}

type gatewayGitBackend struct{ client *gatewayClient }
type gatewayWorkItemBackend struct{ client *gatewayClient }

type gatewayLookupRequest struct {
	Publisher    snapshot.TypeRef `json:"publisher"`
	OperationKey string           `json:"operation_key"`
}

type gatewayLookupResponse struct {
	Found  bool          `json:"found"`
	Result gatewayResult `json:"result,omitempty"`
}

type gatewayResult struct {
	ExternalID string `json:"external_id,omitempty"`
	URL        string `json:"url,omitempty"`
	HeadSHA    string `json:"head_sha,omitempty"`
}

func (backend gatewayGitBackend) Lookup(ctx context.Context, credential Credential, operationKey string) (GitResult, bool, error) {
	result, found, err := backend.client.lookup(ctx, credential, GitPublisher, operationKey)
	return GitResult{ExternalID: result.ExternalID, URL: result.URL, HeadSHA: result.HeadSHA}, found, err
}

func (backend gatewayWorkItemBackend) Lookup(ctx context.Context, credential Credential, operationKey string) (WorkItemResult, bool, error) {
	result, found, err := backend.client.lookup(ctx, credential, WorkItemPublisher, operationKey)
	return WorkItemResult{ExternalID: result.ExternalID, URL: result.URL}, found, err
}

func (client *gatewayClient) lookup(
	ctx context.Context,
	credential Credential,
	publisher snapshot.TypeRef,
	operationKey string,
) (gatewayResult, bool, error) {
	if !operationKeyPattern.MatchString(operationKey) {
		return gatewayResult{}, false, fmt.Errorf("%w: operation key is invalid", ErrInvalidRequest)
	}
	body, err := encodeGatewayJSON(gatewayLookupRequest{Publisher: publisher, OperationKey: operationKey})
	if err != nil {
		return gatewayResult{}, false, err
	}
	responseBody, err := client.do(ctx, credential, "/v1/publications/lookup", operationKey, "application/json", bytes.NewReader(body))
	if err != nil {
		return gatewayResult{}, false, err
	}
	var response gatewayLookupResponse
	if err := decodeGatewayJSON(responseBody, &response); err != nil {
		return gatewayResult{}, false, err
	}
	if !response.Found {
		if response.Result != (gatewayResult{}) {
			return gatewayResult{}, false, fmt.Errorf("publisher gateway: lookup returned a result with found=false")
		}
		return gatewayResult{}, false, nil
	}
	if err := response.Result.validate(publisher); err != nil {
		return gatewayResult{}, false, err
	}
	return response.Result, true, nil
}

func (backend gatewayGitBackend) CurrentBase(
	ctx context.Context,
	credential Credential,
	destination string,
	targetBranch string,
) (string, error) {
	body, err := encodeGatewayJSON(struct {
		Destination  string `json:"destination"`
		TargetBranch string `json:"target_branch"`
	}{Destination: destination, TargetBranch: targetBranch})
	if err != nil {
		return "", err
	}
	responseBody, err := backend.client.do(ctx, credential, "/v1/git/current-base", "", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	var response struct {
		BaseSHA string `json:"base_sha"`
	}
	if err := decodeGatewayJSON(responseBody, &response); err != nil {
		return "", err
	}
	if !validGitObjectID(response.BaseSHA) {
		return "", fmt.Errorf("publisher gateway: current-base response has an invalid base_sha")
	}
	return response.BaseSHA, nil
}

type gatewayGitOperation struct {
	OperationKey          string               `json:"operation_key"`
	Input                 snapshot.SnapshotRef `json:"input"`
	Destination           string               `json:"destination"`
	Mode                  Mode                 `json:"mode"`
	Parameters            map[string]string    `json:"parameters"`
	ApprovalPolicyVersion string               `json:"approval_policy_version"`
	ApprovedBy            string               `json:"approved_by,omitempty"`
	Approval              *ApprovalEvidence    `json:"approval,omitempty"`
	BaseSHA               string               `json:"base_sha"`
	ResultSHA             string               `json:"result_sha"`
	Authority             Authority            `json:"authority"`
}

func (backend gatewayGitBackend) Publish(ctx context.Context, credential Credential, operation GitOperation) (GitResult, error) {
	if !operationKeyPattern.MatchString(operation.OperationKey) || !validGitObjectID(operation.BaseSHA) || !validGitObjectID(operation.ResultSHA) {
		return GitResult{}, fmt.Errorf("%w: gateway Git operation identity is invalid", ErrInvalidRequest)
	}
	if operation.CanonicalArchivePath == "" {
		return GitResult{}, fmt.Errorf("%w: gateway Git operation has no canonical snapshot archive", ErrInvalidRequest)
	}
	metadata, err := encodeGatewayJSON(gatewayGitOperation{
		OperationKey: operation.OperationKey, Input: operation.Input, Destination: operation.Destination,
		Mode: operation.Mode, Parameters: cloneParameters(operation.Parameters),
		ApprovalPolicyVersion: operation.ApprovalPolicyVersion, ApprovedBy: operation.ApprovedBy,
		Approval: cloneApproval(operation.Approval), BaseSHA: operation.BaseSHA, ResultSHA: operation.ResultSHA,
		Authority: operation.Authority,
	})
	if err != nil {
		return GitResult{}, err
	}
	body, contentType := streamGatewaySnapshot(metadata, operation.CanonicalArchivePath)
	defer body.Close()
	responseBody, err := backend.client.do(ctx, credential, "/v1/git/publish", operation.OperationKey, contentType, body)
	if err != nil {
		return GitResult{}, err
	}
	var result gatewayResult
	if err := decodeGatewayJSON(responseBody, &result); err != nil {
		return GitResult{}, err
	}
	if err := result.validate(GitPublisher); err != nil {
		return GitResult{}, err
	}
	return GitResult{ExternalID: result.ExternalID, URL: result.URL, HeadSHA: result.HeadSHA}, nil
}

type gatewayWorkItemOperation struct {
	OperationKey          string               `json:"operation_key"`
	Input                 snapshot.SnapshotRef `json:"input"`
	Destination           string               `json:"destination"`
	Mode                  Mode                 `json:"mode"`
	Parameters            map[string]string    `json:"parameters"`
	ApprovalPolicyVersion string               `json:"approval_policy_version"`
	Authority             Authority            `json:"authority"`
}

func (backend gatewayWorkItemBackend) Publish(ctx context.Context, credential Credential, operation WorkItemOperation) (WorkItemResult, error) {
	if !operationKeyPattern.MatchString(operation.OperationKey) {
		return WorkItemResult{}, fmt.Errorf("%w: gateway work-item operation key is invalid", ErrInvalidRequest)
	}
	if operation.CanonicalArchivePath == "" {
		return WorkItemResult{}, fmt.Errorf("%w: gateway work-item operation has no canonical snapshot archive", ErrInvalidRequest)
	}
	metadata, err := encodeGatewayJSON(gatewayWorkItemOperation{
		OperationKey: operation.OperationKey, Input: operation.Input, Destination: operation.Destination,
		Mode: operation.Mode, Parameters: cloneParameters(operation.Parameters),
		ApprovalPolicyVersion: operation.ApprovalPolicyVersion, Authority: operation.Authority,
	})
	if err != nil {
		return WorkItemResult{}, err
	}
	body, contentType := streamGatewaySnapshot(metadata, operation.CanonicalArchivePath)
	defer body.Close()
	responseBody, err := backend.client.do(ctx, credential, "/v1/work-items/publish", operation.OperationKey, contentType, body)
	if err != nil {
		return WorkItemResult{}, err
	}
	var result gatewayResult
	if err := decodeGatewayJSON(responseBody, &result); err != nil {
		return WorkItemResult{}, err
	}
	if err := result.validate(WorkItemPublisher); err != nil {
		return WorkItemResult{}, err
	}
	return WorkItemResult{ExternalID: result.ExternalID, URL: result.URL}, nil
}

func streamGatewaySnapshot(metadata []byte, archivePath string) (*io.PipeReader, string) {
	reader, writer := io.Pipe()
	multipartWriter := multipart.NewWriter(writer)
	contentType := multipartWriter.FormDataContentType()
	go func() {
		var streamErr error
		defer func() {
			streamErr = errors.Join(streamErr, multipartWriter.Close())
			_ = writer.CloseWithError(streamErr)
		}()
		operationHeader := make(textproto.MIMEHeader)
		operationHeader.Set("Content-Disposition", `form-data; name="operation"`)
		operationHeader.Set("Content-Type", "application/json")
		operationPart, err := multipartWriter.CreatePart(operationHeader)
		if err != nil {
			streamErr = err
			return
		}
		if _, err := operationPart.Write(metadata); err != nil {
			streamErr = err
			return
		}
		archive, err := os.Open(archivePath)
		if err != nil {
			streamErr = fmt.Errorf("publisher gateway: open canonical snapshot archive: %w", err)
			return
		}
		defer func() { streamErr = errors.Join(streamErr, archive.Close()) }()
		info, err := archive.Stat()
		if err != nil || !info.Mode().IsRegular() {
			streamErr = fmt.Errorf("publisher gateway: canonical snapshot archive is not a regular file")
			return
		}
		contentHeader := make(textproto.MIMEHeader)
		contentHeader.Set("Content-Disposition", `form-data; name="snapshot"; filename="snapshot.tar"`)
		contentHeader.Set("Content-Type", "application/x-tar")
		contentPart, err := multipartWriter.CreatePart(contentHeader)
		if err != nil {
			streamErr = err
			return
		}
		_, streamErr = io.Copy(contentPart, archive)
	}()
	return reader, contentType
}

func (client *gatewayClient) do(
	ctx context.Context,
	credential Credential,
	path string,
	operationKey string,
	contentType string,
	body io.Reader,
) ([]byte, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: context is required", ErrInvalidRequest)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := credential.Validate(); err != nil || credential.bearerToken == "" {
		return nil, fmt.Errorf("publisher gateway: authorized mounted credential is unavailable")
	}
	target := *client.endpoint
	target.Path = path
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), body)
	if err != nil {
		return nil, fmt.Errorf("publisher gateway: build request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+credential.bearerToken)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("User-Agent", "jetbridge-agent-publisher-gateway/1")
	if operationKey != "" {
		if !operationKeyPattern.MatchString(operationKey) {
			return nil, fmt.Errorf("%w: operation key is invalid", ErrInvalidRequest)
		}
		request.Header.Set("Idempotency-Key", operationKey)
	}
	response, err := client.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("publisher gateway: request failed: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, client.maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("publisher gateway: read response: %w", err)
	}
	if int64(len(responseBody)) > client.maxResponseBytes {
		return nil, fmt.Errorf("publisher gateway: response exceeds %d bytes", client.maxResponseBytes)
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("publisher gateway: response status %d", response.StatusCode)
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return nil, fmt.Errorf("publisher gateway: response must be application/json")
	}
	return responseBody, nil
}

func encodeGatewayJSON(value any) ([]byte, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("publisher gateway: encode request: %w", err)
	}
	if int64(len(body)) > maxGatewayMetadataBytes {
		return nil, fmt.Errorf("publisher gateway: request metadata exceeds %d bytes", maxGatewayMetadataBytes)
	}
	return body, nil
}

func decodeGatewayJSON(body []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("publisher gateway: decode response: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return fmt.Errorf("publisher gateway: decode response: %w", err)
	}
	return nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

func (result gatewayResult) validate(publisher snapshot.TypeRef) error {
	if !boundedText(result.ExternalID, 4096, false) {
		return fmt.Errorf("publisher gateway: result external_id is invalid")
	}
	if result.URL != "" {
		parsed, err := url.Parse(result.URL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
			return fmt.Errorf("publisher gateway: result URL is invalid")
		}
	}
	switch publisher {
	case GitPublisher:
		if !validGitObjectID(result.HeadSHA) {
			return fmt.Errorf("publisher gateway: Git result head_sha is invalid")
		}
	case WorkItemPublisher:
		if result.HeadSHA != "" {
			return fmt.Errorf("publisher gateway: work-item result contains a head_sha")
		}
	default:
		return fmt.Errorf("publisher gateway: result publisher is invalid")
	}
	return nil
}

func validGitObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}

func cloneApproval(approval *ApprovalEvidence) *ApprovalEvidence {
	if approval == nil {
		return nil
	}
	copy := *approval
	return &copy
}

// SnapshotValueInspector reopens an exact team-authorized snapshot and
// canonicalizes it again immediately before a work-item side effect. Semantic
// validation happened at the sealing boundary; this check proves the bytes
// offered to the gateway still match that immutable value.
type GatewaySnapshotValueInspector struct {
	metadata      snapshot.MetadataStore
	content       snapshot.ContentStore
	canonicalizer snapshot.Canonicalizer
}

func NewSnapshotValueInspector(
	metadata snapshot.MetadataStore,
	content snapshot.ContentStore,
	canonicalizer snapshot.Canonicalizer,
) (*GatewaySnapshotValueInspector, error) {
	if nilInterface(metadata) || nilInterface(content) {
		return nil, fmt.Errorf("publisher gateway: snapshot metadata and content are required")
	}
	return &GatewaySnapshotValueInspector{metadata: metadata, content: content, canonicalizer: canonicalizer}, nil
}

func (inspector *GatewaySnapshotValueInspector) InspectValue(ctx context.Context, request Request) (SnapshotValue, error) {
	if ctx == nil {
		return SnapshotValue{}, fmt.Errorf("%w: context is required", ErrInvalidRequest)
	}
	if err := ctx.Err(); err != nil {
		return SnapshotValue{}, err
	}
	if err := request.ValidatePersisted(); err != nil {
		return SnapshotValue{}, err
	}
	manifest, found, err := inspector.metadata.GetAuthorized(ctx, request.Authority.TeamID, request.Input.ID)
	if err != nil {
		return SnapshotValue{}, fmt.Errorf("publisher gateway: authorize snapshot value: %w", err)
	}
	if !found {
		return SnapshotValue{}, fmt.Errorf("%w: publication input snapshot", snapshot.ErrNotFound)
	}
	if err := manifest.Validate(); err != nil {
		return SnapshotValue{}, fmt.Errorf("publisher gateway: invalid persisted snapshot value: %w", err)
	}
	if manifest.ID != request.Input.ID || manifest.Type != request.Input.Type || manifest.Digest != request.Input.Digest {
		return SnapshotValue{}, fmt.Errorf("publisher gateway: authorized snapshot does not match the exact requested value")
	}
	if manifest.ContentState != snapshot.ContentStateAvailable {
		return SnapshotValue{}, fmt.Errorf("%w: publication input snapshot is %s", snapshot.ErrContentUnavailable, manifest.ContentState)
	}
	reader, err := inspector.content.Open(ctx, manifest)
	if err != nil || reader == nil {
		return SnapshotValue{}, fmt.Errorf("%w: open publication input snapshot", snapshot.ErrContentUnavailable)
	}
	tree, captureErr := inspector.canonicalizer.Capture(ctx, reader)
	closeErr := reader.Close()
	if err := errors.Join(captureErr, closeErr); err != nil {
		if tree != nil {
			_ = tree.Close()
		}
		return SnapshotValue{}, fmt.Errorf("publisher gateway: capture publication input snapshot: %w", err)
	}
	if tree == nil {
		return SnapshotValue{}, fmt.Errorf("publisher gateway: publication input canonicalizer returned no value")
	}
	if tree.Digest != manifest.Digest || tree.ByteSize != manifest.ByteSize || tree.FileCount != manifest.FileCount {
		_ = tree.Close()
		return SnapshotValue{}, fmt.Errorf("publisher gateway: publication input content does not match its sealed manifest")
	}
	return SnapshotValue{CanonicalArchivePath: tree.ArchivePath, close: tree.Close}, nil
}

// SnapshotChangeInspector authorizes the exact snapshot for the persisted
// team, re-hashes its canonical bytes, and checks that record.json and its
// payload still agree with the sealed intrinsic metadata.
type SnapshotChangeInspector struct {
	metadata      snapshot.MetadataStore
	content       snapshot.ContentStore
	canonicalizer snapshot.Canonicalizer
}

func NewSnapshotChangeInspector(
	metadata snapshot.MetadataStore,
	content snapshot.ContentStore,
	canonicalizer snapshot.Canonicalizer,
) (*SnapshotChangeInspector, error) {
	if nilInterface(metadata) || nilInterface(content) {
		return nil, fmt.Errorf("publisher gateway: snapshot metadata and content are required")
	}
	return &SnapshotChangeInspector{metadata: metadata, content: content, canonicalizer: canonicalizer}, nil
}

func (inspector *SnapshotChangeInspector) Inspect(ctx context.Context, request Request) (RepositoryChange, error) {
	if ctx == nil {
		return RepositoryChange{}, fmt.Errorf("%w: context is required", ErrInvalidRequest)
	}
	if err := ctx.Err(); err != nil {
		return RepositoryChange{}, err
	}
	if err := request.ValidatePersisted(); err != nil {
		return RepositoryChange{}, err
	}
	if request.Publisher != GitPublisher || request.Input.Type != snapshot.TypeRef("repository-change/v1") {
		return RepositoryChange{}, fmt.Errorf("%w: snapshot change inspector requires repository-change/v1", ErrInvalidRequest)
	}
	manifest, found, err := inspector.metadata.GetAuthorized(ctx, request.Authority.TeamID, request.Input.ID)
	if err != nil {
		return RepositoryChange{}, fmt.Errorf("publisher gateway: authorize repository change: %w", err)
	}
	if !found {
		return RepositoryChange{}, fmt.Errorf("%w: repository change snapshot", snapshot.ErrNotFound)
	}
	if err := manifest.Validate(); err != nil {
		return RepositoryChange{}, fmt.Errorf("publisher gateway: invalid persisted repository change: %w", err)
	}
	if manifest.ID != request.Input.ID || manifest.Type != request.Input.Type || manifest.Digest != request.Input.Digest {
		return RepositoryChange{}, fmt.Errorf("publisher gateway: authorized snapshot does not match the exact requested reference")
	}
	if manifest.ContentState != snapshot.ContentStateAvailable {
		return RepositoryChange{}, fmt.Errorf("%w: repository change snapshot is %s", snapshot.ErrContentUnavailable, manifest.ContentState)
	}
	metadata, err := decodeRepositoryChangeMetadata(manifest.IntrinsicMetadata)
	if err != nil {
		return RepositoryChange{}, fmt.Errorf("publisher gateway: repository-change intrinsic metadata: %w", err)
	}
	reader, err := inspector.content.Open(ctx, manifest)
	if err != nil {
		return RepositoryChange{}, fmt.Errorf("%w: open repository change: %v", snapshot.ErrContentUnavailable, err)
	}
	if reader == nil {
		return RepositoryChange{}, fmt.Errorf("%w: repository change returned no content", snapshot.ErrContentUnavailable)
	}
	tree, captureErr := inspector.canonicalizer.Capture(ctx, reader)
	closeErr := reader.Close()
	if err := errors.Join(captureErr, closeErr); err != nil {
		if tree != nil {
			_ = tree.Close()
		}
		return RepositoryChange{}, fmt.Errorf("publisher gateway: capture repository change: %w", err)
	}
	if tree.Digest != manifest.Digest || tree.ByteSize != manifest.ByteSize || tree.FileCount != manifest.FileCount {
		_ = tree.Close()
		return RepositoryChange{}, fmt.Errorf("publisher gateway: repository change content does not match its sealed manifest")
	}
	record, err := inspectRepositoryChangeRecord(ctx, tree.Root, manifest.ByteSize)
	if err != nil {
		_ = tree.Close()
		return RepositoryChange{}, err
	}
	document := record.Body
	if document.RepositoryID != metadata.RepositoryID || document.BaseSHA != metadata.BaseSHA ||
		document.ResultCommit != metadata.ResultCommit || document.ResultTree != metadata.ResultTree ||
		document.Representation != metadata.Representation {
		_ = tree.Close()
		return RepositoryChange{}, fmt.Errorf("publisher gateway: repository-change intrinsic metadata does not match exact content")
	}
	if metadata.ResultCommit == "" {
		_ = tree.Close()
		return RepositoryChange{}, fmt.Errorf("publisher gateway: repository change has no publishable result_commit")
	}
	return RepositoryChange{
		BaseSHA: metadata.BaseSHA, ResultSHA: metadata.ResultCommit,
		MaterializedRoot: tree.Root, CanonicalArchivePath: tree.ArchivePath,
		close: tree.Close,
	}, nil
}

// decodeRepositoryChangeMetadata reads the sealed intrinsic metadata and then
// applies the semantic rules the gateway needs. The shape half is delegated so
// that snapshots sealed before the intrinsic-metadata rename stay readable;
// every rule below still runs over the normalized current-shape values.
func decodeRepositoryChangeMetadata(raw json.RawMessage) (contracts.RepositoryChangeMetadata, error) {
	metadata, err := contracts.DecodeRepositoryChangeMetadata(raw)
	if err != nil {
		return contracts.RepositoryChangeMetadata{}, err
	}
	if _, err := snapshot.ParseDigest(metadata.RepositoryID); err != nil {
		return contracts.RepositoryChangeMetadata{}, err
	}
	if !validGitObjectID(metadata.BaseSHA) || !validGitObjectID(metadata.ResultTree) ||
		(metadata.ResultCommit != "" && !validGitObjectID(metadata.ResultCommit)) {
		return contracts.RepositoryChangeMetadata{}, fmt.Errorf("repository object identity is invalid")
	}
	if metadata.Representation != "git-tree" && metadata.Representation != "patch" && metadata.Representation != "git-bundle" {
		return contracts.RepositoryChangeMetadata{}, fmt.Errorf("representation is invalid")
	}
	if !sort.StringsAreSorted(metadata.ChangedFiles) {
		return contracts.RepositoryChangeMetadata{}, fmt.Errorf("changed_files must be sorted")
	}
	for index, name := range metadata.ChangedFiles {
		if strings.TrimSpace(name) == "" || path.IsAbs(name) || path.Clean(name) != name || strings.HasPrefix(name, "../") {
			return contracts.RepositoryChangeMetadata{}, fmt.Errorf("changed_files[%d] is invalid", index)
		}
		if index > 0 && metadata.ChangedFiles[index-1] == name {
			return contracts.RepositoryChangeMetadata{}, fmt.Errorf("changed_files contains duplicate %q", name)
		}
	}
	return metadata, nil
}

func inspectRepositoryChangeRecord(ctx context.Context, rootPath string, maximumPayloadBytes int64) (contracts.Record[contracts.RepositoryChangeBody], error) {
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return contracts.Record[contracts.RepositoryChangeBody]{}, fmt.Errorf("publisher gateway: anchor repository change: %w", err)
	}
	defer root.Close()
	record, err := contracts.ReadSealedRepositoryChangeRecord(ctx, root)
	if err != nil {
		return contracts.Record[contracts.RepositoryChangeBody]{}, fmt.Errorf("publisher gateway: validate repository-change record: %w", err)
	}
	document := record.Body
	payloadInfo, err := root.Lstat(document.Payload.Path)
	if err != nil || !payloadInfo.Mode().IsRegular() || payloadInfo.Size() > maximumPayloadBytes {
		return contracts.Record[contracts.RepositoryChangeBody]{}, fmt.Errorf("publisher gateway: repository change payload must be a bounded regular file")
	}
	payload, err := root.Open(document.Payload.Path)
	if err != nil {
		return contracts.Record[contracts.RepositoryChangeBody]{}, fmt.Errorf("publisher gateway: open repository change payload: %w", err)
	}
	hash := sha256.New()
	copied, copyErr := copyWithContext(ctx, hash, io.LimitReader(payload, maximumPayloadBytes+1))
	closeErr := payload.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return contracts.Record[contracts.RepositoryChangeBody]{}, fmt.Errorf("publisher gateway: hash repository change payload: %w", err)
	}
	if copied > maximumPayloadBytes {
		return contracts.Record[contracts.RepositoryChangeBody]{}, fmt.Errorf("publisher gateway: repository change payload exceeds its sealed snapshot")
	}
	digest := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	if digest != document.Payload.Digest.String() {
		return contracts.Record[contracts.RepositoryChangeBody]{}, fmt.Errorf("publisher gateway: repository change payload digest does not match record.json")
	}
	return record, nil
}

func copyWithContext(ctx context.Context, destination io.Writer, source io.Reader) (int64, error) {
	buffer := make([]byte, 32<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		read, readErr := source.Read(buffer)
		if read > 0 {
			written, writeErr := destination.Write(buffer[:read])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != read {
				return total, io.ErrShortWrite
			}
		}
		if errors.Is(readErr, io.EOF) {
			return total, nil
		}
		if readErr != nil {
			return total, readErr
		}
	}
}
