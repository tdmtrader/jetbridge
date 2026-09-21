package review

import (
	"context"
	"testing"
)

func TestWorkerRequiresMemoryRuntime(t *testing.T) {
	// Ordinary temporary directories must not become a credential store merely
	// because they will be deleted later. No real credentials are read here.
	if err := requireMemoryRuntime(t.TempDir()); err == nil {
		t.Skip("host temporary directory is itself memory backed")
	}
	_, err := RunWorker(context.Background(), WorkerOptions{RuntimeDir: t.TempDir()})
	if err == nil {
		t.Fatal("worker accepted disk runtime")
	}
}
