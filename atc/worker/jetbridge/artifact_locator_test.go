package jetbridge

import (
	"sync"
	"testing"
)

func TestArtifactLocator_RecordAndLocate(t *testing.T) {
	locator := NewArtifactLocator()

	locator.Record("abc", "node-1", "")
	locator.Record("def", "node-2", "")

	loc, ok := locator.Locate("abc")
	if !ok || loc.NodeName != "node-1" {
		t.Errorf("expected node-1, got %q (found=%v)", loc.NodeName, ok)
	}

	loc, ok = locator.Locate("def")
	if !ok || loc.NodeName != "node-2" {
		t.Errorf("expected node-2, got %q (found=%v)", loc.NodeName, ok)
	}
}

func TestArtifactLocator_LocateNode(t *testing.T) {
	locator := NewArtifactLocator()

	locator.Record("abc", "node-1", "container-abc/output")

	node, ok := locator.LocateNode("abc")
	if !ok || node != "node-1" {
		t.Errorf("expected node-1, got %q (found=%v)", node, ok)
	}

	_, ok = locator.LocateNode("nonexistent")
	if ok {
		t.Error("expected not found for nonexistent key")
	}
}

func TestArtifactLocator_LocateWithHostDir(t *testing.T) {
	locator := NewArtifactLocator()

	locator.Record("abc", "node-1", "container-abc/output")

	loc, ok := locator.Locate("abc")
	if !ok {
		t.Fatal("expected found")
	}
	if loc.NodeName != "node-1" {
		t.Errorf("expected node-1, got %q", loc.NodeName)
	}
	if loc.HostDir != "container-abc/output" {
		t.Errorf("expected container-abc/output, got %q", loc.HostDir)
	}
}

func TestArtifactLocator_LocateMissing(t *testing.T) {
	locator := NewArtifactLocator()

	_, ok := locator.Locate("nonexistent")
	if ok {
		t.Error("expected not found for nonexistent key")
	}
}

func TestArtifactLocator_Remove(t *testing.T) {
	locator := NewArtifactLocator()

	locator.Record("abc", "node-1", "")
	locator.Remove("abc")

	_, ok := locator.Locate("abc")
	if ok {
		t.Error("expected not found after Remove")
	}
}

func TestArtifactLocator_ConcurrentAccess(t *testing.T) {
	locator := NewArtifactLocator()
	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(3)
		key := "key-" + string(rune('a'+i%26))
		go func() {
			defer wg.Done()
			locator.Record(key, "node-x", "")
		}()
		go func() {
			defer wg.Done()
			locator.Locate(key)
		}()
		go func() {
			defer wg.Done()
			locator.Remove(key)
		}()
	}
	wg.Wait()
}
