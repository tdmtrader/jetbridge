// Connectivity test for the Grafana LGTM observability stack on theborg.
//
// Verifies end-to-end data flow:
//   - Traces: sends a span via OTLP HTTP → queries Tempo HTTP API
//   - Logs: pushes a log entry via Loki HTTP API → queries it back
//
// The metrics test is omitted because OTLP metrics require Prometheus to be
// configured as an OTLP receiver (remote-write), which is not part of the
// standard kube-prometheus-stack. Metrics are validated by querying
// Prometheus for Concourse's own metrics once the server-side tracing is
// enabled (Phase 3).
//
// Setup: port-forward Tempo's OTLP HTTP and query ports:
//
//	kubectl --context theborg -n monitoring port-forward svc/tempo 4318:4318 3200:3200 &
//	kubectl --context theborg -n monitoring port-forward svc/loki 3100:3100 &
//
// Or, if DNS is configured (tempo-otlp.home, tempo.home, loki.home):
//
//	OTLP_HTTP_ENDPOINT=http://tempo-otlp.home \
//	TEMPO_QUERY_URL=http://tempo.home \
//	LOKI_URL=http://loki.home \
//	go test ./testhelpers/otel/ -run TestConnectivity -v -count=1
//
// Default endpoints use theborg DNS names (tempo-otlp.home, tempo.home, loki.home).
package otel_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.4.0"
	"go.opentelemetry.io/otel/trace"
)

func envOr(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func TestConnectivityTraces(t *testing.T) {
	// OTLP HTTP endpoint (Tempo's receiver on port 4318).
	otlpEndpoint := envOr("OTLP_HTTP_ENDPOINT", "http://tempo-otlp.home")
	tempoURL := envOr("TEMPO_QUERY_URL", "http://tempo.home")

	ctx := context.Background()

	exporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpoint(stripScheme(otlpEndpoint)),
		otlptracehttp.WithInsecure(),
	)
	if err != nil {
		t.Fatalf("failed to create OTLP HTTP trace exporter: %v", err)
	}

	res := resource.NewSchemaless(
		semconv.ServiceNameKey.String("concourse-connectivity-test"),
	)

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(exporter),
		sdktrace.WithResource(res),
	)
	defer tp.Shutdown(ctx)

	tracer := tp.Tracer("connectivity-test")
	marker := fmt.Sprintf("connectivity-%d", time.Now().UnixNano())

	_, span := tracer.Start(ctx, "connectivity-test-trace",
		trace.WithAttributes(attribute.String("test.marker", marker)),
	)
	traceID := span.SpanContext().TraceID().String()
	span.End()

	if err := tp.ForceFlush(ctx); err != nil {
		t.Fatalf("failed to flush traces: %v", err)
	}

	t.Logf("Sent trace with traceID=%s marker=%s", traceID, marker)

	var found bool
	for i := 0; i < 30; i++ {
		time.Sleep(time.Second)

		url := fmt.Sprintf("%s/api/traces/%s", tempoURL, traceID)
		resp, err := http.Get(url)
		if err != nil {
			t.Logf("attempt %d: Tempo query failed: %v", i+1, err)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == 200 && len(body) > 0 {
			t.Logf("Trace found in Tempo after %d seconds", i+1)
			found = true
			break
		}
		t.Logf("attempt %d: status=%d len=%d", i+1, resp.StatusCode, len(body))
	}

	if !found {
		t.Fatal("trace not found in Tempo after 30 seconds")
	}
}

func TestConnectivityLogs(t *testing.T) {
	lokiURL := envOr("LOKI_URL", "http://loki.home")

	marker := fmt.Sprintf("connectivity-log-%d", time.Now().UnixNano())

	now := time.Now()
	payload := map[string]interface{}{
		"streams": []map[string]interface{}{
			{
				"stream": map[string]string{
					"job":    "concourse-connectivity-test",
					"source": "otel-test",
				},
				"values": [][]string{
					{fmt.Sprintf("%d", now.UnixNano()), marker},
				},
			},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("failed to marshal log payload: %v", err)
	}

	pushURL := fmt.Sprintf("%s/loki/api/v1/push", lokiURL)
	resp, err := http.Post(pushURL, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("failed to push log to Loki: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != 204 && resp.StatusCode != 200 {
		t.Fatalf("Loki push returned status %d", resp.StatusCode)
	}

	t.Logf("Pushed log entry with marker=%s", marker)

	var found bool
	for i := 0; i < 30; i++ {
		time.Sleep(time.Second)

		queryURL := fmt.Sprintf(
			"%s/loki/api/v1/query_range?query={job=\"concourse-connectivity-test\"}&start=%d&end=%d&limit=10",
			lokiURL,
			now.Add(-time.Minute).UnixNano(),
			now.Add(time.Minute).UnixNano(),
		)
		resp, err := http.Get(queryURL)
		if err != nil {
			t.Logf("attempt %d: Loki query failed: %v", i+1, err)
			continue
		}
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == 200 && bytes.Contains(respBody, []byte(marker)) {
			t.Logf("Log entry found in Loki after %d seconds", i+1)
			found = true
			break
		}
		t.Logf("attempt %d: marker not found yet", i+1)
	}

	if !found {
		t.Fatal("log entry not found in Loki after 30 seconds")
	}
}

// stripScheme removes http:// or https:// from a URL, returning just host:port.
func stripScheme(url string) string {
	for _, prefix := range []string{"https://", "http://"} {
		if len(url) > len(prefix) && url[:len(prefix)] == prefix {
			return url[len(prefix):]
		}
	}
	return url
}
