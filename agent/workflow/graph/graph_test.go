package graph

import "testing"

func TestNodeKindValidation(t *testing.T) {
	valid := []NodeKind{KindInput, KindResourceSource, KindLoad, KindAgent, KindTask, KindAwait, KindPublish, KindOutput}
	for _, kind := range valid {
		if err := kind.Validate(); err != nil {
			t.Fatalf("expected %q to be valid, got: %v", kind, err)
		}
	}
	if err := NodeKind("prompt").Validate(); err == nil {
		t.Fatal("expected an unknown node kind to be rejected")
	}
}

func TestGraphLookupByID(t *testing.T) {
	g := Graph{Nodes: []Node{{ID: "implement", Kind: KindAgent, DisplayName: "implement"}}}
	node, found := g.Node("implement")
	if !found || node.Kind != KindAgent {
		t.Fatalf("expected to find the agent node, got found=%v node=%+v", found, node)
	}
	if _, found := g.Node("absent"); found {
		t.Fatal("expected absent node lookup to report not found")
	}
}
