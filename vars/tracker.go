package vars

import (
	"strings"
	"sync"
)

type TrackedVarsIterator interface {
	YieldCred(string, string)
}

// SecretRef holds the coordinates of an external secret store entry (e.g. a
// Kubernetes Secret) so that runtimes can reference the secret natively
// instead of embedding the literal value.
type SecretRef struct {
	Namespace string
	Name      string
	Key       string
}

// SecretRefResolver is implemented by Variables backends that can provide
// native secret store references for resolved variables.
type SecretRefResolver interface {
	GetSecretRef(ref Reference) (*SecretRef, bool)
}

// TrackedSecretRefsIterator iterates over tracked secret references.
type TrackedSecretRefsIterator interface {
	YieldSecretRef(varPath string, ref SecretRef)
}

type Tracker struct {
	// Considering in-parallel steps, a lock is need.
	lock              sync.RWMutex
	interpolatedCreds map[string]string
	secretRefs        map[string]SecretRef
}

func NewTracker() *Tracker {
	return &Tracker{
		interpolatedCreds: map[string]string{},
		secretRefs:        map[string]SecretRef{},
	}
}

func (t *Tracker) Track(varRef Reference, val any) {
	t.lock.Lock()
	defer t.lock.Unlock()

	t.track(varRef, val)
}

func (t *Tracker) track(varRef Reference, val any) {
	switch v := val.(type) {
	case map[any]any:
		for kk, vv := range v {
			t.track(Reference{
				Path:   varRef.Path,
				Fields: append(varRef.Fields, kk.(string)),
			}, vv)
		}
	case map[string]any:
		for kk, vv := range v {
			t.track(Reference{
				Path:   varRef.Path,
				Fields: append(varRef.Fields, kk),
			}, vv)
		}
	case string:
		paths := append([]string{varRef.Path}, varRef.Fields...)

		t.interpolatedCreds[strings.Join(paths, ".")] = v
	default:
		// Do nothing
	}
}

func (t *Tracker) TrackSecretRef(varRef Reference, ref SecretRef) {
	t.lock.Lock()
	defer t.lock.Unlock()

	t.secretRefs[varRef.Path] = ref
}

func (t *Tracker) IterateInterpolatedCreds(iter TrackedVarsIterator) {
	t.lock.RLock()
	defer t.lock.RUnlock()
	for k, v := range t.interpolatedCreds {
		iter.YieldCred(k, v)
	}
}

func (t *Tracker) IterateSecretRefs(iter TrackedSecretRefsIterator) {
	t.lock.RLock()
	defer t.lock.RUnlock()
	for k, v := range t.secretRefs {
		iter.YieldSecretRef(k, v)
	}
}

type CredVarsTracker struct {
	*Tracker
	CredVars Variables
}

func (t *CredVarsTracker) Get(ref Reference) (any, bool, error) {
	val, found, err := t.CredVars.Get(ref)
	if found {
		t.Tracker.Track(ref, val)

		if resolver, ok := t.CredVars.(SecretRefResolver); ok {
			if secretRef, hasRef := resolver.GetSecretRef(ref); hasRef {
				t.Tracker.TrackSecretRef(ref, *secretRef)
			}
		}
	}
	return val, found, err
}

func (t *CredVarsTracker) IterateSecretRefs(iter TrackedSecretRefsIterator) {
	t.Tracker.IterateSecretRefs(iter)
}

func (t *CredVarsTracker) List() ([]Reference, error) {
	return t.CredVars.List()
}

// TrackedVarsMap is a TrackedVarsIterator which populates interpolated secrets into a map.
// If there are multiple secrets with the same name, it only keeps the first value.
type TrackedVarsMap map[string]string

func (it TrackedVarsMap) YieldCred(k, v string) {
	_, found := it[k]
	if !found {
		it[k] = v
	}
}
