package jetbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/checkpoint"
	"github.com/concourse/concourse/agent/hangar"
)

func TestCheckpointCaptureClientUsesOnlyTheScheduledNode(t *testing.T) {
	prepared := checkpoint.PreparedArchive{Handle: "prepared-" + strings.Repeat("a", 48), Digest: hangar.Digest("sha256:" + strings.Repeat("a", 64)), Files: 2, Bytes: 1024}
	client := checkpointCaptureDaemonClient([]DaemonEndpoint{{NodeName: "node-a", Address: "10.0.0.1"}, {NodeName: "node-b", Address: "10.0.0.2"}}, func(request *http.Request) *http.Response {
		if request.URL.Host != "10.0.0.2:7780" || request.URL.Path != "/checkpoints/v1/prepare" {
			t.Fatalf("request = %s %s", request.URL.Host, request.URL.Path)
		}
		return checkpointJSONResponse(http.StatusCreated, prepared)
	})
	got, err := client.PrepareCheckpoint(context.Background(), "node-b", checkpoint.ArchiveRequest{ContainerHandle: "agent-42", WorkspaceRoots: []string{"dir"}, SessionRoots: []string{"session"}, MaxBytes: 1024})
	if err != nil || got != prepared {
		t.Fatalf("PrepareCheckpoint() = %#v, %v", got, err)
	}
}

func TestCheckpointCaptureClientFailsClosedForEndpointAndResponseProblems(t *testing.T) {
	request := checkpoint.ArchiveRequest{ContainerHandle: "agent-42", WorkspaceRoots: []string{"dir"}, SessionRoots: []string{"session"}, MaxBytes: 1024}
	for name, endpoints := range map[string][]DaemonEndpoint{"blank node": {{NodeName: "node-a", Address: "10.0.0.1"}}, "missing node": {{NodeName: "node-a", Address: "10.0.0.1"}}, "duplicate node": {{NodeName: "node-a", Address: "10.0.0.1"}, {NodeName: "node-a", Address: "10.0.0.2"}}} {
		t.Run(name, func(t *testing.T) {
			client := checkpointCaptureDaemonClient(endpoints, func(*http.Request) *http.Response { t.Fatal("unexpected HTTP request"); return nil })
			node := "node-a"
			if name == "blank node" {
				node = ""
			}
			if name == "missing node" {
				node = "node-b"
			}
			if _, err := client.PrepareCheckpoint(context.Background(), node, request); err == nil {
				t.Fatal("expected endpoint selection failure")
			}
		})
	}
	client := checkpointCaptureDaemonClient([]DaemonEndpoint{{NodeName: "node-a", Address: "10.0.0.1"}}, func(*http.Request) *http.Response {
		return &http.Response{StatusCode: http.StatusCreated, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"handle":"prepared-x"}`))}
	})
	if _, err := client.PrepareCheckpoint(context.Background(), "node-a", request); err == nil {
		t.Fatal("malformed response succeeded")
	}
}

func TestCheckpointCaptureClientUploadValidatesExactObjectAndAccounting(t *testing.T) {
	prepared := checkpoint.PreparedArchive{Handle: "prepared-" + strings.Repeat("b", 48), Digest: hangar.Digest("sha256:" + strings.Repeat("b", 64)), Files: 2, Bytes: 1024}
	key, err := hangar.Key(hangar.KindCheckpoint, prepared.Digest)
	if err != nil {
		t.Fatal(err)
	}
	ticket := checkpoint.ObjectUploadTicket{ObjectID: 1, StagedCheckpointID: 2, Kind: hangar.KindCheckpoint, Digest: prepared.Digest, Key: key, UploadToken: "token"}
	result := checkpoint.ArchiveResult{Object: hangar.Attributes{Ref: hangar.ObjectRef{Kind: hangar.KindCheckpoint, Digest: prepared.Digest, Key: key, Generation: 9}, UncompressedBytes: prepared.Bytes, CompressedBytes: 100}, Files: prepared.Files, Bytes: prepared.Bytes}
	client := checkpointCaptureDaemonClient([]DaemonEndpoint{{NodeName: "node-a", Address: "10.0.0.1"}}, func(request *http.Request) *http.Response {
		if request.URL.Path != "/checkpoints/v1/upload/"+prepared.Handle {
			t.Fatalf("path = %s", request.URL.Path)
		}
		return checkpointJSONResponse(http.StatusOK, result)
	})
	got, err := client.UploadCheckpoint(context.Background(), "node-a", prepared, ticket)
	if err != nil || got.Object.Ref != result.Object.Ref {
		t.Fatalf("UploadCheckpoint() = %#v, %v", got, err)
	}

	result.Object.UncompressedBytes--
	client = checkpointCaptureDaemonClient([]DaemonEndpoint{{NodeName: "node-a", Address: "10.0.0.1"}}, func(*http.Request) *http.Response { return checkpointJSONResponse(http.StatusOK, result) })
	if _, err := client.UploadCheckpoint(context.Background(), "node-a", prepared, ticket); err == nil {
		t.Fatal("mismatched accounting succeeded")
	}

	available := checkpoint.ObjectUploadTicket{ObjectID: 3, StagedCheckpointID: 4, Kind: hangar.KindCheckpoint, Digest: prepared.Digest, Key: key, AlreadyAvailable: true, AvailableGeneration: 11}
	availableResult := checkpoint.ArchiveResult{Object: hangar.Attributes{Ref: hangar.ObjectRef{Kind: hangar.KindCheckpoint, Digest: prepared.Digest, Key: key, Generation: 11}}, Files: prepared.Files, Bytes: prepared.Bytes}
	client = checkpointCaptureDaemonClient([]DaemonEndpoint{{NodeName: "node-a", Address: "10.0.0.1"}}, func(*http.Request) *http.Response { return checkpointJSONResponse(http.StatusOK, availableResult) })
	if got, err := client.UploadCheckpoint(context.Background(), "node-a", prepared, available); err != nil || got != availableResult {
		t.Fatalf("AlreadyAvailable upload = %#v, %v", got, err)
	}
}

func TestCheckpointCaptureClientRejectsHTTPAndTrailingResponsesAndClosesBodies(t *testing.T) {
	request := checkpoint.ArchiveRequest{ContainerHandle: "agent-42", WorkspaceRoots: []string{"dir"}, SessionRoots: []string{"session"}, MaxBytes: 1024}
	endpoint := []DaemonEndpoint{{NodeName: "node-a", Address: "10.0.0.1"}}
	for name, response := range map[string]*http.Response{
		"HTTP failure":  {StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader("no"))},
		"trailing JSON": {StatusCode: http.StatusCreated, Body: io.NopCloser(strings.NewReader(`{"handle":"prepared-x"}{}`))},
		"oversized":     {StatusCode: http.StatusCreated, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", checkpointCaptureResponseLimit+1)))},
	} {
		t.Run(name, func(t *testing.T) {
			client := checkpointCaptureDaemonClient(endpoint, func(*http.Request) *http.Response { return response })
			if _, err := client.PrepareCheckpoint(context.Background(), "node-a", request); err == nil {
				t.Fatal("invalid response succeeded")
			}
		})
	}
	prepared := checkpoint.PreparedArchive{Handle: "prepared-" + strings.Repeat("c", 48), Digest: hangar.Digest("sha256:" + strings.Repeat("c", 64)), Files: 1, Bytes: 1}
	body := &checkpointCloseProbe{ReadCloser: io.NopCloser(bytes.NewReader(mustCheckpointJSON(prepared)))}
	client := checkpointCaptureDaemonClient(endpoint, func(*http.Request) *http.Response { return &http.Response{StatusCode: http.StatusCreated, Body: body} })
	if _, err := client.PrepareCheckpoint(context.Background(), "node-a", request); err != nil {
		t.Fatal(err)
	}
	if !body.closed {
		t.Fatal("response body was not closed")
	}
}

func TestCheckpointCaptureClientDoesNotRequestAfterContextCancellation(t *testing.T) {
	request := checkpoint.ArchiveRequest{ContainerHandle: "agent-42", WorkspaceRoots: []string{"dir"}, SessionRoots: []string{"session"}, MaxBytes: 1024}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := checkpointCaptureDaemonClient([]DaemonEndpoint{{NodeName: "node-a", Address: "10.0.0.1"}}, func(*http.Request) *http.Response { t.Fatal("cancelled request reached transport"); return nil })
	if _, err := client.PrepareCheckpoint(ctx, "node-a", request); err == nil {
		t.Fatal("cancelled request succeeded")
	}
}

func checkpointCaptureDaemonClient(endpoints []DaemonEndpoint, respond func(*http.Request) *http.Response) *DaemonClient {
	return &DaemonClient{scheme: "https", port: 7780, streamingClient: &http.Client{Transport: checkpointCaptureTransport{respond: respond}}, checkpointEndpoints: func(context.Context) ([]DaemonEndpoint, error) { return endpoints, nil }}
}

type checkpointCaptureTransport struct {
	respond func(*http.Request) *http.Response
}

func (transport checkpointCaptureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport.respond(request), nil
}
func checkpointJSONResponse(status int, value any) *http.Response {
	body, _ := json.Marshal(value)
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewReader(body))}
}

type checkpointCloseProbe struct {
	io.ReadCloser
	closed bool
}

func (probe *checkpointCloseProbe) Close() error {
	probe.closed = true
	return probe.ReadCloser.Close()
}
func mustCheckpointJSON(value any) []byte { body, _ := json.Marshal(value); return body }
