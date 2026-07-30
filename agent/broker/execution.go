package broker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type ExecutionState string

const (
	ExecutionPending    ExecutionState = "pending"
	ExecutionAdmitted   ExecutionState = "admitted"
	ExecutionCapturing  ExecutionState = "capturing"
	ExecutionRunning    ExecutionState = "running"
	ExecutionValidating ExecutionState = "validating"
	ExecutionSealing    ExecutionState = "sealing"
	ExecutionSucceeded  ExecutionState = "succeeded"
	ExecutionErrored    ExecutionState = "errored"
	ExecutionCancelled  ExecutionState = "cancelled"
	ExecutionTimedOut   ExecutionState = "timed_out"
)

var forwardExecutionStates = map[ExecutionState]map[ExecutionState]struct{}{
	ExecutionPending: {
		ExecutionAdmitted: {}, ExecutionErrored: {}, ExecutionCancelled: {},
	},
	ExecutionAdmitted: {
		ExecutionCapturing: {}, ExecutionRunning: {}, ExecutionErrored: {},
		ExecutionCancelled: {}, ExecutionTimedOut: {},
	},
	ExecutionCapturing: {
		ExecutionRunning: {}, ExecutionErrored: {}, ExecutionCancelled: {}, ExecutionTimedOut: {},
	},
	ExecutionRunning: {
		ExecutionValidating: {}, ExecutionErrored: {}, ExecutionCancelled: {}, ExecutionTimedOut: {},
	},
	ExecutionValidating: {
		ExecutionSealing: {}, ExecutionErrored: {}, ExecutionCancelled: {}, ExecutionTimedOut: {},
	},
	ExecutionSealing: {
		ExecutionSucceeded: {}, ExecutionErrored: {}, ExecutionTimedOut: {},
	},
}

func ValidateExecutionTransition(from, to ExecutionState) error {
	if from == to {
		if isTerminalExecutionState(from) {
			return fmt.Errorf("broker execution: terminal state %q is immutable", from)
		}
		return nil
	}
	allowed, known := forwardExecutionStates[from]
	if !known {
		return fmt.Errorf("broker execution: state %q is terminal or unknown", from)
	}
	if _, found := allowed[to]; !found {
		return fmt.Errorf("broker execution: invalid state transition %q -> %q", from, to)
	}
	return nil
}

func isTerminalExecutionState(state ExecutionState) bool {
	switch state {
	case ExecutionSucceeded, ExecutionErrored, ExecutionCancelled, ExecutionTimedOut:
		return true
	default:
		return false
	}
}

type ExecutionIdentity struct {
	TeamID         int
	WorkflowRunID  int64
	NodePlanID     string
	ParentAttempt  int
	IdempotencyKey string
	Tool           Tool
	Selector       Selector
	ProfileID      string
	ProfileDigest  string
	InputDigest    string
	Attachments    []string
}

func (identity ExecutionIdentity) Fingerprint() (string, error) {
	if identity.TeamID <= 0 || identity.WorkflowRunID <= 0 || identity.ParentAttempt <= 0 {
		return "", fmt.Errorf("broker execution: positive team, workflow run, and parent attempt are required")
	}
	for name, value := range map[string]string{
		"node plan ID": identity.NodePlanID, "idempotency key": identity.IdempotencyKey,
		"profile ID": identity.ProfileID,
	} {
		if strings.TrimSpace(value) == "" {
			return "", fmt.Errorf("broker execution: %s is required", name)
		}
	}
	if err := validateTool(identity.Tool); err != nil {
		return "", err
	}
	if err := identity.Selector.validate(); err != nil {
		return "", err
	}
	if !digestPattern.MatchString(identity.ProfileDigest) ||
		!digestPattern.MatchString(identity.InputDigest) {
		return "", fmt.Errorf("broker execution: profile and input digests must be exact sha256 digests")
	}
	attachments := append([]string(nil), identity.Attachments...)
	sort.Strings(attachments)
	for index, name := range attachments {
		if !attachmentNamePattern.MatchString(name) {
			return "", fmt.Errorf("broker execution: attachment %q is invalid", name)
		}
		if index > 0 && attachments[index-1] == name {
			return "", fmt.Errorf("broker execution: attachment %q is duplicate", name)
		}
	}
	encoded, err := json.Marshal(struct {
		TeamID         int
		WorkflowRunID  int64
		NodePlanID     string
		ParentAttempt  int
		IdempotencyKey string
		Tool           Tool
		Selector       Selector
		ProfileID      string
		ProfileDigest  string
		InputDigest    string
		Attachments    []string
	}{
		TeamID: identity.TeamID, WorkflowRunID: identity.WorkflowRunID,
		NodePlanID: identity.NodePlanID, ParentAttempt: identity.ParentAttempt,
		IdempotencyKey: identity.IdempotencyKey, Tool: identity.Tool,
		Selector: identity.Selector, ProfileID: identity.ProfileID,
		ProfileDigest: identity.ProfileDigest, InputDigest: identity.InputDigest,
		Attachments: attachments,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
