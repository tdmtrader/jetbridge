// Package provider defines the narrow, provider-neutral safe-boundary seam
// used by agent-runner. It deliberately contains no checkpoint persistence,
// recovery, effect journal, or transport authority.
package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Identity identifies a trusted, versioned provider adapter.
type Identity struct {
	Name    string
	Version string
}

func (identity Identity) Validate() error {
	if strings.TrimSpace(identity.Name) == "" || strings.TrimSpace(identity.Name) != identity.Name {
		return errors.New("provider adapter name is required")
	}
	if strings.TrimSpace(identity.Version) == "" || strings.TrimSpace(identity.Version) != identity.Version {
		return errors.New("provider adapter version is required")
	}
	return nil
}

// Capabilities are adapter-declared facts, not requests for server authority.
// A false SafeBoundary means its output (including generic stream JSON) is not
// evidence that a live execution can safely be checkpointed.
type Capabilities struct {
	SafeBoundary  bool
	SessionExport bool
	NativeResume  bool
	EffectJournal bool
}

// RecoveryProof is a server-owned, exact compatibility contract for native
// recovery. An adapter must not infer these facts from a package version,
// stream output, or a session ID.
type RecoveryProof struct {
	Adapter       Identity
	Executable    string
	SessionFormat string
	SafeBoundary  bool
	EffectJournal bool
	SessionExport bool
	NativeResume  bool
}

func (proof RecoveryProof) ValidateFor(identity Identity) error {
	if err := proof.Adapter.Validate(); err != nil {
		return fmt.Errorf("invalid recovery-proof adapter: %w", err)
	}
	if proof.Adapter != identity {
		return errors.New("recovery proof adapter does not match runner adapter")
	}
	if strings.TrimSpace(proof.Executable) == "" || strings.TrimSpace(proof.Executable) != proof.Executable {
		return errors.New("recovery proof executable is required")
	}
	if strings.TrimSpace(proof.SessionFormat) == "" || strings.TrimSpace(proof.SessionFormat) != proof.SessionFormat {
		return errors.New("recovery proof session format is required")
	}
	if !proof.SafeBoundary || !proof.EffectJournal || !proof.SessionExport || !proof.NativeResume {
		return errors.New("recovery proof does not establish native resume safety")
	}
	return nil
}

// Boundary is provider evidence observed only after a completed tool batch and
// before a subsequent model request. SessionID must be stable for the
// provider session; TranscriptCursor is an observed/durable byte cursor.
type Boundary struct {
	SessionID            string
	TranscriptCursor     int64
	CompletedToolCallIDs []string
}

func (boundary Boundary) Clone() Boundary {
	cloned := boundary
	cloned.CompletedToolCallIDs = append([]string(nil), boundary.CompletedToolCallIDs...)
	return cloned
}

func (boundary Boundary) Validate() error {
	if strings.TrimSpace(boundary.SessionID) == "" || strings.TrimSpace(boundary.SessionID) != boundary.SessionID {
		return errors.New("provider boundary session ID is required")
	}
	if boundary.TranscriptCursor < 0 {
		return errors.New("provider boundary transcript cursor cannot be negative")
	}
	seen := make(map[string]struct{}, len(boundary.CompletedToolCallIDs))
	for _, id := range boundary.CompletedToolCallIDs {
		if strings.TrimSpace(id) == "" || strings.TrimSpace(id) != id {
			return errors.New("provider boundary completed tool call ID is invalid")
		}
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("provider boundary repeats completed tool call ID %q", id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

// BoundaryControl blocks a safe-capable adapter at a boundary when the server
// has requested capture. It never creates a boundary itself.
type BoundaryControl interface {
	AtSafeBoundary(context.Context, Boundary) error
}

// StartRequest contains runner-owned execution inputs. SessionDir is a
// reserved server path; adapters must not substitute HOME or derive paths from
// provider output.
type StartRequest struct {
	Prompt       string
	SystemPrompt string
	WorkDir      string
	SessionDir   string
	Stdout       io.Writer
}

// ResumeRequest is only constructed by the runner from its strictly decoded,
// server-authored recovery spec. It is deliberately separate from Start.
type ResumeRequest struct {
	StartRequest
	SessionID            string
	ExecutionAttempt     int
	CheckpointGeneration int
	TranscriptCursor     int64
	CompletedToolCallIDs []string
}

func (request ResumeRequest) Clone() ResumeRequest {
	cloned := request
	cloned.CompletedToolCallIDs = append([]string(nil), request.CompletedToolCallIDs...)
	return cloned
}

// Result is the observed provider stream. SessionID is optional for adapters
// that cannot reliably report one; the runner may parse a terminal stream ID.
type Result struct {
	Stream    []byte
	SessionID string
}

func (result Result) Clone() Result {
	cloned := result
	cloned.Stream = append([]byte(nil), result.Stream...)
	return cloned
}

// RunningSession separates process start from completion so trusted adapters
// can synchronously block at an admitted boundary without using stdin frames.
type RunningSession interface {
	Wait(context.Context) (Result, error)
}

// Adapter starts a provider execution. Runner passes a nonnil control only to
// adapters that explicitly advertise SafeBoundary.
type Adapter interface {
	Identity() Identity
	Capabilities() Capabilities
	Start(context.Context, StartRequest, BoundaryControl) (RunningSession, error)
}

// RecoveryAdapter is an explicit native-resume seam. Generic and legacy
// adapters intentionally do not implement it.
type RecoveryAdapter interface {
	Adapter
	RecoveryProof() RecoveryProof
	Resume(context.Context, ResumeRequest, BoundaryControl) (RunningSession, error)
}
