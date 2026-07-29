package runtime

import (
	"fmt"
	"sort"

	"github.com/concourse/concourse/agent/checkpoint"
	"github.com/concourse/concourse/atc/db"
)

// CheckpointRestoreTopology is the exact daemon-relative topology re-derived
// from the current fresh ContainerSpec. It is never supplied by a provider or
// copied from a source attempt.
type CheckpointRestoreTopology struct {
	WorkspaceRoots []string
	SessionRoots   []string
}

func (topology CheckpointRestoreTopology) Clone() CheckpointRestoreTopology {
	cloned := topology
	cloned.WorkspaceRoots = append([]string(nil), topology.WorkspaceRoots...)
	cloned.SessionRoots = append([]string(nil), topology.SessionRoots...)
	return cloned
}

func CheckpointRestoreTopologyForSpec(spec ContainerSpec) (CheckpointRestoreTopology, error) {
	capture, err := CheckpointCaptureTopologyForSpec(spec)
	if err != nil {
		return CheckpointRestoreTopology{}, err
	}
	topology := CheckpointRestoreTopology{
		WorkspaceRoots: append([]string(nil), capture.WorkspaceRoots...),
		SessionRoots:   []string{capture.SessionRoot},
	}
	sort.Strings(topology.WorkspaceRoots)
	sort.Strings(topology.SessionRoots)
	request := checkpoint.ArchiveRequest{ContainerHandle: "checkpoint-restore", WorkspaceRoots: topology.WorkspaceRoots, SessionRoots: topology.SessionRoots, MaxBytes: 1}
	if err := request.Validate(); err != nil {
		return CheckpointRestoreTopology{}, fmt.Errorf("checkpoint restore topology: %w", err)
	}
	return topology, nil
}

func (descriptor CheckpointRestoreDescriptor) ValidateForSpec(spec ContainerSpec) error {
	if spec.Type != db.ContainerTypeAgent || !spec.Hermetic || !spec.CheckpointCapture {
		return fmt.Errorf("checkpoint restore requires a hermetic checkpoint-enabled agent container")
	}
	if !checkpoint.ValidRestoreMaterializationID(descriptor.MaterializationID) || descriptor.MaxBytes <= 0 || descriptor.MaxEntries <= 0 {
		return fmt.Errorf("checkpoint restore descriptor is invalid")
	}
	if err := descriptor.Archive.Validate(); err != nil {
		return err
	}
	_, err := CheckpointRestoreTopologyForSpec(spec)
	return err
}
