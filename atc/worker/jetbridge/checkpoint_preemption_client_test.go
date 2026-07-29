package jetbridge

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestCheckpointPreemptionNoticeSourceUsesOnlyTheExactNode(t *testing.T) {
	var calls int
	source, err := NewCheckpointPreemptionNoticeSource(preemptionDaemonClient(
		[]DaemonEndpoint{{NodeName: "node-a", Address: "10.0.0.1"}, {NodeName: "node-b", Address: "10.0.0.2"}},
		func(request *http.Request) (*http.Response, error) {
			calls++
			if request.URL.Host != "10.0.0.2:7780" || request.URL.Path != "/checkpoints/v1/preemption-notice" {
				t.Fatalf("request = %s%s", request.URL.Host, request.URL.Path)
			}
			return nil, errors.New("connection reset after request write")
		},
	), "node-b")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.WaitForNodePreemption(context.Background()); err == nil {
		t.Fatal("ambiguous daemon response succeeded")
	}
	if calls != 1 {
		t.Fatalf("ambiguous daemon response made %d requests, want 1", calls)
	}
}

func TestCheckpointPreemptionNoticeSourcePollsNoContentAndStrictlyDecodesNotice(t *testing.T) {
	observed := time.Date(2026, time.July, 29, 12, 0, 0, 0, time.UTC)
	var calls int
	source, err := NewCheckpointPreemptionNoticeSource(preemptionDaemonClient(
		[]DaemonEndpoint{{NodeName: "node-a", Address: "10.0.0.1"}},
		func(request *http.Request) (*http.Response, error) {
			calls++
			if calls == 1 {
				return &http.Response{StatusCode: http.StatusNoContent, Body: io.NopCloser(strings.NewReader(""))}, nil
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"sequence":1,"observed_at":"2026-07-29T12:00:00Z"}`))}, nil
		},
	), "node-a")
	if err != nil {
		t.Fatal(err)
	}
	got, err := source.WaitForNodePreemption(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(observed) || got.Location() != time.UTC {
		t.Fatalf("notice = %v, want %v UTC", got, observed)
	}
	if calls != 2 {
		t.Fatalf("notice calls = %d, want 2", calls)
	}
}

func TestCheckpointPreemptionNoticeSourceRejectsNonStrictOrOversizedNotices(t *testing.T) {
	for name, body := range map[string]string{
		"unknown field": `{"sequence":1,"observed_at":"2026-07-29T12:00:00Z","extra":true}`,
		"oversized":     strings.Repeat("x", checkpointPreemptionResponseMax+1),
	} {
		t.Run(name, func(t *testing.T) {
			source, err := NewCheckpointPreemptionNoticeSource(preemptionDaemonClient(
				[]DaemonEndpoint{{NodeName: "node-a", Address: "10.0.0.1"}},
				func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
				},
			), "node-a")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := source.WaitForNodePreemption(context.Background()); err == nil {
				t.Fatal("invalid daemon notice succeeded")
			}
		})
	}
}

func preemptionDaemonClient(endpoints []DaemonEndpoint, respond func(*http.Request) (*http.Response, error)) *DaemonClient {
	return &DaemonClient{
		scheme:              "https",
		port:                7780,
		streamingClient:     &http.Client{Transport: preemptionTransport{respond: respond}},
		checkpointEndpoints: func(context.Context) ([]DaemonEndpoint, error) { return endpoints, nil },
	}
}

type preemptionTransport struct {
	respond func(*http.Request) (*http.Response, error)
}

func (transport preemptionTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport.respond(request)
}
