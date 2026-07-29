package jetbridge

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/checkpoint"
	"github.com/concourse/concourse/agent/hangar"
)

func TestCheckpointRestoreClientUsesOnlyExactNodeAndValidatesResponse(t *testing.T) {
	digest := hangar.Digest("sha256:" + strings.Repeat("a", 64))
	ref, err := hangar.NewObjectRef(hangar.KindCheckpoint, digest, 9)
	if err != nil {
		t.Fatal(err)
	}
	request := checkpoint.RestoreRequest{
		ContainerHandle: "agent-42", MaterializationID: "materialization-2", PodUID: "pod-uid-2",
		Archive: checkpoint.Archive{Ref: ref}, WorkspaceRoots: []string{"workspace"}, SessionRoots: []string{"session"}, MaxBytes: 1024, MaxEntries: 16,
	}
	result := checkpoint.RestoreResult{Object: hangar.Attributes{Ref: ref, CompressedBytes: 100, UncompressedBytes: 512}, MaterializationID: request.MaterializationID, PodUID: request.PodUID}
	client := checkpointCaptureDaemonClient([]DaemonEndpoint{{NodeName: "node-a", Address: "10.0.0.1"}, {NodeName: "node-b", Address: "10.0.0.2"}}, func(httpRequest *http.Request) *http.Response {
		if httpRequest.URL.Host != "10.0.0.2:7780" || httpRequest.URL.Path != "/checkpoints/v1/restore" {
			t.Fatalf("request = %s %s", httpRequest.URL.Host, httpRequest.URL.Path)
		}
		return checkpointJSONResponse(http.StatusOK, result)
	})
	if got, err := client.RestoreCheckpoint(context.Background(), "node-b", request); err != nil || got != result {
		t.Fatalf("RestoreCheckpoint() = %#v, %v", got, err)
	}

	result.PodUID = "wrong"
	client = checkpointCaptureDaemonClient([]DaemonEndpoint{{NodeName: "node-b", Address: "10.0.0.2"}}, func(*http.Request) *http.Response { return checkpointJSONResponse(http.StatusOK, result) })
	if _, err := client.RestoreCheckpoint(context.Background(), "node-b", request); err == nil {
		t.Fatal("mismatched restore response succeeded")
	}
}
