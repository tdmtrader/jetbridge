package runtime

import (
	"context"
	"reflect"
	"testing"

	"github.com/concourse/concourse/atc/db"
)

func TestTerminalCheckpointProcessIsOptionalRuntimeExtension(t *testing.T) {
	var _ TerminalCheckpointProcess = terminalCheckpointProcessTest{}
}

type terminalCheckpointProcessTest struct{}

func (terminalCheckpointProcessTest) AcquireTerminalCheckpointCapture(context.Context, int64) (CheckpointCaptureLease, error) {
	return nil, nil
}

func TestCheckpointCaptureTopologyUsesOnlyOrdinaryAgentStepRoots(t *testing.T) {
	spec := ContainerSpec{
		Type: db.ContainerTypeAgent,
		Dir:  "/tmp/build",
		Inputs: []Input{
			{DestinationPath: "/tmp/build/source"},
			{DestinationPath: "/tmp/build/result"},
		},
		Outputs: OutputPaths{
			"result":  "/tmp/build/result/",
			"z-last":  "/tmp/build/z-last",
			"a-first": "/tmp/build/a-first",
		},
		Caches:            []string{".cache"},
		ScratchPaths:      []string{"tmp"},
		SecretMounts:      []SecretMount{{SecretName: "platform", MountPath: "/run/secret"}},
		PrivateFileMounts: []PrivateFileMount{{MountPath: "/run/private"}},
	}
	topology, err := CheckpointCaptureTopologyForSpec(spec)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"dir", "input-1", "result", "a-first", "z-last"}; !reflect.DeepEqual(topology.WorkspaceRoots, want) {
		t.Fatalf("workspace roots = %#v, want %#v", topology.WorkspaceRoots, want)
	}
	if topology.SessionRoot != "session" || topology.SessionMountPath != "/tmp/build/.concourse/session" {
		t.Fatalf("session topology = %#v", topology)
	}
	request, err := topology.ArchiveRequest("agent-1", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if request.SessionRoots[0] != "session" || request.MaxBytes != 1024 {
		t.Fatalf("archive request = %#v", request)
	}
}

func TestCheckpointCaptureTopologyFailsClosedForInvalidOrCollidingRoots(t *testing.T) {
	tests := []ContainerSpec{
		{Dir: "relative"},
		{Dir: "/tmp/build", Outputs: OutputPaths{"session": "/tmp/build/output"}},
		{Dir: "/tmp/build", Outputs: OutputPaths{"one": "/tmp/build/output", "two": "/tmp/build/output/"}},
		{Dir: "/tmp/build", Inputs: []Input{{DestinationPath: "relative"}}},
	}
	for _, spec := range tests {
		if _, err := CheckpointCaptureTopologyForSpec(spec); err == nil {
			t.Fatalf("accepted unsafe topology %#v", spec)
		}
	}
}
