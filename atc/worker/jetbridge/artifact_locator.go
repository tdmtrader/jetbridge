package jetbridge

import "sync"

// ArtifactLocator tracks which K8s node holds each artifact key. This enables
// soft scheduling affinity (co-locate steps with their inputs) and local vs
// remote fetch decisions in init containers.
//
// The map is ephemeral — lost on ATC restart. In-flight builds retry from the
// producing step on restart (same as PVC loss behavior).
type ArtifactLocator struct {
	mu    sync.RWMutex
	nodes map[string]string // artifact key → node name
}

// NewArtifactLocator creates a new ArtifactLocator.
func NewArtifactLocator() *ArtifactLocator {
	return &ArtifactLocator{
		nodes: make(map[string]string),
	}
}

// Record associates an artifact key with the node it was stored on.
func (l *ArtifactLocator) Record(key, nodeName string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.nodes[key] = nodeName
}

// Locate returns the node name for a given artifact key.
func (l *ArtifactLocator) Locate(key string) (string, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	node, ok := l.nodes[key]
	return node, ok
}

// Remove deletes an artifact key from the locator (called during GC).
func (l *ArtifactLocator) Remove(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.nodes, key)
}
