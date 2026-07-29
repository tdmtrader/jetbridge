package checkpoint

import (
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/hangar"
	"github.com/concourse/concourse/agent/snapshot"
)

func TestRestoreRequestRequiresCanonicalServerDerivedAuthority(t *testing.T) {
	request := validRestoreRequest(t)
	if err := request.Validate(); err != nil {
		t.Fatalf("valid restore request: %v", err)
	}
	for name, mutate := range map[string]func(*RestoreRequest){
		"host-like handle":  func(request *RestoreRequest) { request.ContainerHandle = "../host" },
		"unsorted roots":    func(request *RestoreRequest) { request.WorkspaceRoots = []string{"z", "a"} },
		"cross-set overlap": func(request *RestoreRequest) { request.SessionRoots = []string{"workspace/cache"} },
		"empty UID":         func(request *RestoreRequest) { request.PodUID = "" },
		"zero bounds":       func(request *RestoreRequest) { request.MaxEntries = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := request.Clone()
			mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("invalid restore request succeeded")
			}
		})
	}
}

func TestRestoreResultBindsExactReferenceAccountingAndGateIdentity(t *testing.T) {
	request := validRestoreRequest(t)
	result := RestoreResult{Object: hangar.Attributes{Ref: request.Archive.Ref, CompressedBytes: 12, UncompressedBytes: 128}, MaterializationID: request.MaterializationID, PodUID: request.PodUID}
	if err := result.ValidateFor(request); err != nil {
		t.Fatalf("valid restore result: %v", err)
	}
	for name, mutate := range map[string]func(*RestoreResult){
		"wrong pod":       func(result *RestoreResult) { result.PodUID = "other" },
		"zero compressed": func(result *RestoreResult) { result.Object.CompressedBytes = 0 },
		"oversized": func(result *RestoreResult) {
			archiveByteLimit, err := snapshot.CanonicalArchiveByteLimit(request.MaxBytes, request.MaxEntries)
			if err != nil {
				t.Fatal(err)
			}
			result.Object.UncompressedBytes = archiveByteLimit + 1
		},
		"wrong generation": func(result *RestoreResult) { result.Object.Ref.Generation++ },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := result
			mutate(&candidate)
			if err := candidate.ValidateFor(request); err == nil {
				t.Fatal("mismatched restore result succeeded")
			}
		})
	}
}

func validRestoreRequest(t *testing.T) RestoreRequest {
	t.Helper()
	ref, err := hangar.NewObjectRef(hangar.KindCheckpoint, hangar.Digest("sha256:"+strings.Repeat("a", 64)), 7)
	if err != nil {
		t.Fatal(err)
	}
	return RestoreRequest{ContainerHandle: "agent-42", MaterializationID: "materialization-2", PodUID: "pod-uid-2", Archive: Archive{Ref: ref}, WorkspaceRoots: []string{"workspace"}, SessionRoots: []string{"session"}, MaxBytes: 1024, MaxEntries: 16}
}
