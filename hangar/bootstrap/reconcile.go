// Package bootstrap creates the Hangar key material a deployment needs and
// never changes it afterwards.
//
// The chart declares one bootstrap inventory. Reconcile validates every entry
// against what already exists, refuses with a named cause and writes nothing
// if any is wrong, and otherwise creates only the absent entries. It never
// updates, rotates or deletes a Secret: a regenerated key would break every
// capability and every client already issued against the old one.
package bootstrap

import (
	"bytes"
	"context"
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

	// Key is the data key of a KindRandomKey entry.
	Key string `json:"key,omitempty"`

	// CA names the KindCA entry a leaf is issued from.
	CA string `json:"ca,omitempty"`
	// DNSNames a server leaf or bundle must carry.
	DNSNames []string `json:"dnsNames,omitempty"`
	// CommonName of a CA, leaf or bundle certificate.
	CommonName string `json:"commonName,omitempty"`

	// Purposes says what each data key is for, and Consumers which components
	// mount the Secret. Reconcile does not act on them; they are the
	// inventory's record, checked against the chart's mounts by its tests.
	Purposes  map[string]string `json:"purposes,omitempty"`
	Consumers []string          `json:"consumers,omitempty"`
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
// dependency order: CAs first, then leaves and bundles, then keys and tokens.
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
		case KindTLSServer, KindTLSClient:
			if entry.CA == "" {
				return fmt.Errorf("leaf %q names no CA", entry.Name)
			}
		case KindStoreTokens, KindCA, KindTLSBundle:
		default:
			return fmt.Errorf("%q has unknown kind %q", entry.Name, entry.Kind)
		}
	}
	for _, entry := range inventory.Entries {
		if entry.CA != "" && names[entry.CA].Kind != KindCA {
			return fmt.Errorf("leaf %q names CA %q, which is not a CA entry", entry.Name, entry.CA)
		}
	}
	return nil
}

// createPlan is what a validated run will create, and the material it reads.
type createPlan struct {
	order []Entry
	cas   map[string]certificateAuthority
}

// planCreates validates every existing Secret and returns the absent entries
// in creation order. Any problem refuses the whole run before a write.
func planCreates(inventory Inventory, existing map[string]Secret) (*createPlan, error) {
	plan := &createPlan{cas: map[string]certificateAuthority{}}
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
		case entry.Kind == KindCA:
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
		}
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		return nil, fmt.Errorf("%w: %s", ErrRefused, strings.Join(problems, "; "))
	}

	rank := map[Kind]int{KindCA: 0, KindTLSBundle: 1, KindTLSServer: 1, KindTLSClient: 1, KindRandomKey: 2, KindStoreTokens: 2}
	for _, entry := range inventory.Entries {
		if _, found := existing[entry.Name]; found {
			continue
		}
		plan.order = append(plan.order, entry)
	}
	sort.SliceStable(plan.order, func(i, j int) bool { return rank[plan.order[i].Kind] < rank[plan.order[j].Kind] })
	return plan, nil
}

// generate makes one absent entry's Secret, recording what later entries need
// from it (a CA's key).
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
	case KindStoreTokens:
		data, err := newStoreTokens()
		if err != nil {
			return Secret{}, nil, err
		}
		secret.Data = data
	default:
		return Secret{}, nil, fmt.Errorf("cannot generate kind %q", entry.Kind)
	}
	return secret, fields, nil
}
