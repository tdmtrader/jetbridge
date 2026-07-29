package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/checkpoint"
	"github.com/concourse/concourse/agent/hangar"
	"github.com/concourse/concourse/atc/db"
)

func TestCheckpointRestoreDescriptorIsStrictServerOwnedAgentAuthority(t *testing.T) {
	ref, err := hangar.NewObjectRef(hangar.KindCheckpoint, hangar.Digest("sha256:"+strings.Repeat("a", 64)), 7)
	if err != nil {
		t.Fatal(err)
	}
	spec := ContainerSpec{
		Type:              db.ContainerTypeAgent,
		Hermetic:          true,
		CheckpointCapture: true,
		Dir:               "/tmp/build",
		CheckpointRestore: &CheckpointRestoreDescriptor{
			Archive:           checkpoint.Archive{Ref: ref},
			MaterializationID: "materialization-2",
			MaxBytes:          1024,
			MaxEntries:        16,
		},
	}
	if err := spec.CheckpointRestore.ValidateForSpec(spec); err != nil {
		t.Fatalf("valid restore descriptor: %v", err)
	}
	for name, mutate := range map[string]func(*ContainerSpec){
		"not an agent":     func(spec *ContainerSpec) { spec.Type = db.ContainerTypeTask },
		"not hermetic":     func(spec *ContainerSpec) { spec.Hermetic = false },
		"capture disabled": func(spec *ContainerSpec) { spec.CheckpointCapture = false },
		"zero bytes":       func(spec *ContainerSpec) { spec.CheckpointRestore.MaxBytes = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := spec
			candidate.CheckpointRestore = &CheckpointRestoreDescriptor{}
			*candidate.CheckpointRestore = *spec.CheckpointRestore
			mutate(&candidate)
			if err := candidate.CheckpointRestore.ValidateForSpec(candidate); err == nil {
				t.Fatal("invalid restore descriptor succeeded")
			}
		})
	}
	if _, err := CheckpointRestoreTopologyForSpec(spec); err != nil {
		t.Fatalf("server-derived restore topology: %v", err)
	}
}

func TestPreLaunchMaterializerIsOptionalRuntimeExtension(t *testing.T) {
	var _ PreLaunchMaterializer = preLaunchMaterializerTest{}
}

type preLaunchMaterializerTest struct{}

func (preLaunchMaterializerTest) MaterializeBeforeLaunch(context.Context, ProcessSpec) error {
	return nil
}
