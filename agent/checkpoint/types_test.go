package checkpoint_test

import (
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/checkpoint"
	"github.com/concourse/concourse/agent/hangar"
)

func TestManifestValidationRejectsUnsafeOrAmbiguousCheckpointShapes(t *testing.T) {
	t.Parallel()

	validArchive, err := hangar.NewObjectRef(
		hangar.KindCheckpoint,
		hangar.Digest("sha256:"+strings.Repeat("a", 64)),
		42,
	)
	if err != nil {
		t.Fatal(err)
	}

	valid := checkpoint.Manifest{
		Version:          1,
		CheckpointID:     44,
		Generation:       3,
		ExecutionAttempt: 2,
		BuildID:          99,
		PlanID:           "plan-id",
		FunctionID:       "implement",
		Provider:         "claude",
		RuntimeImage:     "agent-runner:v1",
		Model:            "claude-test",
		ConfigDigest:     "sha256:" + strings.Repeat("b", 64),
		InputDigest:      "sha256:" + strings.Repeat("c", 64),
		MCPDigest:        "sha256:" + strings.Repeat("d", 64),
		SkillDigest:      "sha256:" + strings.Repeat("e", 64),
		Archive:          &validArchive,
		SessionID:        "session-1",
		TranscriptCursor: 14,
		CompletedToolCallIDs: []string{
			"tool-a",
			"tool-b",
		},
		Effects: []checkpoint.Effect{
			{ToolCallID: "tool-a", ToolName: "read_file", Provider: "claude", AdapterVersion: "1.2.3", ReadOnly: true, State: checkpoint.EffectCommitted},
			{ToolCallID: "tool-b", ToolName: "write_file", Provider: "claude", AdapterVersion: "1.2.3", IdempotencyKey: "write-1", IdempotencyContract: "concourse-write-v1", State: checkpoint.EffectCommitted},
		},
		SafeAt: time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC),
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}

	for name, mutate := range map[string]func(*checkpoint.Manifest){
		"missing identity":                 func(m *checkpoint.Manifest) { m.PlanID = "" },
		"session without combined archive": func(m *checkpoint.Manifest) { m.Archive = nil },
		"noncheckpoint archive":            func(m *checkpoint.Manifest) { m.Archive.Kind = hangar.KindSnapshot },
		"duplicate completed tool":         func(m *checkpoint.Manifest) { m.CompletedToolCallIDs = []string{"tool-a", "tool-a"} },
		"duplicate effect":                 func(m *checkpoint.Manifest) { m.Effects[1].ToolCallID = "tool-a" },
		"effect not in completed list":     func(m *checkpoint.Manifest) { m.CompletedToolCallIDs = []string{"tool-a"} },
		"invalid effect transition":        func(m *checkpoint.Manifest) { m.Effects[0].State = "unknown" },
	} {
		t.Run(name, func(t *testing.T) {
			m := valid
			m.CompletedToolCallIDs = append([]string(nil), valid.CompletedToolCallIDs...)
			m.Effects = append([]checkpoint.Effect(nil), valid.Effects...)
			archive := validArchive
			m.Archive = &archive
			mutate(&m)
			if err := m.Validate(); err == nil {
				t.Fatal("Validate accepted an unsafe manifest")
			}
		})
	}
}

func TestManifestCanonicalizeProducesStableToolOrder(t *testing.T) {
	t.Parallel()

	m := checkpoint.Manifest{
		CompletedToolCallIDs: []string{"tool-z", "tool-a"},
		Effects: []checkpoint.Effect{
			{ToolCallID: "tool-z", ToolName: "z", Provider: "provider", AdapterVersion: "v1", ReadOnly: true, State: checkpoint.EffectCommitted},
			{ToolCallID: "tool-a", ToolName: "a", Provider: "provider", AdapterVersion: "v1", ReadOnly: true, State: checkpoint.EffectCommitted},
		},
	}

	canonical := m.Canonicalized()
	if got, want := canonical.CompletedToolCallIDs, []string{"tool-a", "tool-z"}; !equalStrings(got, want) {
		t.Fatalf("completed tool IDs = %#v, want %#v", got, want)
	}
	if got, want := canonical.Effects[0].ToolCallID, "tool-a"; got != want {
		t.Fatalf("first effect = %q, want %q", got, want)
	}
	if m.CompletedToolCallIDs[0] != "tool-z" {
		t.Fatal("Canonicalized mutated the source manifest")
	}
}

func TestAutomaticRecoveryModeRequiresProvenEffectsAndCapabilities(t *testing.T) {
	t.Parallel()

	archive, err := hangar.NewObjectRef(
		hangar.KindCheckpoint,
		hangar.Digest("sha256:"+strings.Repeat("a", 64)),
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	base := checkpoint.Manifest{Provider: "claude", Archive: &archive, SessionID: "session", Effects: []checkpoint.Effect{
		{ToolCallID: "read", ToolName: "read_file", Provider: "claude", AdapterVersion: "v1", ReadOnly: true, State: checkpoint.EffectCommitted},
	}}
	caps := checkpoint.Capabilities{SafeBoundary: true, EffectJournal: true, SessionExport: true, NativeResume: true, ReplaySafety: true, Version: "v1"}

	if got := base.AutomaticRecoveryMode(caps); got != checkpoint.FallbackNativeResume {
		t.Fatalf("effect-free native recovery = %q, want %q", got, checkpoint.FallbackNativeResume)
	}

	workspace := base
	workspace.SessionID = ""
	if got := workspace.AutomaticRecoveryMode(caps); got != checkpoint.FallbackWorkspaceOnly {
		t.Fatalf("workspace-only recovery = %q, want %q", got, checkpoint.FallbackWorkspaceOnly)
	}

	checkpointZero := workspace
	checkpointZero.Archive = nil
	if got := checkpointZero.AutomaticRecoveryMode(caps); got != checkpoint.FallbackCheckpointZero {
		t.Fatalf("checkpoint-zero recovery = %q, want %q", got, checkpoint.FallbackCheckpointZero)
	}

	begun := base
	begun.Effects = append(begun.Effects, checkpoint.Effect{
		ToolCallID: "begun", ToolName: "write_file", Provider: "claude", AdapterVersion: "v1", State: checkpoint.EffectBegun,
	})
	if got := begun.AutomaticRecoveryMode(caps); got != checkpoint.FallbackManualReview {
		t.Fatalf("begun effect recovery = %q, want %q", got, checkpoint.FallbackManualReview)
	}

	for name, candidate := range map[string]checkpoint.Manifest{
		"registry-approved external write": func() checkpoint.Manifest {
			manifest := base
			manifest.Effects = append(append([]checkpoint.Effect(nil), base.Effects...), checkpoint.Effect{
				ToolCallID: "write", ToolName: "write_file", Provider: "claude", AdapterVersion: "v1",
				IdempotencyKey: "k", IdempotencyContract: "proven-v1", State: checkpoint.EffectCommitted,
			})
			return manifest
		}(),
		"keyless external write": func() checkpoint.Manifest {
			manifest := base
			manifest.Effects = append(append([]checkpoint.Effect(nil), base.Effects...), checkpoint.Effect{
				ToolCallID: "write", ToolName: "write_file", Provider: "claude", AdapterVersion: "v1",
				IdempotencyContract: "proven-v1", State: checkpoint.EffectCommitted,
			})
			return manifest
		}(),
		"mismatched effect provider": func() checkpoint.Manifest {
			manifest := base
			manifest.Effects = append([]checkpoint.Effect(nil), base.Effects...)
			manifest.Effects[0].Provider = "other-provider"
			return manifest
		}(),
		"mismatched adapter version": func() checkpoint.Manifest {
			manifest := base
			manifest.Effects = append([]checkpoint.Effect(nil), base.Effects...)
			manifest.Effects[0].AdapterVersion = "v2"
			return manifest
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			if got := candidate.AutomaticRecoveryMode(caps); got != checkpoint.FallbackManualReview {
				t.Fatalf("recovery = %q, want %q", got, checkpoint.FallbackManualReview)
			}
		})
	}

	withoutSafeBoundary := caps
	withoutSafeBoundary.SafeBoundary = false
	if got := workspace.AutomaticRecoveryMode(withoutSafeBoundary); got != checkpoint.FallbackManualReview {
		t.Fatalf("workspace recovery without safe-boundary capability = %q, want %q", got, checkpoint.FallbackManualReview)
	}

	withoutEffectJournal := caps
	withoutEffectJournal.EffectJournal = false
	if got := workspace.AutomaticRecoveryMode(withoutEffectJournal); got != checkpoint.FallbackManualReview {
		t.Fatalf("workspace recovery without effect journal = %q, want %q", got, checkpoint.FallbackManualReview)
	}
	if got := checkpointZero.AutomaticRecoveryMode(withoutEffectJournal); got != checkpoint.FallbackManualReview {
		t.Fatalf("checkpoint-zero recovery without effect journal = %q, want %q", got, checkpoint.FallbackManualReview)
	}
}

func TestFinalizeSucceededRequestRequiresMatchingFenceAuthority(t *testing.T) {
	t.Parallel()

	valid := checkpoint.FinalizeSucceededRequest{
		Identity:         checkpoint.Identity{BuildID: 42, PlanID: "plan", FunctionID: "function"},
		ExecutionAttempt: 2,
		Fence: checkpoint.FenceClaim{
			ExecutionAttempt: 2,
			Token:            "11111111-1111-1111-1111-111111111111",
		},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid success finalization request rejected: %v", err)
	}

	for name, mutate := range map[string]func(*checkpoint.FinalizeSucceededRequest){
		"missing identity":         func(request *checkpoint.FinalizeSucceededRequest) { request.Identity.PlanID = "" },
		"nonpositive attempt":      func(request *checkpoint.FinalizeSucceededRequest) { request.ExecutionAttempt = 0 },
		"mismatched fence attempt": func(request *checkpoint.FinalizeSucceededRequest) { request.Fence.ExecutionAttempt = 1 },
		"noncanonical fence token": func(request *checkpoint.FinalizeSucceededRequest) { request.Fence.Token = "not-a-uuid" },
	} {
		t.Run(name, func(t *testing.T) {
			request := valid
			mutate(&request)
			if err := request.Validate(); err == nil {
				t.Fatal("Validate accepted an invalid terminal-success authority")
			}
		})
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
