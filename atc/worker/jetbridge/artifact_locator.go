package jetbridge

import (
	"sort"
	"sync"
)

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
// It also indexes keys by the step directory they belong to, because the two
// sides of a step's life speak different keys. A step's outputs are recorded
// under VOLUME handles ("<h>-dir", "<h>-input-N", "<h>-output-<name>") and a
// resource cache under its own key, while the Reaper is handed CONTAINER
// handles, and the daemon's steps/<h> directory is named by the container
// handle. The index is exact rather than a prefix match on "<h>-": handles
// are not prefix-free (a pod's readable handle can be another's plus a
// suffix), and a resource cache key shares no prefix with anything.
//
// The map is ephemeral — lost on ATC restart. In-flight builds retry from the
// producing step on restart.
type ArtifactLocator struct {
	mu        sync.RWMutex
	locations map[string]ArtifactLocation // artifact key → location

	// stepKeys: step directory (container handle) → the volume keys recorded
	// for it. stepOf is its inverse.
	stepKeys map[string]map[string]struct{}
	stepOf   map[string]string

	// aliasesInto: step directory → alias keys (resource caches) the daemon
	// resolves into it. aliasOf is its inverse.
	aliasesInto map[string]map[string]struct{}
	aliasOf     map[string]string
}

// StepArtifacts is what the locator knows about one step directory.
type StepArtifacts struct {
	// Keys are the volume keys recorded for the step.
	Keys []string
	// Nodes are the distinct nodes those keys were recorded on; empty when
	// none was known at recording time.
	Nodes []string
	// AliasedBy are alias keys -- resource caches -- that resolve into the
	// step's directory. While any exists, the directory is not the step's
	// alone to delete.
	AliasedBy []string
}

// NewArtifactLocator creates a new ArtifactLocator.
func NewArtifactLocator() *ArtifactLocator {
	return &ArtifactLocator{
		locations:   make(map[string]ArtifactLocation),
		stepKeys:    make(map[string]map[string]struct{}),
		stepOf:      make(map[string]string),
		aliasesInto: make(map[string]map[string]struct{}),
		aliasOf:     make(map[string]string),
	}
}

// Record associates an artifact key with the node and directory it was stored
// on, owned by no step. Production writers use RecordStepOutput and
// RecordAlias, which the Reaper can find again.
func (l *ArtifactLocator) Record(key, nodeName, hostDir string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.forgetLocked(key)
	l.locations[key] = ArtifactLocation{NodeName: nodeName, HostDir: hostDir}
}

// RecordStepOutput records a volume key of the step whose daemon directory is
// steps/<stepDir>, so reaping that step can find and retire it.
func (l *ArtifactLocator) RecordStepOutput(stepDir, key, nodeName, hostDir string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.forgetLocked(key)
	l.locations[key] = ArtifactLocation{NodeName: nodeName, HostDir: hostDir}
	indexAdd(l.stepKeys, stepDir, key)
	l.stepOf[key] = stepDir
}

// RecordAlias records an alias key (a resource cache) that the daemon
// resolves into steps/<stepDir>. The location is recorded only when the node
// is known; the dependency on the step directory is recorded regardless, since
// the daemon's alias exists either way.
func (l *ArtifactLocator) RecordAlias(key, nodeName, stepDir string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.forgetLocked(key)
	if nodeName != "" {
		l.locations[key] = ArtifactLocation{NodeName: nodeName, HostDir: key}
	}
	if stepDir != "" {
		indexAdd(l.aliasesInto, stepDir, key)
		l.aliasOf[key] = stepDir
	}
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

// Step returns what is recorded for the step directory steps/<stepDir>.
func (l *ArtifactLocator) Step(stepDir string) StepArtifacts {
	l.mu.RLock()
	defer l.mu.RUnlock()

	var step StepArtifacts
	nodes := map[string]struct{}{}
	for key := range l.stepKeys[stepDir] {
		step.Keys = append(step.Keys, key)
		if node := l.locations[key].NodeName; node != "" {
			nodes[node] = struct{}{}
		}
	}
	for node := range nodes {
		step.Nodes = append(step.Nodes, node)
	}
	for key := range l.aliasesInto[stepDir] {
		step.AliasedBy = append(step.AliasedBy, key)
	}
	sort.Strings(step.Keys)
	sort.Strings(step.Nodes)
	sort.Strings(step.AliasedBy)
	return step
}

// ForgetStep retires every volume key recorded for steps/<stepDir>. Aliases
// into it are left alone: they are resource caches, which outlive the step.
func (l *ArtifactLocator) ForgetStep(stepDir string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for key := range l.stepKeys[stepDir] {
		l.forgetLocked(key)
	}
}

// Remove deletes an artifact key from the locator.
func (l *ArtifactLocator) Remove(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.forgetLocked(key)
}

func (l *ArtifactLocator) forgetLocked(key string) {
	delete(l.locations, key)
	if step, ok := l.stepOf[key]; ok {
		delete(l.stepOf, key)
		indexRemove(l.stepKeys, step, key)
	}
	if step, ok := l.aliasOf[key]; ok {
		delete(l.aliasOf, key)
		indexRemove(l.aliasesInto, step, key)
	}
}

func indexAdd(index map[string]map[string]struct{}, group, key string) {
	set, ok := index[group]
	if !ok {
		set = make(map[string]struct{})
		index[group] = set
	}
	set[key] = struct{}{}
}

func indexRemove(index map[string]map[string]struct{}, group, key string) {
	set := index[group]
	delete(set, key)
	if len(set) == 0 {
		delete(index, group)
	}
}
