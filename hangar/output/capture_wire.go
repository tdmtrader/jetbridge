package output

// The node daemon's capture routes: hold, seal, publish, release, stat.
//
// The daemon answers; the control plane writes the capture row. Nothing here
// carries a path, a bucket, a scope or an object key: every request names an
// execution and an output, and the daemon derives the step directory
// (CaptureKey.Directory) and the object key (its own namespace and the digest
// it computed) itself. A request also names the execution's exact identity,
// because the capability that authorizes it is bound to that identity.

import (
	"context"
	"fmt"
	"slices"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
)

// CaptureFacet is the capture routes' authorization surface. A base control
// capability presented at a capture route is refused, and a capture capability
// presented at a base route is refused there.
const CaptureFacet executioncontrol.Facet = "durable-output-capture"

// SourceControl is the control plane's seam to one node's capture routes.
// Every method takes a request naming an execution and an output -- never a
// path, bucket, scope or key -- and the node derives each location itself.
type SourceControl interface {
	Seal(ctx context.Context, request CaptureSealRequest) (CaptureSealResult, error)
	Publish(ctx context.Context, request CapturePublishRequest) (CapturePublishResult, error)
	Release(ctx context.Context, request CaptureReleaseRequest) (CaptureReleaseAcknowledgement, error)
	Stat(ctx context.Context, request CaptureStatRequest) (CapturePublishResult, error)
}

// StepMarkerState is the closed vocabulary of the node-local marker.
type StepMarkerState string

const (
	// StepHeld: written before the step's first container may start. The
	// directory survives every destructive path until it is released.
	StepHeld StepMarkerState = "held"
	// StepSealed: the producer has exited and its tree is being, or has been,
	// read. Nobody may write to it again.
	StepSealed StepMarkerState = "sealed"
)

// StepMarker is the one marker file per step directory: the only node-local
// capture state there is. It is written by fsync and rename, and an
// unreadable one makes the node refuse every destructive path.
type StepMarker struct {
	State       StepMarkerState              `json:"state"`
	ExecutionID executioncontrol.ExecutionID `json:"execution"`
	Output      OutputName                   `json:"output"`
	Node        executioncontrol.NodeUID     `json:"node"`
	PodUID      executioncontrol.PodUID      `json:"pod_uid"`
}

func (marker StepMarker) Key() CaptureKey {
	return CaptureKey{ExecutionID: marker.ExecutionID, Output: marker.Output}
}

func (marker StepMarker) Validate() error {
	if !slices.Contains([]StepMarkerState{StepHeld, StepSealed}, marker.State) {
		return fmt.Errorf("%w: step marker state %q", ErrUnknownMember, marker.State)
	}
	if err := marker.Key().Validate(); err != nil {
		return err
	}
	if marker.Node == "" || marker.PodUID == "" {
		return fmt.Errorf("%w: a step marker names no node or no Pod", ErrIncomplete)
	}

	return nil
}

// CaptureHoldRequest is the capture control init's body. It runs in the
// producing Pod, before any other container, and is the first message that
// can name that Pod.
type CaptureHoldRequest struct {
	ProtocolVersion string                    `json:"protocol_version"`
	Execution       executioncontrol.Identity `json:"execution"`
	Output          OutputName                `json:"output"`
	PodUID          executioncontrol.PodUID   `json:"pod_uid"`
}

func (request CaptureHoldRequest) Validate() error {
	if err := validateProtocol(request.ProtocolVersion); err != nil {
		return err
	}
	if err := request.Execution.Validate(); err != nil {
		return err
	}
	if request.PodUID == "" {
		return fmt.Errorf("%w: a hold names no Pod", ErrIncomplete)
	}

	return request.Output.Validate()
}

// HoldAcknowledged is the hold's answer kind; the control init waits for it.
const HoldAcknowledged = "hold_acknowledged"

// CaptureHoldAcknowledgement says the held marker is durable.
type CaptureHoldAcknowledgement struct {
	ProtocolVersion string     `json:"protocol_version"`
	Kind            string     `json:"kind"`
	Marker          StepMarker `json:"marker"`
}

func (ack CaptureHoldAcknowledgement) Validate() error {
	if err := validateProtocol(ack.ProtocolVersion); err != nil {
		return err
	}
	if ack.Kind != HoldAcknowledged {
		return fmt.Errorf("%w: a hold answered %q", ErrUnknownMember, ack.Kind)
	}

	return ack.Marker.Validate()
}

// CaptureSealRequest is step 2. PodUID is the Pod the node's own finish
// statement named; the seal refuses any marker that names another.
type CaptureSealRequest struct {
	ProtocolVersion string                    `json:"protocol_version"`
	Execution       executioncontrol.Identity `json:"execution"`
	Output          OutputName                `json:"output"`
	PodUID          executioncontrol.PodUID   `json:"pod_uid"`
}

func (request CaptureSealRequest) Validate() error {
	if err := validateProtocol(request.ProtocolVersion); err != nil {
		return err
	}
	if err := request.Execution.Validate(); err != nil {
		return err
	}
	if request.PodUID == "" {
		return fmt.Errorf("%w: a seal names no Pod", ErrIncomplete)
	}

	return request.Output.Validate()
}

// CaptureSealResult is what the sealed tree IS. Scope and digest are the
// daemon's: the scope from its own namespace, the digest from the bytes.
// Staged names the canonical archive the seal left in scratch for the
// publish; it is a hint, and a publish that finds it gone canonicalizes again.
type CaptureSealResult struct {
	ProtocolVersion string        `json:"protocol_version"`
	Marker          StepMarker    `json:"marker"`
	Scope           hangar.Scope  `json:"scope"`
	Digest          hangar.Digest `json:"digest"`
	LogicalBytes    int64         `json:"logical_bytes"`
	Staged          string        `json:"staged,omitempty"`
}

func (result CaptureSealResult) Validate() error {
	if err := validateProtocol(result.ProtocolVersion); err != nil {
		return err
	}
	if err := result.Marker.Validate(); err != nil {
		return err
	}
	if result.Marker.State != StepSealed {
		return fmt.Errorf("%w: a seal answered with a %s marker", ErrUnresolved, result.Marker.State)
	}
	if err := result.Scope.Validate(); err != nil {
		return err
	}
	if err := result.Digest.Validate(); err != nil {
		return err
	}
	if result.LogicalBytes <= 0 {
		return fmt.Errorf("%w: the sealed tree measured %d logical bytes", ErrIncomplete, result.LogicalBytes)
	}

	return nil
}

// CapturePublishRequest is step 4: create the object for the digest the
// control plane already wrote into its row. The digest is a CHECK, not a key:
// the daemon publishes the sealed tree it holds and refuses if that tree is
// not this digest.
type CapturePublishRequest struct {
	ProtocolVersion string                    `json:"protocol_version"`
	Execution       executioncontrol.Identity `json:"execution"`
	Output          OutputName                `json:"output"`
	Digest          hangar.Digest             `json:"digest"`
	Staged          string                    `json:"staged,omitempty"`
	Namespace       CallerNamespaceRequest    `json:"namespace"`
}

func (request CapturePublishRequest) Validate() error {
	if err := validateProtocol(request.ProtocolVersion); err != nil {
		return err
	}
	if err := request.Execution.Validate(); err != nil {
		return err
	}
	if err := request.Output.Validate(); err != nil {
		return err
	}
	if err := request.Digest.Validate(); err != nil {
		return err
	}

	return request.Namespace.Validate()
}

// CapturePublishResult is the generation the store holds for the digest.
// Deduplicated says the object existed already, marked by this plane with
// this digest, and the capture joined it rather than creating a second.
type CapturePublishResult struct {
	ProtocolVersion string         `json:"protocol_version"`
	Ref             hangar.TreeRef `json:"ref"`
	Metageneration  int64          `json:"metageneration"`
	MarkerVersion   string         `json:"marker_version"`
	Deduplicated    bool           `json:"deduplicated"`
}

func (result CapturePublishResult) Validate() error {
	if err := validateProtocol(result.ProtocolVersion); err != nil {
		return err
	}
	if err := result.Ref.Validate(); err != nil {
		return err
	}
	if result.MarkerVersion != MarkerVersion {
		return fmt.Errorf("%w: the published object is marked %q and this plane writes %q",
			ErrUnknownMember, result.MarkerVersion, MarkerVersion)
	}
	if result.Metageneration <= 0 {
		return fmt.Errorf("%w: the published object has no metageneration", ErrIncomplete)
	}

	return nil
}

// CaptureReleaseRequest is step 6: clear the marker. Idempotent.
type CaptureReleaseRequest struct {
	ProtocolVersion string                    `json:"protocol_version"`
	Execution       executioncontrol.Identity `json:"execution"`
	Output          OutputName                `json:"output"`
}

func (request CaptureReleaseRequest) Validate() error {
	if err := validateProtocol(request.ProtocolVersion); err != nil {
		return err
	}
	if err := request.Execution.Validate(); err != nil {
		return err
	}

	return request.Output.Validate()
}

// ReleaseAcknowledged is the release's answer kind.
const ReleaseAcknowledged = "release_acknowledged"

// CaptureReleaseAcknowledgement says no marker remains for the capture.
type CaptureReleaseAcknowledgement struct {
	ProtocolVersion string     `json:"protocol_version"`
	Kind            string     `json:"kind"`
	Key             CaptureKey `json:"capture"`
}

func (ack CaptureReleaseAcknowledgement) Validate() error {
	if err := validateProtocol(ack.ProtocolVersion); err != nil {
		return err
	}
	if ack.Kind != ReleaseAcknowledged {
		return fmt.Errorf("%w: a release answered %q", ErrUnknownMember, ack.Kind)
	}

	return ack.Key.Validate()
}

// CaptureStatRequest is recovery's question for a publishing row: what does
// the store hold, right now, for this digest? The scope is the daemon's.
type CaptureStatRequest struct {
	ProtocolVersion string                    `json:"protocol_version"`
	Execution       executioncontrol.Identity `json:"execution"`
	Digest          hangar.Digest             `json:"digest"`
}

func (request CaptureStatRequest) Validate() error {
	if err := validateProtocol(request.ProtocolVersion); err != nil {
		return err
	}
	if err := request.Execution.Validate(); err != nil {
		return err
	}

	return request.Digest.Validate()
}
