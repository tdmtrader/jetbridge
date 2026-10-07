package bootstrap_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/hangar/bootstrap"
)

// memoryStore is an in-memory SecretStore. failAfter > 0 makes the create
// after that many succeed fail, as an interrupted run does.
type memoryStore struct {
	secrets   map[string]bootstrap.Secret
	created   []string
	failAfter int
}

func newMemoryStore() *memoryStore { return &memoryStore{secrets: map[string]bootstrap.Secret{}} }

func (store *memoryStore) Get(_ context.Context, name string) (bootstrap.Secret, bool, error) {
	secret, found := store.secrets[name]
	return secret, found, nil
}

func (store *memoryStore) Create(_ context.Context, secret bootstrap.Secret) error {
	if store.failAfter > 0 && len(store.created) == store.failAfter {
		return errors.New("the API server went away")
	}
	if _, found := store.secrets[secret.Name]; found {
		return fmt.Errorf("secret %q already exists", secret.Name)
	}
	store.secrets[secret.Name] = secret
	store.created = append(store.created, secret.Name)
	return nil
}

func (store *memoryStore) snapshot() map[string]map[string][]byte {
	out := map[string]map[string][]byte{}
	for name, secret := range store.secrets {
		out[name] = maps.Clone(secret.Data)
	}
	return out
}

func inventory() bootstrap.Inventory {
	return bootstrap.Inventory{
		Labels: map[string]string{"app.kubernetes.io/managed-by": "concourse-hangar-bootstrap"},
		Entries: []bootstrap.Entry{
			{Name: "hangar-key", Kind: bootstrap.KindRandomKey, Key: "hangar.key"},
			{Name: "store-tls", Kind: bootstrap.KindTLSBundle, CommonName: "store", DNSNames: []string{"concourse-hangar-store.cicd.svc"}},
			{Name: "store-tokens", Kind: bootstrap.KindStoreTokens},
			{Name: "control-1", Kind: bootstrap.KindEd25519, Key: "control.key", Ring: bootstrap.ControlRing, Epoch: 1},
			{Name: "warrant-key", Kind: bootstrap.KindRandomKey, Key: "capability.key"},
			{Name: "output-server", Kind: bootstrap.KindTLSServer, CA: "output-ca", CommonName: "output", DNSNames: []string{"output-daemon.cicd"}},
			{Name: "output-client", Kind: bootstrap.KindTLSClient, CA: "output-ca", CommonName: "web"},
			{Name: "output-ca", Kind: bootstrap.KindCA, CommonName: "output plane"},
			{Name: "materialize-key", Kind: bootstrap.KindRandomKey, Key: "materialize.key"},
			{Name: "rings", Kind: bootstrap.KindRing, ActiveEpoch: 1},
		},
	}
}

func reconcile(t *testing.T, inv bootstrap.Inventory, store *memoryStore) ([]map[string]string, error) {
	t.Helper()
	var logged []map[string]string
	err := bootstrap.Reconcile(context.Background(), inv, store, func(event string, fields map[string]string) {
		entry := maps.Clone(fields)
		entry["event"] = event
		logged = append(logged, entry)
	})
	return logged, err
}

func TestAFreshInventoryIsCreatedOnceInDependencyOrder(t *testing.T) {
	store := newMemoryStore()
	if _, err := reconcile(t, inventory(), store); err != nil {
		t.Fatalf("first run: %v", err)
	}
	position := map[string]int{}
	for i, name := range store.created {
		position[name] = i
	}
	if position["output-ca"] > position["output-server"] || position["output-ca"] > position["output-client"] {
		t.Errorf("a leaf was created before its CA: %v", store.created)
	}
	if position["rings"] != len(store.created)-1 {
		t.Errorf("the ring was not created last: %v", store.created)
	}
	for name, secret := range store.secrets {
		if secret.Labels["app.kubernetes.io/managed-by"] != "concourse-hangar-bootstrap" {
			t.Errorf("%s carries no bootstrap label", name)
		}
	}

	first := store.snapshot()
	store.created = nil
	if _, err := reconcile(t, inventory(), store); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(store.created) != 0 {
		t.Errorf("the second run created %v", store.created)
	}
	if !equalSnapshots(first, store.snapshot()) {
		t.Error("the second run changed a Secret")
	}
}

func TestAnInterruptedRunIsCompletedWithoutReplacingAnything(t *testing.T) {
	store := newMemoryStore()
	store.failAfter = 1
	if _, err := reconcile(t, inventory(), store); err == nil {
		t.Fatal("the interrupted run reported success")
	}
	if len(store.created) != 1 {
		t.Fatalf("the interrupted run created %v, want exactly its first Secret", store.created)
	}
	firstName := store.created[0]
	firstData := maps.Clone(store.secrets[firstName].Data)

	store.failAfter = 0
	if _, err := reconcile(t, inventory(), store); err != nil {
		t.Fatalf("the re-run: %v", err)
	}
	if len(store.secrets) != len(inventory().Entries) {
		t.Errorf("the re-run left %d Secrets, want the whole inventory", len(store.secrets))
	}
	if !equalData(firstData, store.secrets[firstName].Data) {
		t.Errorf("the re-run replaced %s", firstName)
	}
}

func TestAMalformedSecretRefusesTheRunAndChangesNothing(t *testing.T) {
	foreignCA := func(t *testing.T) map[string][]byte {
		other := newMemoryStore()
		if _, err := reconcile(t, inventory(), other); err != nil {
			t.Fatal(err)
		}
		return other.secrets["output-server"].Data
	}
	for name, tc := range map[string]struct {
		secret string
		mutate func(t *testing.T, data map[string][]byte)
		reason string
		names  []string // replaces the entry's DNS names, when set
	}{
		"a short random key": {"hangar-key", func(_ *testing.T, d map[string][]byte) { d["hangar.key"] = d["hangar.key"][:16] }, "holds 16 bytes", nil},
		"a non-Ed25519 key": {"control-1", func(_ *testing.T, d map[string][]byte) {
			d["control.key"] = newKeyPEM()
		}, "not an Ed25519 key", nil},
		"a repeated token": {"store-tokens", func(_ *testing.T, d map[string][]byte) { d["publisher"] = d["input"] }, "repeats another principal's token", nil},
		"a short token":    {"store-tokens", func(_ *testing.T, d map[string][]byte) { d["inventory"] = []byte("short") }, "at least 32", nil},
		"server.json out of step": {"store-tokens", func(_ *testing.T, d map[string][]byte) {
			d["server.json"] = bytes.Replace(d["server.json"], d["reclaimer"], []byte(strings.Repeat("x", 64)), 1)
		}, "server.json's \"reclaimer\" token", nil},
		"a key that does not match its certificate": {"output-client", func(_ *testing.T, d map[string][]byte) {
			d["tls.key"] = newKeyPEM()
		}, "tls.key does not match tls.crt", nil},
		"a leaf from another CA": {"output-server", func(t *testing.T, d map[string][]byte) {
			foreign := foreignCA(t)
			d["tls.crt"], d["tls.key"] = foreign["tls.crt"], foreign["tls.key"]
		}, "does not chain to its CA", nil},
		"a server leaf without its name": {"store-tls", func(*testing.T, map[string][]byte) {}, "does not name", []string{"another.cicd.svc"}},
	} {
		t.Run(name, func(t *testing.T) {
			store := newMemoryStore()
			if _, err := reconcile(t, inventory(), store); err != nil {
				t.Fatal(err)
			}
			data := store.secrets[tc.secret].Data
			tc.mutate(t, data)
			inv := inventory()
			for i := range inv.Entries {
				if inv.Entries[i].Name == tc.secret && tc.names != nil {
					inv.Entries[i].DNSNames = tc.names
				}
			}
			// Remove one other Secret so a run that did not refuse would create it.
			delete(store.secrets, "materialize-key")
			before := store.snapshot()
			store.created = nil

			_, err := reconcile(t, inv, store)
			if !errors.Is(err, bootstrap.ErrRefused) || !strings.Contains(err.Error(), tc.reason) || !strings.Contains(err.Error(), tc.secret) {
				t.Fatalf("got %v, want a refusal naming %s and %q", err, tc.secret, tc.reason)
			}
			if len(store.created) != 0 || !equalSnapshots(before, store.snapshot()) {
				t.Errorf("a refused run wrote %v", store.created)
			}
		})
	}
}

func TestALeafWithoutItsCAIsRefused(t *testing.T) {
	store := newMemoryStore()
	if _, err := reconcile(t, inventory(), store); err != nil {
		t.Fatal(err)
	}
	delete(store.secrets, "output-ca")
	_, err := reconcile(t, inventory(), store)
	if !errors.Is(err, bootstrap.ErrRefused) || !strings.Contains(err.Error(), `exists without its CA "output-ca"`) {
		t.Fatalf("got %v, want a refusal naming the missing CA", err)
	}
}

func TestAnEarlierEpochsKeyIsReadAndNeverCreated(t *testing.T) {
	inv := inventory()
	inv.Entries = append(inv.Entries, bootstrap.Entry{Name: "control-0", Kind: bootstrap.KindEd25519, Key: "control.key",
		Ring: bootstrap.ControlRing, Epoch: 1, Required: true})
	for i := range inv.Entries {
		if inv.Entries[i].Name == "control-1" {
			inv.Entries[i].Epoch = 2
		}
		if inv.Entries[i].Kind == bootstrap.KindRing {
			inv.Entries[i].ActiveEpoch = 2
		}
	}
	store := newMemoryStore()
	_, err := reconcile(t, inv, store)
	if !errors.Is(err, bootstrap.ErrRefused) || !strings.Contains(err.Error(), `"control-0": is absent`) {
		t.Fatalf("got %v, want a refusal naming the absent earlier key", err)
	}
	if len(store.secrets) != 0 {
		t.Errorf("a refused run created %v", store.created)
	}
}

// The ring holds the control ring and nothing else: web mounts one file from it.
func TestTheRingComposesOnlyTheControlRing(t *testing.T) {
	store := newMemoryStore()
	if _, err := reconcile(t, inventory(), store); err != nil {
		t.Fatal(err)
	}
	files := slices.Sorted(maps.Keys(store.secrets["rings"].Data))
	if !slices.Equal(files, []string{"control-keys.json"}) {
		t.Errorf("the ring holds %v, want only control-keys.json", files)
	}
}

// A ring created before its composition shrank keeps the file it no longer
// composes. The bootstrap never changes a Secret, so the next run checks the
// files it composes and leaves the extra one alone rather than refusing.
func TestAnExistingRingWithAFileItNoLongerComposesIsKept(t *testing.T) {
	store := newMemoryStore()
	if _, err := reconcile(t, inventory(), store); err != nil {
		t.Fatal(err)
	}
	store.secrets["rings"].Data["retired-keys.json"] = []byte(`{"keys":[]}`)
	before := store.snapshot()
	store.created = nil
	if _, err := reconcile(t, inventory(), store); err != nil {
		t.Fatalf("an existing ring with an extra file was refused: %v", err)
	}
	if len(store.created) != 0 || !equalSnapshots(before, store.snapshot()) {
		t.Errorf("the run changed the existing ring: created %v", store.created)
	}
}

// The control ring is what web verifies node statements with; it must load
// through web's own loader after a second run.
func TestTheControlRingLoadsThroughWebsLoaderAfterASecondRun(t *testing.T) {
	store := newMemoryStore()
	if _, err := reconcile(t, inventory(), store); err != nil {
		t.Fatal(err)
	}
	if _, err := reconcile(t, inventory(), store); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	controlPath := filepath.Join(dir, "control-keys.json")
	if err := os.WriteFile(controlPath, store.secrets["rings"].Data["control-keys.json"], 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := hangaroutput.LoadControlKeyRing(controlPath); err != nil {
		t.Errorf("web's loader refused the control ring: %v", err)
	}
}

func TestNoPrivateValueReachesARingOrTheLog(t *testing.T) {
	store := newMemoryStore()
	logged, err := reconcile(t, inventory(), store)
	if err != nil {
		t.Fatal(err)
	}
	more, err := reconcile(t, inventory(), store)
	if err != nil {
		t.Fatal(err)
	}
	logged = append(logged, more...)

	var log strings.Builder
	for _, fields := range logged {
		for key, value := range fields {
			fmt.Fprintf(&log, "%s=%s\n", key, value)
		}
	}
	rings := string(store.secrets["rings"].Data["control-keys.json"])
	for name, secret := range store.secrets {
		if name == "rings" {
			continue
		}
		for key, value := range secret.Data {
			if key == "ca.crt" || key == "tls.crt" {
				continue // public certificates may be logged
			}
			for _, form := range []string{string(value), base64.StdEncoding.EncodeToString(value), hex.EncodeToString(value)} {
				if len(form) >= 16 && strings.Contains(log.String(), form) {
					t.Errorf("the log holds %s/%s", name, key)
				}
				if len(form) >= 16 && strings.Contains(rings, form) {
					t.Errorf("a ring holds %s/%s", name, key)
				}
			}
		}
	}
}

func parseEd25519(t *testing.T, keyPEM []byte) ed25519.PrivateKey {
	t.Helper()
	block, _ := pem.Decode(keyPEM)
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return key.(ed25519.PrivateKey)
}

// newKeyPEM returns a PKCS#8 key that is not Ed25519: a generated CA's key.
func newKeyPEM() []byte {
	store := newMemoryStore()
	_ = bootstrap.Reconcile(context.Background(), bootstrap.Inventory{Entries: []bootstrap.Entry{{Name: "ca", Kind: bootstrap.KindCA, CommonName: "x"}}}, store, nil)
	return store.secrets["ca"].Data["ca.key"]
}

func equalData(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if !bytes.Equal(value, b[key]) {
			return false
		}
	}
	return true
}

func equalSnapshots(a, b map[string]map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for name, data := range a {
		if !equalData(data, b[name]) {
			return false
		}
	}
	return true
}
