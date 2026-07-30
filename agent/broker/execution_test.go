package broker_test

import (
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/broker"
)

func TestChildExecutionLifecycleAllowsOnlyForwardTransitions(t *testing.T) {
	for _, transition := range [][2]broker.ExecutionState{
		{broker.ExecutionPending, broker.ExecutionAdmitted},
		{broker.ExecutionAdmitted, broker.ExecutionCapturing},
		{broker.ExecutionAdmitted, broker.ExecutionRunning},
		{broker.ExecutionCapturing, broker.ExecutionRunning},
		{broker.ExecutionRunning, broker.ExecutionValidating},
		{broker.ExecutionValidating, broker.ExecutionSealing},
		{broker.ExecutionSealing, broker.ExecutionSucceeded},
		{broker.ExecutionRunning, broker.ExecutionErrored},
		{broker.ExecutionRunning, broker.ExecutionTimedOut},
		{broker.ExecutionRunning, broker.ExecutionCancelled},
	} {
		if err := broker.ValidateExecutionTransition(transition[0], transition[1]); err != nil {
			t.Errorf("%s -> %s: %v", transition[0], transition[1], err)
		}
	}
	for _, transition := range [][2]broker.ExecutionState{
		{broker.ExecutionPending, broker.ExecutionSucceeded},
		{broker.ExecutionRunning, broker.ExecutionAdmitted},
		{broker.ExecutionSucceeded, broker.ExecutionErrored},
		{broker.ExecutionErrored, broker.ExecutionRunning},
	} {
		if err := broker.ValidateExecutionTransition(transition[0], transition[1]); err == nil {
			t.Errorf("%s -> %s unexpectedly allowed", transition[0], transition[1])
		}
	}
}

func TestChildExecutionIdentityFingerprintIsStableAndSensitive(t *testing.T) {
	identity := broker.ExecutionIdentity{
		TeamID: 1, WorkflowRunID: 2, NodePlanID: "review",
		ParentAttempt: 1, IdempotencyKey: "call",
		Tool:      broker.ToolConsultAgent,
		Selector:  broker.Selector{Tier: broker.TierBalanced, Effort: broker.EffortHigh},
		ProfileID: "profile", ProfileDigest: "sha256:" + strings.Repeat("a", 64),
		InputDigest: "sha256:" + strings.Repeat("b", 64),
		Attachments: []string{"design", "workspace"},
	}
	first, err := identity.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	second := identity
	second.Attachments = []string{"workspace", "design"}
	again, err := second.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if first != again {
		t.Fatal("logical attachment order changed immutable identity")
	}
	second.ProfileID = "other"
	changed, _ := second.Fingerprint()
	if first == changed {
		t.Fatal("exact profile change did not change immutable identity")
	}
}
