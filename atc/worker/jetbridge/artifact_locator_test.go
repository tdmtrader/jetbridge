package jetbridge

import (
	"sync"
	"testing"
)

func TestArtifactLocator_RecordAndLocate(t *testing.T) {
	locator := NewArtifactLocator()

	locator.Record("artifacts/abc.tar", "node-1")
	locator.Record("artifacts/def.tar", "node-2")

	node, ok := locator.Locate("artifacts/abc.tar")
	if !ok || node != "node-1" {
		t.Errorf("expected node-1, got %q (found=%v)", node, ok)
	}

	node, ok = locator.Locate("artifacts/def.tar")
	if !ok || node != "node-2" {
		t.Errorf("expected node-2, got %q (found=%v)", node, ok)
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

	locator.Record("artifacts/abc.tar", "node-1")
	locator.Remove("artifacts/abc.tar")

	_, ok := locator.Locate("artifacts/abc.tar")
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
			locator.Record(key, "node-x")
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
