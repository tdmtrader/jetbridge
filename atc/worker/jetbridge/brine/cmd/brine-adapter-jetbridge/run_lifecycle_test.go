package main

import (
	"errors"
	"reflect"
	"testing"

	"github.com/brine-dev/brine-go/pkg/brine"
)

func TestLifecycleDocumentWarmsLocalAndMixedRunsBeforeSteps(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sources []string
		warm    bool
	}{
		{"local", []string{"features/task.feature"}, true},
		{"live only", []string{"features/live/task.feature", "/repo/features/live/pod.feature"}, false},
		{"mixed", []string{"features/live/task.feature", "features/exec.feature"}, true},
		{"cleaned local", []string{"features/live/../exec.feature"}, true},
		{"unknown", []string{""}, true},
		{"empty", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var plan brine.ExecutionPlan
			for _, source := range tc.sources {
				plan.Features = append(plan.Features, brine.PlannedFeature{Source: source})
			}
			var calls []string
			registry, err := brine.NewResourceRegistry([]brine.ResourceDefinition{
				{Name: "live-namespace-sweep", Scope: brine.ScopeSuite, Factory: func(map[string]any) (any, error) { calls = append(calls, "startup sweep"); return true, nil }},
				{Name: "real-cluster", Scope: brine.ScopeSuite, Factory: func(map[string]any) (any, error) { calls = append(calls, "acquire"); return "lazy cluster", nil }},
			})
			if err != nil {
				t.Fatal(err)
			}
			state := brine.NewResourceState(registry)
			err = prepareRun(plan, state, func(resources brine.Resources) error {
				if resources.Get("real-cluster") != "lazy cluster" {
					t.Fatal("accessor ran before acquisition")
				}
				calls = append(calls, "ready")
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			calls = append(calls, "step deadline opens")
			want := []string{"startup sweep", "step deadline opens"}
			if tc.warm {
				want = []string{"startup sweep", "acquire", "ready", "step deadline opens"}
			}
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("wrong startup order: %v", calls)
			}
		})
	}
}

func TestLifecycleWarmupFailurePreventsStepDeadlines(t *testing.T) {
	registry, err := brine.NewResourceRegistry([]brine.ResourceDefinition{
		{Name: "live-namespace-sweep", Scope: brine.ScopeSuite, Factory: func(map[string]any) (any, error) { return true, nil }},
		{Name: "real-cluster", Scope: brine.ScopeSuite, Factory: func(map[string]any) (any, error) { return true, nil }},
	})
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("cold envtest failed")
	plan := brine.ExecutionPlan{Features: []brine.PlannedFeature{{Source: "features/exec.feature"}}}
	if err := prepareRun(plan, brine.NewResourceState(registry), func(brine.Resources) error { return failure }); !errors.Is(err, failure) {
		t.Fatalf("lost warmup error: %v", err)
	}
}
