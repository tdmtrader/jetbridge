// Package bootstrap creates the Hangar key material a deployment needs and
// never changes it afterwards.
//
// The chart declares one bootstrap inventory. Reconcile validates every entry
// against what already exists, refuses with a named cause and writes nothing
// if any is wrong, and otherwise creates only the absent entries. It never
// updates, rotates or deletes a Secret: a regenerated key would break every
// publication receipt and every client already issued against the old one.
package bootstrap

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Kind is what an inventory entry holds.
type Kind string

const (
	// KindRandomKey is one 32-byte symmetric key under Entry.Key.
	KindRandomKey Kind = "random32"
	// KindEd25519 is one PKCS#8 PEM Ed25519 private key under Entry.Key whose
	// public half joins Entry.Ring of the ring entry.
	KindEd25519 Kind = "ed25519"
	// KindStoreTokens is the disk store's four principal tokens and the
	// server.json mapping them.
	KindStoreTokens Kind = "store-tokens"
	// KindCA is a certificate authority (ca.crt, ca.key) other entries'
	// leaves are issued from. It has no consumer; it exists so an interrupted
	// run can still issue the missing leaf.
	KindCA Kind = "ca"
	// KindTLSServer and KindTLSClient are leaves (tls.crt, tls.key, ca.crt)
	// issued from Entry.CA.
	KindTLSServer Kind = "tls-server"
	KindTLSClient Kind = "tls-client"
	// KindTLSBundle is a self-contained server pair and the CA that issued it,
	// written in one Secret (tls.crt, tls.key, ca.crt).
	KindTLSBundle Kind = "tls-bundle"
	// KindRing is the public verification rings composed from every
	// KindEd25519 entry: receipt-keys.json and control-keys.json.
	KindRing Kind = "ring"
	// KindDatabaseCredential is created by the database step, not here; the
	// sync-start reconcile only checks its place in the inventory.
	KindDatabaseCredential Kind = "dsn"
)

// RingName says which ring an Ed25519 key's public half joins.
type RingName string

const (
	ReceiptRing RingName = "receipt"
	ControlRing RingName = "control"
)

// Inventory is the bootstrap inventory the chart declares.
type Inventory struct {
	// Labels go on every Secret the bootstrap creates.
	Labels  map[string]string `json:"labels"`
	Entries []Entry           `json:"entries"`
}

// Entry is one Secret of the inventory. It describes material, not purpose:
// the chart's inventory define carries what each key is for.
type Entry struct {
	Name string `json:"name"`
	Kind Kind   `json:"kind"`

	// Key is the data key of a KindRandomKey or KindEd25519 entry.
	Key string `json:"key,omitempty"`

	// CA names the KindCA entry a leaf is issued from.
	CA string `json:"ca,omitempty"`
	// DNSNames a server leaf or bundle must carry.
	DNSNames []string `json:"dnsNames,omitempty"`
	// CommonName of a CA, leaf or bundle certificate.
	CommonName string `json:"commonName,omitempty"`

	// Ring, Epoch and KeyID place an Ed25519 key on a ring. KeyID is a
	// receipt key's id; a control key has none.
	Ring  RingName `json:"ring,omitempty"`
	Epoch int64    `json:"epoch,omitempty"`
	KeyID string   `json:"keyID,omitempty"`

	// Required marks an entry the bootstrap reads and never creates: an
	// earlier activation epoch's key, whose absence is a refusal.
	Required bool `json:"required,omitempty"`

	// ActiveEpoch and ActiveKeyID are a KindRing entry's active activation
	// epoch and receipt key id.
	ActiveEpoch int64  `json:"activeEpoch,omitempty"`
	ActiveKeyID string `json:"activeKeyID,omitempty"`
}

// Secret is the part of a Kubernetes Secret the bootstrap reads and writes.
type Secret struct {
	Name   string
	Type   string
	Labels map[string]string
	Data   map[string][]byte
}

const (
	secretTypeOpaque = "Opaque"
	secretTypeTLS    = "kubernetes.io/tls"
)

// SecretStore is the bootstrap's whole reach into the cluster: get a Secret by
// name, and create one. It cannot list, update or delete.
type SecretStore interface {
	Get(ctx context.Context, name string) (Secret, bool, error)
	Create(ctx context.Context, secret Secret) error
}

// Logger records what the bootstrap did. Fields carry names, kinds and public
// fingerprints only; no private value is ever passed to it.
type Logger func(event string, fields map[string]string)

// ErrRefused is wrapped by every refusal: an existing Secret is malformed or
// inconsistent, or a required one is absent. A refused run writes nothing.
var ErrRefused = errors.New("bootstrap refused")

// Reconcile validates the whole inventory, then creates the absent entries in
// dependency order: CAs, leaves and bundles, keys and tokens, the ring last.
// Re-running it after any partial run completes that run without replacing
// anything.
func Reconcile(ctx context.Context, inventory Inventory, store SecretStore, log Logger) error {
	if log == nil {
		log = func(string, map[string]string) {}
	}
	if err := inventory.validate(); err != nil {
		return fmt.Errorf("%w: the inventory: %v", ErrRefused, err)
	}

	existing := map[string]Secret{}
	for _, entry := range inventory.Entries {
		if entry.Kind == KindDatabaseCredential {
			continue
		}
		secret, found, err := store.Get(ctx, entry.Name)
		if err != nil {
			return fmt.Errorf("get Secret %q: %w", entry.Name, err)
		}
		if found {
			existing[entry.Name] = secret
		}
	}

	plan, err := planCreates(inventory, existing)
	if err != nil {
		return err
	}

	for _, entry := range plan.order {
		secret, fields, err := plan.generate(entry)
		if err != nil {
			return fmt.Errorf("generate %q: %w", entry.Name, err)
		}
		secret.Labels = inventory.Labels
		if err := store.Create(ctx, secret); err != nil {
			return fmt.Errorf("create Secret %q: %w", entry.Name, err)
		}
		fields["name"], fields["kind"] = entry.Name, string(entry.Kind)
		log("created", fields)
	}
	for _, entry := range inventory.Entries {
		if _, found := existing[entry.Name]; found {
			log("kept", map[string]string{"name": entry.Name, "kind": string(entry.Kind)})
		}
	}
	return nil
}

func (inventory Inventory) validate() error {
	names := map[string]Entry{}
	rings := 0
	for _, entry := range inventory.Entries {
		if entry.Name == "" {
			return errors.New("an entry has no name")
		}
		if _, dup := names[entry.Name]; dup {
			return fmt.Errorf("%q is declared twice", entry.Name)
		}
		names[entry.Name] = entry
		switch entry.Kind {
		case KindRandomKey:
			if entry.Key == "" {
				return fmt.Errorf("%q names no data key", entry.Name)
			}
		case KindEd25519:
			if entry.Key == "" || entry.Epoch <= 0 || (entry.Ring != ReceiptRing && entry.Ring != ControlRing) {
				return fmt.Errorf("%q needs a data key, an activation epoch and a ring", entry.Name)
			}
			if entry.Ring == ReceiptRing && entry.KeyID == "" {
				return fmt.Errorf("receipt key %q has no key id", entry.Name)
			}
		case KindRing:
			rings++
			if entry.ActiveEpoch <= 0 || entry.ActiveKeyID == "" {
				return fmt.Errorf("ring %q needs its active activation epoch and receipt key id", entry.Name)
			}
		case KindTLSServer, KindTLSClient:
			if entry.CA == "" {
				return fmt.Errorf("leaf %q names no CA", entry.Name)
			}
		case KindStoreTokens, KindCA, KindTLSBundle, KindDatabaseCredential:
		default:
			return fmt.Errorf("%q has unknown kind %q", entry.Name, entry.Kind)
		}
		if entry.Required && entry.Kind != KindEd25519 {
			return fmt.Errorf("%q is required but only an earlier activation epoch's key may be", entry.Name)
		}
	}
	for _, entry := range inventory.Entries {
		if entry.CA != "" && names[entry.CA].Kind != KindCA {
			return fmt.Errorf("leaf %q names CA %q, which is not a CA entry", entry.Name, entry.CA)
		}
	}
	if rings > 1 {
		return errors.New("more than one ring entry")
	}
	return nil
}

// createPlan is what a validated run will create, and the material it reads.
type createPlan struct {
	order   []Entry
	cas     map[string]certificateAuthority
	publics map[string]ed25519.PublicKey
	ring    *Entry
	entries []Entry
}

// planCreates validates every existing Secret and returns the absent entries
// in creation order. Any problem refuses the whole run before a write.
func planCreates(inventory Inventory, existing map[string]Secret) (*createPlan, error) {
	plan := &createPlan{cas: map[string]certificateAuthority{}, publics: map[string]ed25519.PublicKey{}, entries: inventory.Entries}
	var problems []string
	refuse := func(entry Entry, format string, args ...any) {
		problems = append(problems, fmt.Sprintf("%s %q: %s", entry.Kind, entry.Name, fmt.Sprintf(format, args...)))
	}

	for _, entry := range inventory.Entries {
		if entry.Kind != KindCA {
			continue
		}
		if secret, found := existing[entry.Name]; found {
			ca, err := loadCertificateAuthority(secret.Data["ca.crt"], secret.Data["ca.key"])
			if err != nil {
				refuse(entry, "%v", err)
				continue
			}
			plan.cas[entry.Name] = ca
		}
	}

	for _, entry := range inventory.Entries {
		secret, found := existing[entry.Name]
		switch {
		case entry.Kind == KindDatabaseCredential || entry.Kind == KindCA:
			continue
		case !found && entry.Required:
			refuse(entry, "is absent; an earlier activation epoch's key is never created again, because a new one would verify nothing that epoch signed")
			continue
		case !found:
			// An absent leaf is issued from its CA, which is either valid
			// above or created first in this run.
			continue
		}
		switch entry.Kind {
		case KindRandomKey:
			if err := validateRandomKey(secret.Data[entry.Key]); err != nil {
				refuse(entry, "key %q %v", entry.Key, err)
			}
		case KindEd25519:
			public, err := ed25519PublicHalf(secret.Data[entry.Key])
			if err != nil {
				refuse(entry, "key %q %v", entry.Key, err)
				continue
			}
			plan.publics[entry.Name] = public
		case KindStoreTokens:
			if err := validateStoreTokens(secret.Data); err != nil {
				refuse(entry, "%v", err)
			}
		case KindTLSServer, KindTLSClient:
			ca, ok := plan.cas[entry.CA]
			if !ok {
				refuse(entry, "exists without its CA %q; a new CA would orphan it", entry.CA)
				continue
			}
			usage := serverLeaf
			if entry.Kind == KindTLSClient {
				usage = clientLeaf
			}
			if err := validateLeaf(ca.cert, usage, secret.Data["tls.crt"], secret.Data["tls.key"], entry.DNSNames); err != nil {
				refuse(entry, "%v", err)
			} else if !bytes.Equal(secret.Data["ca.crt"], ca.certPEM) {
				refuse(entry, "ca.crt is not its CA's certificate")
			}
		case KindTLSBundle:
			ca, err := parseCertificate(secret.Data["ca.crt"])
			if err != nil {
				refuse(entry, "ca.crt %v", err)
				continue
			}
			if err := validateLeaf(ca, serverLeaf, secret.Data["tls.crt"], secret.Data["tls.key"], entry.DNSNames); err != nil {
				refuse(entry, "%v", err)
			}
		case KindRing:
			// Checked once every key's public half is known.
		}
	}

	for _, entry := range inventory.Entries {
		if entry.Kind == KindRing {
			e := entry
			plan.ring = &e
		}
	}
	if plan.ring != nil {
		if secret, found := existing[plan.ring.Name]; found {
			for _, entry := range inventory.Entries {
				if entry.Kind == KindEd25519 && plan.publics[entry.Name] == nil {
					refuse(*plan.ring, "exists while key %q is absent; the ring would no longer match its keys", entry.Name)
				}
			}
			if len(problems) == 0 {
				want, err := composeRing(*plan.ring, inventory.Entries, plan.publics)
				if err != nil {
					refuse(*plan.ring, "%v", err)
				} else {
					for key, body := range want {
						if !bytes.Equal(secret.Data[key], body) {
							refuse(*plan.ring, "%s does not match the public halves of its keys", key)
						}
					}
				}
			}
		}
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		return nil, fmt.Errorf("%w: %s", ErrRefused, strings.Join(problems, "; "))
	}

	rank := map[Kind]int{KindCA: 0, KindTLSBundle: 1, KindTLSServer: 1, KindTLSClient: 1, KindRandomKey: 2, KindEd25519: 2, KindStoreTokens: 2, KindRing: 3}
	for _, entry := range inventory.Entries {
		if _, found := existing[entry.Name]; found || entry.Kind == KindDatabaseCredential {
			continue
		}
		plan.order = append(plan.order, entry)
	}
	sort.SliceStable(plan.order, func(i, j int) bool { return rank[plan.order[i].Kind] < rank[plan.order[j].Kind] })
	return plan, nil
}

// generate makes one absent entry's Secret, recording what later entries need
// from it (a CA's key, a key's public half).
func (plan *createPlan) generate(entry Entry) (Secret, map[string]string, error) {
	fields := map[string]string{}
	secret := Secret{Name: entry.Name, Type: secretTypeOpaque, Data: map[string][]byte{}}
	switch entry.Kind {
	case KindCA:
		ca, err := newCertificateAuthority(entry.CommonName)
		if err != nil {
			return Secret{}, nil, err
		}
		plan.cas[entry.Name] = ca
		secret.Data["ca.crt"], secret.Data["ca.key"] = ca.certPEM, ca.keyPEM
		fields["certificate"] = Fingerprint(ca.cert.Raw)
	case KindTLSServer, KindTLSClient:
		ca := plan.cas[entry.CA]
		usage := serverLeaf
		if entry.Kind == KindTLSClient {
			usage = clientLeaf
		}
		certPEM, keyPEM, err := ca.issue(usage, entry.CommonName, entry.DNSNames)
		if err != nil {
			return Secret{}, nil, err
		}
		secret.Type = secretTypeTLS
		secret.Data["tls.crt"], secret.Data["tls.key"], secret.Data["ca.crt"] = certPEM, keyPEM, ca.certPEM
		fields["ca"] = entry.CA
	case KindTLSBundle:
		ca, err := newCertificateAuthority(entry.CommonName + " CA")
		if err != nil {
			return Secret{}, nil, err
		}
		certPEM, keyPEM, err := ca.issue(serverLeaf, entry.CommonName, entry.DNSNames)
		if err != nil {
			return Secret{}, nil, err
		}
		secret.Type = secretTypeTLS
		secret.Data["tls.crt"], secret.Data["tls.key"], secret.Data["ca.crt"] = certPEM, keyPEM, ca.certPEM
	case KindRandomKey:
		key, err := randomKey()
		if err != nil {
			return Secret{}, nil, err
		}
		secret.Data[entry.Key] = key
	case KindEd25519:
		keyPEM, public, err := newEd25519Key()
		if err != nil {
			return Secret{}, nil, err
		}
		plan.publics[entry.Name] = public
		secret.Data[entry.Key] = keyPEM
		fields["public"] = Fingerprint(public)
	case KindStoreTokens:
		data, err := newStoreTokens()
		if err != nil {
			return Secret{}, nil, err
		}
		secret.Data = data
	case KindRing:
		files, err := composeRing(entry, plan.entries, plan.publics)
		if err != nil {
			return Secret{}, nil, err
		}
		secret.Data = files
	default:
		return Secret{}, nil, fmt.Errorf("cannot generate kind %q", entry.Kind)
	}
	return secret, fields, nil
}

type receiptRingFile struct {
	ActiveKeyID     string             `json:"active_key_id"`
	ActivationEpoch int64              `json:"activation_epoch"`
	Keys            []receiptRingEntry `json:"keys"`
}

type receiptRingEntry struct {
	ID        string `json:"id"`
	Epoch     int64  `json:"epoch"`
	Retired   bool   `json:"retired"`
	PublicKey string `json:"public_key"`
}

type controlRingFile struct {
	ActivationEpoch int64              `json:"activation_epoch"`
	Keys            []controlRingEntry `json:"keys"`
}

type controlRingEntry struct {
	Epoch     int64  `json:"epoch"`
	PublicKey string `json:"public_key"`
}

// composeRing builds receipt-keys.json and control-keys.json from the public
// halves of the inventory's Ed25519 keys, ordered by activation epoch so the
// same keys always give the same bytes. Only public halves enter a ring.
func composeRing(ring Entry, entries []Entry, publics map[string]ed25519.PublicKey) (map[string][]byte, error) {
	receipt := receiptRingFile{ActiveKeyID: ring.ActiveKeyID, ActivationEpoch: ring.ActiveEpoch, Keys: []receiptRingEntry{}}
	control := controlRingFile{ActivationEpoch: ring.ActiveEpoch, Keys: []controlRingEntry{}}
	for _, entry := range entries {
		if entry.Kind != KindEd25519 {
			continue
		}
		public := publics[entry.Name]
		if public == nil {
			return nil, fmt.Errorf("key %q has no public half yet", entry.Name)
		}
		encoded := base64.StdEncoding.EncodeToString(public)
		switch entry.Ring {
		case ReceiptRing:
			receipt.Keys = append(receipt.Keys, receiptRingEntry{ID: entry.KeyID, Epoch: entry.Epoch, PublicKey: encoded})
		case ControlRing:
			control.Keys = append(control.Keys, controlRingEntry{Epoch: entry.Epoch, PublicKey: encoded})
		}
	}
	sort.Slice(receipt.Keys, func(i, j int) bool { return receipt.Keys[i].Epoch < receipt.Keys[j].Epoch })
	sort.Slice(control.Keys, func(i, j int) bool { return control.Keys[i].Epoch < control.Keys[j].Epoch })
	receiptBody, err := json.Marshal(receipt)
	if err != nil {
		return nil, err
	}
	controlBody, err := json.Marshal(control)
	if err != nil {
		return nil, err
	}
	return map[string][]byte{"receipt-keys.json": receiptBody, "control-keys.json": controlBody}, nil
}
