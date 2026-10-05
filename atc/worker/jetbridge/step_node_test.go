package jetbridge

import (
	"testing"

	"github.com/concourse/concourse/atc/runtime"
	corev1 "k8s.io/api/core/v1"
)

func TestParseStepNodeLabel(t *testing.T) {
	got, err := ParseStepNodeLabel("kubernetes.io/hostname=k3s-agent-y")
	if err != nil {
		t.Fatal(err)
	}
	if got != (StepNodeLabel{Key: "kubernetes.io/hostname", Value: "k3s-agent-y"}) {
		t.Fatalf("got %+v", got)
	}
	for _, bad := range []string{"", "no-equals", "=value", "bad key=v", "k=bad value"} {
		if _, err := ParseStepNodeLabel(bad); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
}

// The preference is soft and is added beside, never instead of, what the
// storage backend requires and prefers.
func TestBuildAffinity_PreferredStepNode(t *testing.T) {
	preferred := &StepNodeLabel{Key: "kubernetes.io/hostname", Value: "big-node"}
	want := corev1.PreferredSchedulingTerm{
		Weight: PreferredStepNodeWeight,
		Preference: corev1.NodeSelectorTerm{MatchExpressions: []corev1.NodeSelectorRequirement{{
			Key: "kubernetes.io/hostname", Operator: corev1.NodeSelectorOpIn, Values: []string{"big-node"},
		}}},
	}

	t.Run("without a storage backend", func(t *testing.T) {
		c := &Container{config: Config{Namespace: "ns", PreferredStepNode: preferred}, properties: map[string]string{}}
		affinity := c.buildAffinity()
		if affinity == nil || affinity.NodeAffinity == nil {
			t.Fatal("expected a node affinity")
		}
		if affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution != nil {
			t.Error("a preference must not add a requirement")
		}
		terms := affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution
		if len(terms) != 1 || terms[0].Weight != want.Weight || terms[0].Preference.MatchExpressions[0].Values[0] != "big-node" {
			t.Errorf("preferred terms = %+v", terms)
		}
	})

	t.Run("beside the daemonset backend's own terms", func(t *testing.T) {
		locator := NewArtifactLocator()
		locator.Record(ArtifactKey(constructionArtifact("vol-a", "test-worker").Handle()), "node-2", "/artifacts/vol-a")
		cfg := Config{Namespace: "ns", ArtifactDaemonHostPath: "/artifacts", PreferredStepNode: preferred}
		backend := NewDaemonSetBackend(cfg, locator, nil, nil)
		c := &Container{
			config:         cfg,
			properties:     map[string]string{},
			storageBackend: backend,
			containerSpec: runtime.ContainerSpec{Inputs: []runtime.Input{
				{Artifact: constructionArtifact("vol-a", "test-worker"), DestinationPath: "/in/a"},
			}},
		}
		node := c.buildAffinity().NodeAffinity
		if node.RequiredDuringSchedulingIgnoredDuringExecution == nil {
			t.Fatal("the backend's artifact-cache requirement was dropped")
		}
		terms := node.PreferredDuringSchedulingIgnoredDuringExecution
		if len(terms) != 2 {
			t.Fatalf("want input locality and the preference, got %+v", terms)
		}
		if terms[0].Preference.MatchExpressions[0].Values[0] != "node-2" {
			t.Errorf("input locality term = %+v", terms[0])
		}
		if terms[1].Weight != terms[0].Weight {
			t.Errorf("preference weight %d != input locality weight %d", terms[1].Weight, terms[0].Weight)
		}
	})
}

// The requirement is ANDed into every required term the backend built, so it
// narrows placement without dropping the artifact-cache or reserving-node
// requirements, and adds no preference of its own.
func TestBuildAffinity_RequiredStepNode(t *testing.T) {
	required := &StepNodeLabel{Key: "kubernetes.io/hostname", Value: "theborg"}
	want := corev1.NodeSelectorRequirement{Key: "kubernetes.io/hostname", Operator: corev1.NodeSelectorOpIn, Values: []string{"theborg"}}

	t.Run("without a storage backend", func(t *testing.T) {
		c := &Container{config: Config{Namespace: "ns", RequiredStepNode: required}, properties: map[string]string{}}
		node := c.buildAffinity().NodeAffinity
		terms := node.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
		if len(terms) != 1 || len(terms[0].MatchExpressions) != 1 || terms[0].MatchExpressions[0].Values[0] != "theborg" {
			t.Fatalf("required terms = %+v", terms)
		}
		if len(node.PreferredDuringSchedulingIgnoredDuringExecution) != 0 {
			t.Errorf("a requirement must not add a preference")
		}
	})

	t.Run("beside the daemonset backend's own requirement", func(t *testing.T) {
		cfg := Config{Namespace: "ns", ArtifactDaemonHostPath: "/artifacts", RequiredStepNode: required}
		c := &Container{
			config:         cfg,
			properties:     map[string]string{},
			storageBackend: NewDaemonSetBackend(cfg, nil, nil, nil),
		}
		terms := c.buildAffinity().NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
		if len(terms) != 1 {
			t.Fatalf("want one term, got %+v", terms)
		}
		var sawCache, sawNode bool
		for _, expr := range terms[0].MatchExpressions {
			switch {
			case expr.Key == "concourse.dev/artifact-cache":
				sawCache = true
			case expr.Key == want.Key && expr.Values[0] == "theborg":
				sawNode = true
			}
		}
		if !sawCache || !sawNode {
			t.Errorf("want the artifact-cache requirement AND the node, got %+v", terms[0].MatchExpressions)
		}
	})
}

// A node a step is steered to may be reserved for steps by a NoSchedule taint
// of the same label; the step pod tolerates exactly that taint -- not another
// value, not NoExecute, not a cordon -- and nothing when no node is named.
func TestBuildTolerations(t *testing.T) {
	battle := StepNodeLabel{Key: "jetbridge.dev/battle-station", Value: "true"}
	want := corev1.Toleration{
		Key: "jetbridge.dev/battle-station", Operator: corev1.TolerationOpEqual, Value: "true", Effect: corev1.TaintEffectNoSchedule,
	}

	t.Run("none without step-node labels", func(t *testing.T) {
		c := &Container{config: Config{Namespace: "ns"}}
		if got := c.buildTolerations(); got != nil {
			t.Errorf("tolerations = %+v, want none", got)
		}
	})

	t.Run("the preferred node's taint", func(t *testing.T) {
		c := &Container{config: Config{Namespace: "ns", PreferredStepNode: &battle}}
		if got := c.buildTolerations(); len(got) != 1 || got[0] != want {
			t.Fatalf("tolerations = %+v, want [%+v]", got, want)
		}
	})

	t.Run("required, preferred and extra, without duplicates", func(t *testing.T) {
		pool := StepNodeLabel{Key: "ci.example/pool", Value: "builds"}
		extra := corev1.Toleration{Key: "example.com/gpu", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}
		c := &Container{config: Config{
			Namespace:         "ns",
			PreferredStepNode: &battle,
			RequiredStepNode:  &pool,
			StepTolerations:   []corev1.Toleration{want, extra},
		}}
		got := c.buildTolerations()
		if len(got) != 3 || got[0] != want || got[1] != pool.toleration() || got[2] != extra {
			t.Errorf("tolerations = %+v", got)
		}
	})

	t.Run("on the built pod", func(t *testing.T) {
		cfg := capturePodConfig(false)
		cfg.PreferredStepNode = &battle
		pod, err := capturingContainer(t, cfg, false, nil).
			buildPod(runtime.ProcessSpec{Path: "/bin/sh"}, []string{"sh"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := pod.Spec.Tolerations; len(got) != 1 || got[0] != want {
			t.Errorf("pod tolerations = %+v, want [%+v]", got, want)
		}
	})
}
