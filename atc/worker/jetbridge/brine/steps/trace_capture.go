package steps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/concourse/concourse/tracing"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// SpanCapture reads telemetry produced by JetBridge's production OTLP exporter
// and an official collector subprocess. It implements no exporter or receiver.
type SpanCapture struct {
	export *traceCapture
	lazy   *lazyResource[SpanCapture]
}

// ready must run before the step produces spans, not when assertions read them.
func (s SpanCapture) ready() (SpanCapture, error) {
	if s.lazy != nil {
		return s.lazy.get()
	}
	return s, nil
}

func (s SpanCapture) close() error {
	if s.lazy != nil {
		return s.lazy.close(func(ready SpanCapture) error { return ready.close() })
	}
	return s.export.close()
}

// Read-only projections of the collector's OTLP JSON file, not SDK spans or
// an implementation of the OTLP receiver/exporter interfaces.
type exportedSpan struct {
	Name       string
	TraceID    string `json:"traceId"`
	SpanID     string `json:"spanId"`
	Attributes []exportedAttribute
	Events     []struct {
		Name       string
		Attributes []exportedAttribute
	}
}

type exportedAttribute struct {
	Key   string
	Value struct{ StringValue string }
}

type exportedTraceBatch struct {
	ResourceSpans []struct {
		ScopeSpans []struct{ Spans []exportedSpan }
	}
}

type traceCapture struct {
	root, path          string
	cmd                 *exec.Cmd
	done                chan struct{}
	waitErr             error // written before done closes; read only after receiving done
	log                 *os.File
	provider            *sdktrace.TracerProvider
	previous            trace.TracerProvider
	previousPropagation propagation.TextMapPropagator
	previousConfigured  bool
	once                sync.Once
	closeErr            error
}

func startTraceCapture() (_ SpanCapture, err error) {
	binary := os.Getenv("BRINE_OTELCOL_BINARY")
	if binary == "" {
		binary, err = exec.LookPath("otelcol")
	} else if !filepath.IsAbs(binary) {
		return SpanCapture{}, fmt.Errorf("BRINE_OTELCOL_BINARY must be an absolute path")
	}
	if err != nil {
		return SpanCapture{}, fmt.Errorf("real trace capture needs otelcol on PATH or BRINE_OTELCOL_BINARY: %w", err)
	}
	root, err := AttributedTempDir("brine-trace-*")
	if err != nil {
		return SpanCapture{}, err
	}
	c := &traceCapture{root: root, path: filepath.Join(root, "traces.jsonl"), previous: otel.GetTracerProvider(), previousPropagation: otel.GetTextMapPropagator(), previousConfigured: tracing.Configured}
	defer func() {
		if err != nil {
			err = errors.Join(err, c.close())
		}
	}()
	// freePort closed its listener before the collector binds, so the port is
	// a guess: a second adapter running this suite in parallel probes the same
	// loopback address with the same code. otelcol has no way to inherit a
	// listener this process bound -- its receiver takes an endpoint string and
	// nothing else -- so the collision is survived rather than prevented: a
	// collector that died on its port is started again on a fresh one.
	var endpoint string
	for attempt := 1; ; attempt++ {
		var launchErr error
		endpoint, launchErr = c.launchCollector(binary, root)
		if launchErr == nil {
			break
		}
		if attempt >= daemonPortAttempts || !addressInUse(launchErr.Error()) {
			return SpanCapture{}, launchErr
		}
		if err := c.releaseCollector(); err != nil {
			return SpanCapture{}, errors.Join(launchErr, err)
		}
	}
	// Configure the actual application exporter, batch processor, resource and
	// sampler, rather than replacing any part with an in-memory implementation.
	configTrace := tracing.Config{ServiceName: "jetbridge-brine", OTLP: tracing.OTLP{Address: endpoint}, Sampling: tracing.SamplingConfig{Strategy: "always"}}
	if err := configTrace.Prepare(); err != nil {
		return SpanCapture{}, fmt.Errorf("configure production OTLP export: %w", err)
	}
	var ok bool
	c.provider, ok = otel.GetTracerProvider().(*sdktrace.TracerProvider)
	if !ok {
		return SpanCapture{}, fmt.Errorf("production tracing did not install an SDK provider")
	}
	return SpanCapture{export: c}, nil
}

// launchCollector starts one collector on one freshly probed port and waits
// for it to listen, answering with the endpoint it is on. A failure that was
// the port's fault carries the collector's own log, so addressInUse can read
// the kernel's words out of it -- the collector is another process, and its
// output is the only evidence of why it died.
func (c *traceCapture) launchCollector(binary, root string) (string, error) {
	port, err := freePort()
	if err != nil {
		return "", err
	}
	// Probe the same loopback address the collector is told to bind: a port is
	// free per (address, port) pair.
	endpoint := fmt.Sprintf("127.0.0.1:%d", port)
	// No batch processor or sending queue: the RPC acknowledgment follows the
	// file export. Rotation disables the file exporter's write buffer, so a
	// successful provider ForceFlush makes the received bytes readable now.
	config := fmt.Sprintf("receivers:\n  otlp:\n    protocols:\n      grpc:\n        endpoint: %s\nexporters:\n  file:\n    path: %q\n    format: json\n    rotation:\n      max_megabytes: 100\nservice:\n  telemetry:\n    metrics:\n      level: none\n    logs:\n      level: error\n  pipelines:\n    traces:\n      receivers: [otlp]\n      exporters: [file]\n", endpoint, c.path)
	configPath := filepath.Join(root, "collector.yaml")
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		return "", err
	}
	logPath := filepath.Join(root, "collector.log")
	c.log, err = os.Create(logPath)
	if err != nil {
		return "", err
	}
	c.cmd = exec.Command(binary, "--config", configPath)
	c.cmd.Stdout, c.cmd.Stderr = c.log, c.log
	if err := c.cmd.Start(); err != nil {
		c.cmd = nil
		return "", fmt.Errorf("start trace collector: %w", err)
	}
	c.done = make(chan struct{})
	go func() { c.waitErr = c.cmd.Wait(); close(c.done) }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		select {
		case <-c.done:
			logs, _ := os.ReadFile(logPath)
			return "", fmt.Errorf("trace collector exited during startup: %v: %s", c.waitErr, logs)
		default:
		}
		conn, dialErr := net.DialTimeout("tcp", endpoint, 100*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			return endpoint, nil
		}
		select {
		case <-ctx.Done():
			// A collector that never listened may still be alive and about to:
			// its log is the evidence of whether the port was the reason, and
			// it goes into the message so the retry can read it.
			logs, _ := os.ReadFile(logPath)
			return "", fmt.Errorf("trace collector did not listen: %w: %s", ctx.Err(), logs)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// releaseCollector stops and forgets a collector that failed to come up, so the
// next attempt starts from the same state the first one did. The tracer
// provider is not installed until after a collector is listening, so there is
// nothing else of an attempt to undo.
func (c *traceCapture) releaseCollector() error {
	cmd, done, log := c.cmd, c.done, c.log
	c.cmd, c.done, c.log, c.waitErr = nil, nil, nil, nil
	var err error
	if log != nil {
		err = errors.Join(err, log.Close())
	}
	if cmd == nil || cmd.Process == nil {
		return err
	}
	select {
	case <-done:
		return err
	default:
	}
	if killErr := cmd.Process.Kill(); killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
		return errors.Join(err, killErr)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		return errors.Join(err, fmt.Errorf("trace collector did not exit after kill"))
	}
	return err
}

func (s SpanCapture) spans() ([]exportedSpan, error) {
	c := s.export
	if c == nil || c.provider == nil {
		return nil, fmt.Errorf("trace collector is not configured")
	}
	select {
	case <-c.done:
		return nil, fmt.Errorf("trace collector exited before spans were read: %v", c.waitErr)
	default:
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.provider.ForceFlush(ctx); err != nil {
		return nil, fmt.Errorf("flush production OTLP spans: %w", err)
	}
	file, err := os.Open(c.path)
	if err != nil {
		return nil, fmt.Errorf("read collector export: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	var spans []exportedSpan
	for {
		var message json.RawMessage
		if err := decoder.Decode(&message); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("decode collector export: %w", err)
		}
		var request exportedTraceBatch
		if err := json.Unmarshal(message, &request); err != nil {
			return nil, fmt.Errorf("decode exported OTLP: %w", err)
		}
		for _, resource := range request.ResourceSpans {
			for _, scope := range resource.ScopeSpans {
				spans = append(spans, scope.Spans...)
			}
		}
	}
	if len(spans) == 0 {
		return nil, fmt.Errorf("collector exported no spans")
	}
	return spans, nil
}

func (s SpanCapture) EventNames(spanName string) ([]string, bool, error) {
	spans, err := s.spans()
	if err != nil {
		return nil, false, err
	}
	for _, span := range spans {
		if span.Name == spanName {
			names := make([]string, 0, len(span.Events))
			for _, event := range span.Events {
				names = append(names, event.Name)
			}
			return names, true, nil
		}
	}
	return nil, false, nil
}

func (c *traceCapture) close() error {
	c.once.Do(func() {
		if c.provider != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			c.closeErr = errors.Join(c.closeErr, c.provider.Shutdown(ctx))
			cancel()
		}
		otel.SetTracerProvider(c.previous)
		otel.SetTextMapPropagator(c.previousPropagation)
		tracing.Configured = c.previousConfigured
		joined := true
		if c.cmd != nil {
			joined = false
			select {
			case <-c.done:
				joined = true
			default:
				if err := c.cmd.Process.Signal(os.Interrupt); err != nil && !errors.Is(err, os.ErrProcessDone) {
					c.closeErr = errors.Join(c.closeErr, err)
				}
				select {
				case <-c.done:
					joined = true
				case <-time.After(5 * time.Second):
					c.closeErr = errors.Join(c.closeErr, fmt.Errorf("trace collector did not stop after interrupt"))
					c.closeErr = errors.Join(c.closeErr, c.cmd.Process.Kill())
					select {
					case <-c.done:
						joined = true
					case <-time.After(5 * time.Second):
					}
				}
			}
			if joined {
				c.closeErr = errors.Join(c.closeErr, c.waitErr)
			}
		}
		if c.log != nil {
			c.closeErr = errors.Join(c.closeErr, c.log.Close())
		}
		if joined {
			c.closeErr = errors.Join(c.closeErr, os.RemoveAll(c.root))
		}
	})
	return c.closeErr
}
