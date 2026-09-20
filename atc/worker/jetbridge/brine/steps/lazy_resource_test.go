package steps

import (
	"errors"
	"os"
	"reflect"
	"sync"
	"testing"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/tracing"
	"go.opentelemetry.io/otel"
)

func TestLazyResourceStartsOnceAndDisposesOnce(t *testing.T) {
	starts, stops := 0, 0
	r := &lazyResource[*int]{start: func() (*int, error) {
		starts++
		return new(int), nil
	}}
	var callers sync.WaitGroup
	for range 10 {
		callers.Go(func() {
			if value, err := r.get(); err != nil || value == nil {
				t.Errorf("get: %v, %v", value, err)
			}
		})
	}
	callers.Wait()
	for range 2 {
		if err := r.close(func(*int) error { stops++; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if starts != 1 || stops != 1 {
		t.Fatalf("starts=%d stops=%d", starts, stops)
	}
	if _, err := r.get(); err == nil {
		t.Fatal("disposed resource restarted")
	}
}

func TestLazyResourceCachesStartupFailure(t *testing.T) {
	failure := errors.New("startup failed")
	starts := 0
	r := &lazyResource[int]{start: func() (int, error) { starts++; return 0, failure }}
	for range 2 {
		if _, err := r.get(); !errors.Is(err, failure) {
			t.Fatalf("lost startup error: %v", err)
		}
	}
	if err := r.close(func(int) error { t.Fatal("disposed an unsuccessful start"); return nil }); err != nil {
		t.Fatal(err)
	}
	if starts != 1 {
		t.Fatalf("retried failed startup %d times", starts)
	}
}

func TestLazyFactoriesDoNotStartOrRegisterTempDirsWhenUnused(t *testing.T) {
	// Unusable prerequisites make an accidental eager startup fail immediately.
	t.Setenv("BRINE_OTELCOL_BINARY", "relative-invalid-collector")
	t.Setenv("KUBEBUILDER_ASSETS", "/missing-envtest-assets")
	provider, propagation, configured := otel.GetTracerProvider(), otel.GetTextMapPropagator(), tracing.Configured
	resources := append([]brine.ResourceDefinition{TracingResourceDefinition(), RealClusterResourceDefinition()}, AuthenticationResourceDefinitions()...)
	deps := map[string]any{"jetbridge-db": JetbridgeDB{}}
	before, err := os.ReadDir(adapterDaemonRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, resource := range resources {
		t.Run(resource.Name, func(t *testing.T) {
			value, err := resource.Factory(deps)
			if err != nil {
				t.Fatalf("unused factory did work: %v", err)
			}
			deps[resource.Name] = value
			t.Cleanup(func() { _ = resource.Disposer(value) })
			acquired, err := os.ReadDir(adapterDaemonRoot)
			if err != nil || !reflect.DeepEqual(before, acquired) {
				t.Fatalf("factory allocated temp dirs before use: before=%v after=%v err=%v", before, acquired, err)
			}
			if err := resource.Disposer(value); err != nil {
				t.Fatalf("unused disposal: %v", err)
			}
		})
	}
	after, err := os.ReadDir(adapterDaemonRoot)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("unused resources changed temp dirs: before=%v after=%v err=%v", before, after, err)
	}
	if otel.GetTracerProvider() != provider || !reflect.DeepEqual(otel.GetTextMapPropagator(), propagation) || tracing.Configured != configured {
		t.Fatal("unused tracing resource changed globals")
	}
}

func TestLazyAuthServerStartsItsDependencyOnlyOnUse(t *testing.T) {
	definitions := AuthenticationResourceDefinitions()
	var binaries, server brine.ResourceDefinition
	for _, definition := range definitions {
		switch definition.Name {
		case "auth-binaries":
			binaries = definition
		case "auth-server":
			server = definition
		}
	}
	value, err := binaries.Factory(nil)
	if err != nil {
		t.Fatal(err)
	}
	bin := value.(authBinaries)
	failure := errors.New("unavailable auth binaries")
	starts := 0
	bin.lazy.start = func() (authBinaries, error) { starts++; return authBinaries{}, failure }
	fixture, err := server.Factory(map[string]any{"jetbridge-db": JetbridgeDB{}, "auth-binaries": bin})
	if err != nil || starts != 0 {
		t.Fatalf("factory started its dependency: starts=%d err=%v", starts, err)
	}
	for range 2 {
		if _, err := fixture.(*AuthFixture).ready(); !errors.Is(err, failure) {
			t.Fatalf("first use lost dependency error: %v", err)
		}
	}
	if starts != 1 {
		t.Fatalf("started dependency %d times", starts)
	}
	if err := server.Disposer(fixture); err != nil {
		t.Fatal(err)
	}
	if err := binaries.Disposer(value); err != nil {
		t.Fatal(err)
	}
}

func TestLazyTraceFactoryReportsMissingCollectorOnlyOnUse(t *testing.T) {
	t.Setenv("BRINE_OTELCOL_BINARY", "relative-invalid-collector")
	resource := TracingResourceDefinition()
	value, err := resource.Factory(nil)
	if err != nil {
		t.Fatal(err)
	}
	capture := value.(SpanCapture)
	if capture.export != nil {
		t.Fatal("collector started before first use")
	}
	if _, err := capture.ready(); err == nil {
		t.Fatal("first use did not validate the collector")
	}
	if err := resource.Disposer(value); err != nil {
		t.Fatal(err)
	}
}

func TestLazyMCPProbeStartsAuthBeforeBuildingScenario(t *testing.T) {
	failure := errors.New("auth startup failed")
	starts := 0
	fixture := &AuthFixture{lazy: &lazyResource[*AuthFixture]{start: func() (*AuthFixture, error) { starts++; return nil, failure }}}
	if _, err := newMCPProbeScenario(fixture); !errors.Is(err, failure) || starts != 1 {
		t.Fatalf("probe bypassed auth accessor: starts=%d err=%v", starts, err)
	}
}

type lazyTestResources map[string]any

func (r lazyTestResources) Get(name string) any { return r[name] }

func TestLazyRealClusterWarmupAndStepsShareOneStartup(t *testing.T) {
	starts := 0
	ready := &realCluster{}
	lazy := &realCluster{lazy: &lazyResource[*realCluster]{start: func() (*realCluster, error) { starts++; return ready, nil }}}
	resources := lazyTestResources{"real-cluster": lazy}
	if err := StartRealCluster(resources); err != nil {
		t.Fatal(err)
	}
	got, err := getRealCluster(resources)
	if err != nil || got != ready || starts != 1 {
		t.Fatalf("warmup and step do not share accessor: got=%p starts=%d err=%v", got, starts, err)
	}
}
