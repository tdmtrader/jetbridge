package jetbridge

import "sync"

// ArtifactLocation holds the node name and daemon key for an artifact.
type ArtifactLocation struct {
	NodeName string
	HostDir  string // daemon key, e.g. "build-42/result" (maps to steps/<key> on daemon)
}

// ArtifactLocator tracks which K8s node holds each artifact key and the
// hostPath directory where the data is stored. This enables soft scheduling
// affinity (co-locate steps with their inputs) and local vs remote fetch
// decisions in init containers.
//
// The map is ephemeral — lost on ATC restart. In-flight builds retry from the
// producing step on restart (same as PVC loss behavior).
type ArtifactLocator struct {
	mu        sync.RWMutex
	locations map[string]ArtifactLocation // artifact key → location
}

// NewArtifactLocator creates a new ArtifactLocator.
func NewArtifactLocator() *ArtifactLocator {
	return &ArtifactLocator{
		locations: make(map[string]ArtifactLocation),
	}
}

// Record associates an artifact key with the node and directory it was stored on.
func (l *ArtifactLocator) Record(key, nodeName, hostDir string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.locations[key] = ArtifactLocation{NodeName: nodeName, HostDir: hostDir}
}

// Locate returns the location for a given artifact key.
func (l *ArtifactLocator) Locate(key string) (ArtifactLocation, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	loc, ok := l.locations[key]
	return loc, ok
}

// LocateNode returns just the node name for a given artifact key (convenience).
func (l *ArtifactLocator) LocateNode(key string) (string, bool) {
	loc, ok := l.Locate(key)
	return loc.NodeName, ok
}

// Remove deletes an artifact key from the locator (called during GC).
func (l *ArtifactLocator) Remove(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.locations, key)
}
