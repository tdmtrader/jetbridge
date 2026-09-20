package steps

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/concourse/concourse/tracing"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

func TestTraceCaptureExportsAndDisposesRealCollector(t *testing.T) {
	previous, propagation, configured := otel.GetTracerProvider(), otel.GetTextMapPropagator(), tracing.Configured
	resource := TracingResourceDefinition()
	for i := 0; i < 2; i++ {
		func() {
			value, err := resource.Factory(nil)
			if err != nil {
				t.Fatal(err)
			}
			capture, err := value.(SpanCapture).ready()
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := resource.Disposer(capture); err != nil {
					t.Error(err)
				}
			}()
			_, span := tracing.StartSpan(context.Background(), "real-export-contract", tracing.Attrs{"exec.purpose": "stream-out", "volume.mount_path": "/fixture"})
			eventName := fmt.Sprintf("iteration-%d", i)
			span.AddEvent(eventName, trace.WithAttributes(attribute.String("node.name", "real-node")))
			// Distinct repeated events must survive export without deduplication by
			// the capture reader; only the production lifecycle tracker owns dedup.
			span.AddEvent(eventName, trace.WithAttributes(attribute.String("node.name", "real-node")))
			span.End()
			names, found, err := capture.EventNames("real-export-contract")
			if err != nil || !found || !reflect.DeepEqual(names, []string{eventName, eventName}) {
				t.Fatalf("exported events=%v found=%v err=%v", names, found, err)
			}
			spans, err := capture.spans()
			if err != nil || len(spans) != 1 {
				t.Fatalf("exported spans=%v err=%v", spans, err)
			}
			exported := spans[0]
			attrs := map[string]string{}
			for _, attr := range exported.Attributes {
				attrs[attr.Key] = attr.Value.StringValue
			}
			if !reflect.DeepEqual(attrs, map[string]string{"exec.purpose": "stream-out", "volume.mount_path": "/fixture"}) {
				t.Fatalf("exported span attributes lost: %v", attrs)
			}
			sc := span.SpanContext()
			if exported.TraceID != sc.TraceID().String() || exported.SpanID != sc.SpanID().String() {
				t.Fatalf("exported IDs do not identify the production span: %+v", exported)
			}
			for _, event := range exported.Events {
				if len(event.Attributes) != 1 || event.Attributes[0].Key != "node.name" || event.Attributes[0].Value.StringValue != "real-node" {
					t.Fatalf("exported attributes lost: %+v", event.Attributes)
				}
			}
			data, err := os.ReadFile(capture.export.path)
			if err != nil || !strings.Contains(string(data), "jetbridge-brine") {
				t.Fatalf("missing production service resource in collector file: %s (%v)", data, err)
			}
			if err := resource.Disposer(capture); err != nil {
				t.Fatal(err)
			}
			if !capture.export.cmd.ProcessState.Success() {
				t.Fatalf("collector did not exit cleanly: %v", capture.export.cmd.ProcessState)
			}
			if _, err := os.Stat(capture.export.root); !os.IsNotExist(err) {
				t.Fatalf("collector workspace remains: %v", err)
			}
			if otel.GetTracerProvider() != previous || !reflect.DeepEqual(otel.GetTextMapPropagator(), propagation) || tracing.Configured != configured {
				t.Fatal("trace resource did not restore the previous globals")
			}
		}()
	}
}
