package agentchildexecutions

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/broker"
	"github.com/concourse/concourse/agent/snapshot"
)

const (
	capabilityDomain   = "jb-agent-child-execution-v1"
	capabilityAudience = "agent-child-executions"
	capabilityVersion  = 1
	capabilityNonceLen = 16
	capabilityKeyLen   = sha256.Size
)

var ErrInvalidCapability = errors.New("invalid agent child execution capability")

type CapabilityAction string

const (
	ActionAdmit                   CapabilityAction = "admit"
	ActionPhase                   CapabilityAction = "phase"
	ActionUpdate                  CapabilityAction = "update"
	ActionTerminal                CapabilityAction = "terminal"
	ActionSeal                    CapabilityAction = "seal"
	ActionCaptureWorkspace        CapabilityAction = "capture_workspace"
	ActionCaptureWorkspaceFailure CapabilityAction = "capture_workspace_failure"
)

type capabilityClaims struct {
	Domain    string             `json:"domain"`
	Version   int                `json:"version"`
	KeyID     string             `json:"key_id"`
	Audience  string             `json:"audience"`
	Actions   []CapabilityAction `json:"actions"`
	Resource  string             `json:"resource"`
	Scope     Scope              `json:"scope"`
	Profiles  []broker.Profile   `json:"profiles"`
	NotBefore int64              `json:"not_before"`
	ExpiresAt int64              `json:"expires_at"`
	Nonce     string             `json:"nonce"`
}

// CapabilitySigner is ATC-only. Its root key must never be projected into a
// broker sidecar; sidecars receive only already-signed bearer capabilities.
type CapabilitySigner struct {
	keyID string
	key   [capabilityKeyLen]byte
}

type CapabilityVerifier struct {
	keyID string
	key   [capabilityKeyLen]byte
}

type CapabilityCheck struct {
	Action   CapabilityAction
	Resource string
	Scope    Scope
	Now      time.Time
}

func NewCapabilitySigner(keyID string, key []byte) (*CapabilitySigner, error) {
	if strings.TrimSpace(keyID) == "" || len(key) != capabilityKeyLen {
		return nil, fmt.Errorf("agent child capability: key ID and exactly %d-byte key are required", capabilityKeyLen)
	}
	var copied [capabilityKeyLen]byte
	copy(copied[:], key)
	return &CapabilitySigner{keyID: keyID, key: copied}, nil
}

func NewCapabilityVerifier(keyID string, key []byte) (*CapabilityVerifier, error) {
	if strings.TrimSpace(keyID) == "" || len(key) != capabilityKeyLen {
		return nil, fmt.Errorf("agent child capability: key ID and exactly %d-byte key are required", capabilityKeyLen)
	}
	var copied [capabilityKeyLen]byte
	copy(copied[:], key)
	return &CapabilityVerifier{keyID: keyID, key: copied}, nil
}

// MintBootstrap grants exactly admission under a frozen scope and catalog.
func (signer *CapabilitySigner) MintBootstrap(scope Scope, profiles []broker.Profile, notBefore, expiresAt time.Time) (string, error) {
	return signer.mint([]CapabilityAction{ActionAdmit}, "admit", scope, profiles, notBefore, expiresAt)
}

// MintExecution grants the lifecycle actions for one durable execution only.
func (signer *CapabilitySigner) MintExecution(scope Scope, executionID string, profile broker.Profile, notBefore, expiresAt time.Time) (string, error) {
	return signer.mint([]CapabilityAction{ActionPhase, ActionTerminal, ActionUpdate, ActionSeal}, executionID, scope, []broker.Profile{profile}, notBefore, expiresAt)
}

func (signer *CapabilitySigner) MintReviewPhase(scope Scope, executionID string, profile broker.Profile, notBefore, expiresAt time.Time) (string, error) {
	return signer.mint([]CapabilityAction{ActionPhase}, executionID, scope, []broker.Profile{profile}, notBefore, expiresAt)
}

func (signer *CapabilitySigner) MintWorkspaceCapture(scope Scope, executionID string, profile broker.Profile, notBefore, expiresAt time.Time) (string, error) {
	return signer.mint([]CapabilityAction{ActionCaptureWorkspace, ActionCaptureWorkspaceFailure}, executionID, scope, []broker.Profile{profile}, notBefore, expiresAt)
}

func (signer *CapabilitySigner) mint(actions []CapabilityAction, resource string, scope Scope, profiles []broker.Profile, notBefore, expiresAt time.Time) (string, error) {
	if signer == nil {
		return "", fmt.Errorf("agent child capability: signer is required")
	}
	nonce := make([]byte, capabilityNonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("agent child capability: generate nonce: %w", err)
	}
	claims := capabilityClaims{Domain: capabilityDomain, Version: capabilityVersion, KeyID: signer.keyID, Audience: capabilityAudience, Actions: append([]CapabilityAction(nil), actions...), Resource: resource, Scope: scope, Profiles: cloneProfiles(profiles), NotBefore: notBefore.Unix(), ExpiresAt: expiresAt.Unix(), Nonce: base64.RawURLEncoding.EncodeToString(nonce)}
	if err := validateClaims(claims); err != nil {
		return "", fmt.Errorf("agent child capability: %w", err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("agent child capability: marshal claims: %w", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	signed := capabilityDomain + "." + encoded
	mac := hmac.New(sha256.New, signer.key[:])
	_, _ = mac.Write([]byte(signed))
	return signed + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (verifier *CapabilityVerifier) Verify(token string, check CapabilityCheck) (Scope, error) {
	if verifier == nil || !validCapabilityMAC(verifier.key[:], token) {
		return Scope{}, ErrInvalidCapability
	}
	parts := strings.Split(token, ".")
	payload, err := base64.RawURLEncoding.Strict().DecodeString(parts[1])
	if err != nil {
		return Scope{}, ErrInvalidCapability
	}
	var claims capabilityClaims
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&claims); err != nil {
		return Scope{}, ErrInvalidCapability
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Scope{}, ErrInvalidCapability
	}
	canonical, err := json.Marshal(claims)
	if err != nil || !bytes.Equal(payload, canonical) || claims.KeyID != verifier.keyID || validateClaims(claims) != nil {
		return Scope{}, ErrInvalidCapability
	}
	if !scopeEqual(claims.Scope, check.Scope) || !equalCapabilityString(claims.Resource, check.Resource) || !containsAction(claims.Actions, check.Action) {
		return Scope{}, ErrInvalidCapability
	}
	if check.Now.IsZero() || check.Now.Before(time.Unix(claims.NotBefore, 0)) || !check.Now.Before(time.Unix(claims.ExpiresAt, 0)) {
		return Scope{}, ErrInvalidCapability
	}
	return claims.Scope, nil
}

func (verifier *CapabilityVerifier) Bootstrap(token string, now time.Time) (Scope, []broker.Profile, error) {
	claims, err := verifier.claims(token, now, ActionAdmit, "admit")
	if err != nil || len(claims.Actions) != 1 || claims.Actions[0] != ActionAdmit {
		return Scope{}, nil, ErrInvalidCapability
	}
	return claims.Scope, cloneProfiles(claims.Profiles), nil
}

func (verifier *CapabilityVerifier) Execution(token string, action CapabilityAction, executionID string, now time.Time) (Scope, broker.Profile, error) {
	claims, err := verifier.claims(token, now, action, executionID)
	if err != nil || len(claims.Profiles) != 1 || len(claims.Actions) != 4 || containsAction(claims.Actions, ActionAdmit) || !containsAction(claims.Actions, ActionPhase) || !containsAction(claims.Actions, ActionUpdate) || !containsAction(claims.Actions, ActionTerminal) || !containsAction(claims.Actions, ActionSeal) {
		return Scope{}, broker.Profile{}, ErrInvalidCapability
	}
	return claims.Scope, claims.Profiles[0], nil
}

func (verifier *CapabilityVerifier) PhaseExecution(token, executionID string, now time.Time) (Scope, broker.Profile, bool, error) {
	claims, err := verifier.claims(token, now, ActionPhase, executionID)
	if err != nil || len(claims.Profiles) != 1 {
		return Scope{}, broker.Profile{}, false, ErrInvalidCapability
	}
	if len(claims.Actions) == 1 && claims.Actions[0] == ActionPhase {
		return claims.Scope, claims.Profiles[0], true, nil
	}
	if len(claims.Actions) != 4 || containsAction(claims.Actions, ActionAdmit) ||
		!containsAction(claims.Actions, ActionPhase) || !containsAction(claims.Actions, ActionUpdate) ||
		!containsAction(claims.Actions, ActionTerminal) || !containsAction(claims.Actions, ActionSeal) {
		return Scope{}, broker.Profile{}, false, ErrInvalidCapability
	}
	return claims.Scope, claims.Profiles[0], false, nil
}

func (verifier *CapabilityVerifier) WorkspaceExecution(token string, action CapabilityAction, executionID string, now time.Time) (Scope, broker.Profile, error) {
	claims, err := verifier.claims(token, now, action, executionID)
	if err != nil || len(claims.Profiles) != 1 || len(claims.Actions) != 2 ||
		!containsAction(claims.Actions, ActionCaptureWorkspace) ||
		!containsAction(claims.Actions, ActionCaptureWorkspaceFailure) {
		return Scope{}, broker.Profile{}, ErrInvalidCapability
	}
	return claims.Scope, claims.Profiles[0], nil
}

func (verifier *CapabilityVerifier) claims(token string, now time.Time, action CapabilityAction, resource string) (capabilityClaims, error) {
	if verifier == nil || !validCapabilityMAC(verifier.key[:], token) {
		return capabilityClaims{}, ErrInvalidCapability
	}
	parts := strings.Split(token, ".")
	payload, err := base64.RawURLEncoding.Strict().DecodeString(parts[1])
	if err != nil {
		return capabilityClaims{}, ErrInvalidCapability
	}
	var claims capabilityClaims
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&claims) != nil || (!errors.Is(decoder.Decode(&struct{}{}), io.EOF)) {
		return capabilityClaims{}, ErrInvalidCapability
	}
	canonical, err := json.Marshal(claims)
	if err != nil || !bytes.Equal(payload, canonical) || claims.KeyID != verifier.keyID || validateClaims(claims) != nil || !equalCapabilityString(claims.Resource, resource) || !containsAction(claims.Actions, action) || now.IsZero() || now.Before(time.Unix(claims.NotBefore, 0)) || !now.Before(time.Unix(claims.ExpiresAt, 0)) {
		return capabilityClaims{}, ErrInvalidCapability
	}
	return claims, nil
}

func validCapabilityMAC(key []byte, token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != capabilityDomain || parts[1] == "" || parts[2] == "" {
		return false
	}
	provided, err := base64.RawURLEncoding.Strict().DecodeString(parts[2])
	if err != nil || len(provided) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(parts[0] + "." + parts[1]))
	return hmac.Equal(provided, mac.Sum(nil))
}

func validateClaims(claims capabilityClaims) error {
	if claims.Domain != capabilityDomain || claims.Version != capabilityVersion || strings.TrimSpace(claims.KeyID) == "" || claims.Audience != capabilityAudience || strings.TrimSpace(claims.Resource) == "" || claims.NotBefore <= 0 || claims.ExpiresAt <= claims.NotBefore || claims.Scope.Validate() != nil {
		return ErrInvalidCapability
	}
	nonce, err := base64.RawURLEncoding.Strict().DecodeString(claims.Nonce)
	if err != nil || len(nonce) != capabilityNonceLen || base64.RawURLEncoding.EncodeToString(nonce) != claims.Nonce || len(claims.Actions) == 0 {
		return ErrInvalidCapability
	}
	seenActions := make(map[CapabilityAction]struct{}, len(claims.Actions))
	for _, action := range claims.Actions {
		if action != ActionAdmit && action != ActionPhase && action != ActionUpdate && action != ActionTerminal && action != ActionSeal && action != ActionCaptureWorkspace && action != ActionCaptureWorkspaceFailure {
			return ErrInvalidCapability
		}
		if _, found := seenActions[action]; found {
			return ErrInvalidCapability
		}
		seenActions[action] = struct{}{}
	}
	if len(claims.Profiles) == 0 {
		return ErrInvalidCapability
	}
	for _, profile := range claims.Profiles {
		if err := broker.ValidateResolvedProfile(profile); err != nil {
			return ErrInvalidCapability
		}
		for _, tool := range profile.Tools {
			if tool == broker.ToolRequestReview && claims.Scope.WorkspaceBase == nil {
				return ErrInvalidCapability
			}
		}
	}
	return nil
}

func containsAction(actions []CapabilityAction, expected CapabilityAction) bool {
	for _, action := range actions {
		if equalCapabilityString(string(action), string(expected)) {
			return true
		}
	}
	return false
}
func equalCapabilityString(left, right string) bool { return hmac.Equal([]byte(left), []byte(right)) }
func scopeEqual(left, right Scope) bool {
	return left.TeamID == right.TeamID &&
		left.BuildID == right.BuildID &&
		left.WorkflowDefinitionID == right.WorkflowDefinitionID &&
		left.WorkflowRunID == right.WorkflowRunID &&
		left.ParentAttempt == right.ParentAttempt &&
		left.LeaseDuration == right.LeaseDuration &&
		equalCapabilityString(left.TeamName, right.TeamName) &&
		equalCapabilityString(left.SnapshotCreatedBy, right.SnapshotCreatedBy) &&
		equalCapabilityString(left.NodePlanID, right.NodePlanID) &&
		equalCapabilityString(left.BrokerInstance, right.BrokerInstance) &&
		scopeInputsEqual(left.Inputs, right.Inputs) &&
		snapshotRefPointerEqual(left.WorkspaceBase, right.WorkspaceBase)
}

func snapshotRefPointerEqual(left, right *snapshot.SnapshotRef) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func scopeInputsEqual(left, right map[string]snapshot.SnapshotRef) bool {
	if len(left) != len(right) {
		return false
	}
	for _, name := range sortedInputNames(left) {
		rightRef, found := right[name]
		if !found || left[name] != rightRef {
			return false
		}
	}
	return true
}
func cloneProfiles(source []broker.Profile) []broker.Profile {
	result := append([]broker.Profile(nil), source...)
	sort.Slice(result, func(i, j int) bool {
		if result[i].ID != result[j].ID {
			return result[i].ID < result[j].ID
		}
		return result[i].Digest < result[j].Digest
	})
	return result
}
