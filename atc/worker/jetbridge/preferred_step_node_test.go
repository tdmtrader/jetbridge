package jetbridge

import (
	"testing"

	"github.com/concourse/concourse/atc/runtime"
	corev1 "k8s.io/api/core/v1"
)

func TestParsePreferredStepNode(t *testing.T) {
	got, err := ParsePreferredStepNode("kubernetes.io/hostname=k3s-agent-y")
	if err != nil {
		t.Fatal(err)
	}
	if got != (PreferredStepNode{Key: "kubernetes.io/hostname", Value: "k3s-agent-y"}) {
		t.Fatalf("got %+v", got)
	}
	for _, bad := range []string{"", "no-equals", "=value", "bad key=v", "k=bad value"} {
		if _, err := ParsePreferredStepNode(bad); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
}

// The preference is soft and is added beside, never instead of, what the
// storage backend requires and prefers.
func TestBuildAffinity_PreferredStepNode(t *testing.T) {
	preferred := &PreferredStepNode{Key: "kubernetes.io/hostname", Value: "big-node"}
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
