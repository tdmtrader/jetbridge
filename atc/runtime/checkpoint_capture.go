package runtime

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/concourse/concourse/agent/checkpoint"
)

const checkpointSessionRoot = "session"

// CheckpointProcess is an optional extension implemented only by runtimes
// which can stop an exact, already-running process. It deliberately does not
// widen Process or Container: ordinary runtimes have no checkpoint authority.
//
// maxBytes is a server-owned logical archive cap. A caller must provide a
// deadline; implementations release the lease if that deadline expires.
type CheckpointProcess interface {
	AcquireCheckpointCapture(context.Context, int64) (CheckpointCaptureLease, error)
}

// TerminalCheckpointProcess is an optional extension for a process with
// durable, runtime-verified completion evidence. Unlike CheckpointProcess it
// never asks a live provider for a safe boundary; implementations may only
// quiesce residual pause/sidecar processes after proving the agent child has
// exited. Callers must provide a deadline.
type TerminalCheckpointProcess interface {
	AcquireTerminalCheckpointCapture(context.Context, int64) (CheckpointCaptureLease, error)
}

// CheckpointCaptureLease is opaque process/workspace quiescence. Release is
// idempotent and resumes the process when possible.
type CheckpointCaptureLease interface {
	CaptureTarget() CheckpointCaptureTarget
	Release(context.Context) error
}

// SafeBoundaryProcess is an optional extension for an agent provider that can
// cooperatively pause its exact live child at a provider-declared boundary.
// It is deliberately separate from CheckpointProcess: acquiring this lease
// neither captures data nor coordinates an ATC checkpoint.
//
// Callers must provide a deadline. Release is idempotent and resumes the
// exact process identity held by the lease.
type SafeBoundaryProcess interface {
	AcquireSafeBoundary(context.Context) (SafeBoundaryLease, error)
}

// SafeBoundaryLease is opaque, runtime-owned cooperative provider quiescence.
// Implementations must fail closed when the process identity changes.
type SafeBoundaryLease interface {
	Release(context.Context) error
}

// CheckpointCaptureTarget is immutable runtime evidence identifying the only
// pod/node and daemon-relative roots which may be captured for a lease.
type CheckpointCaptureTarget struct {
	ContainerHandle string
	PodName         string
	PodUID          string
	NodeName        string
	Archive         checkpoint.ArchiveRequest
}

func (target CheckpointCaptureTarget) Clone() CheckpointCaptureTarget {
	cloned := target
	cloned.Archive.WorkspaceRoots = append([]string(nil), target.Archive.WorkspaceRoots...)
	cloned.Archive.SessionRoots = append([]string(nil), target.Archive.SessionRoots...)
	return cloned
}

func (target CheckpointCaptureTarget) Validate() error {
	for name, value := range map[string]string{
		"container handle": target.ContainerHandle,
		"pod name":         target.PodName,
		"pod UID":          target.PodUID,
		"node name":        target.NodeName,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return fmt.Errorf("checkpoint %s is required", name)
		}
	}
	if target.Archive.ContainerHandle != target.ContainerHandle {
		return fmt.Errorf("checkpoint archive handle does not match target")
	}
	return target.Archive.Validate()
}

// CheckpointCaptureTopology is the server-derived mapping between ordinary
// agent volumes and daemon-relative archive roots. Caches, scratch, secrets,
// private mounts, init volumes, and sidecar-only authority are intentionally
// absent.
type CheckpointCaptureTopology struct {
	WorkspaceRoots   []string
	SessionRoot      string
	SessionMountPath string
}

func (topology CheckpointCaptureTopology) Clone() CheckpointCaptureTopology {
	cloned := topology
	cloned.WorkspaceRoots = append([]string(nil), topology.WorkspaceRoots...)
	return cloned
}

func (topology CheckpointCaptureTopology) ArchiveRequest(handle string, maxBytes int64) (checkpoint.ArchiveRequest, error) {
	if err := topology.Validate(); err != nil {
		return checkpoint.ArchiveRequest{}, err
	}
	request := checkpoint.ArchiveRequest{
		ContainerHandle: handle,
		WorkspaceRoots:  append([]string(nil), topology.WorkspaceRoots...),
		SessionRoots:    []string{topology.SessionRoot},
		MaxBytes:        maxBytes,
	}
	if err := request.Validate(); err != nil {
		return checkpoint.ArchiveRequest{}, err
	}
	return request, nil
}

func (topology CheckpointCaptureTopology) Validate() error {
	if len(topology.WorkspaceRoots) == 0 || topology.WorkspaceRoots[0] != "dir" {
		return fmt.Errorf("checkpoint workspace roots must begin with dir")
	}
	if topology.SessionRoot != checkpointSessionRoot || !checkpointRoot(topology.SessionRoot) {
		return fmt.Errorf("checkpoint session root must be %q", checkpointSessionRoot)
	}
	if topology.SessionMountPath == "" || !filepath.IsAbs(topology.SessionMountPath) || filepath.Clean(topology.SessionMountPath) != topology.SessionMountPath {
		return fmt.Errorf("checkpoint session mount path must be canonical and absolute")
	}
	for index, root := range topology.WorkspaceRoots {
		if !checkpointRoot(root) {
			return fmt.Errorf("checkpoint workspace root %q is invalid", root)
		}
		if checkpointRootOverlap(root, topology.SessionRoot) {
			return fmt.Errorf("checkpoint workspace root %q collides with session root", root)
		}
		for previous := 0; previous < index; previous++ {
			if checkpointRootOverlap(root, topology.WorkspaceRoots[previous]) {
				return fmt.Errorf("checkpoint workspace roots %q and %q overlap", topology.WorkspaceRoots[previous], root)
			}
		}
	}
	return nil
}

// CheckpointCaptureTopologyForSpec derives the current-v3 agent volume
// topology. The ordering mirrors Jetbridge's ordinary step volumes exactly:
// dir, inputs in declaration order (with output-overlap roots), then sorted
// non-overlapping outputs.
func CheckpointCaptureTopologyForSpec(spec ContainerSpec) (CheckpointCaptureTopology, error) {
	if spec.Dir == "" || !filepath.IsAbs(spec.Dir) || filepath.Clean(spec.Dir) != spec.Dir {
		return CheckpointCaptureTopology{}, fmt.Errorf("checkpoint workspace directory must be canonical and absolute")
	}
	outputNames := make([]string, 0, len(spec.Outputs))
	outputByPath := make(map[string]string, len(spec.Outputs))
	for name, outputPath := range spec.Outputs {
		if !checkpointRoot(name) {
			return CheckpointCaptureTopology{}, fmt.Errorf("checkpoint output root %q is invalid", name)
		}
		clean := filepath.Clean(outputPath)
		if outputPath == "" || !filepath.IsAbs(clean) {
			return CheckpointCaptureTopology{}, fmt.Errorf("checkpoint output %q path must be absolute", name)
		}
		if previous, duplicate := outputByPath[clean]; duplicate {
			return CheckpointCaptureTopology{}, fmt.Errorf("checkpoint outputs %q and %q share path %q", previous, name, clean)
		}
		outputByPath[clean] = name
		outputNames = append(outputNames, name)
	}
	sort.Strings(outputNames)

	topology := CheckpointCaptureTopology{
		WorkspaceRoots:   []string{"dir"},
		SessionRoot:      checkpointSessionRoot,
		SessionMountPath: filepath.Join(spec.Dir, ".concourse", checkpointSessionRoot),
	}
	inputPaths := make(map[string]struct{}, len(spec.Inputs))
	for index, input := range spec.Inputs {
		clean := filepath.Clean(input.DestinationPath)
		if input.DestinationPath == "" || !filepath.IsAbs(clean) {
			return CheckpointCaptureTopology{}, fmt.Errorf("checkpoint input %d path must be absolute", index+1)
		}
		root := fmt.Sprintf("input-%d", index+1)
		if output, overlaps := outputByPath[clean]; overlaps {
			root = output
		}
		topology.WorkspaceRoots = append(topology.WorkspaceRoots, root)
		inputPaths[clean] = struct{}{}
	}
	for _, name := range outputNames {
		if _, overlaps := inputPaths[filepath.Clean(spec.Outputs[name])]; !overlaps {
			topology.WorkspaceRoots = append(topology.WorkspaceRoots, name)
		}
	}
	if err := topology.Validate(); err != nil {
		return CheckpointCaptureTopology{}, err
	}
	return topology, nil
}

func checkpointRoot(root string) bool {
	if root == "" || path.IsAbs(root) || path.Clean(root) != root || root == "." || root == ".." || strings.ContainsAny(root, "\\\x00") {
		return false
	}
	for _, segment := range strings.Split(root, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func checkpointRootOverlap(left, right string) bool {
	return left == right || strings.HasPrefix(left, right+"/") || strings.HasPrefix(right, left+"/")
}
