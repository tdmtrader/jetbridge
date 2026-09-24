package main

import (
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/pem"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/output"
)

// The output daemon's configuration.
//
// This is a separate binary from cmd/artifact-daemon on purpose, and the reason
// is Kubernetes rather than Go. A service account is Pod-wide: adding an
// output-bucket role to the existing artifact daemon's Pod would give that
// daemon's cache and strict-input identity the same role, and there is no way
// to take it back at the process boundary. So the isolation has to be a second
// Pod with a second service account, which means a second binary -- and this
// file is where that binary refuses to be configured into the first one's
// buckets.

// Config is what the daemon was told, before anything is built from it.
type Config struct {
	// The output plane's own storage identity.
	OutputStore     string
	OutputStoreID   string
	OutputTokenFile string
	OutputCACert    string
	OutputEndpoint  string
	OutputBucket    string
	OutputPrefix    string
	OutputTenant    string

	// The two buckets this one may not be. They are configuration rather than
	// inference: a deployment that has a durable cache and a strict-input
	// bucket cannot be told "not those" unless it can say which they are.
	CacheBucket       string
	StrictInputBucket string

	// SharedBucketPrefixOnlyIsolation is an operator declaring that trust
	// domains are separated by key prefix inside one bucket. It is refused.
	SharedBucketPrefixOnlyIsolation bool

	// The receipt key material. The private half is mounted only here.
	ReceiptKeyID   string
	ReceiptKeyFile string

	// The output read-warrant key: a THIRD key, an exact 32-byte HMAC secret
	// under the hangar-output-materialize-v1 domain. It is neither the receipt
	// key (a read warrant must not be signable by anything that can mint a
	// publication receipt) nor the foundation's strict-input materialization
	// key (which attests inputs and belongs to the other daemon).
	MaterializationKeyID   string
	MaterializationKeyFile string

	// The node's control key: a SECOND Ed25519 key, for the statements the
	// execution and source ledgers make. It is separate from the receipt key
	// because the two say different things -- a receipt says an object exists
	// in a bucket, a control statement says a process on this node did
	// something -- and an activation epoch pins them separately, so rotating
	// one does not rotate the other.
	ControlKeyID   string
	ControlKeyFile string

	// PublishConcurrency bounds how many trees may be spooled to scratch at
	// once.
	//
	// It is here rather than left to whatever arrives because the scratch
	// volume is an emptyDir with a sizeLimit, and the bound an operator can
	// actually reason about is concurrency times the content limit. Without it
	// the volume's ceiling is "however many captures happened to land on this
	// node at once", and an emptyDir that exceeds its sizeLimit evicts the Pod;
	// one with no sizeLimit fills the node's disk and evicts every Pod on it.
	PublishConcurrency int

	// The node-local surfaces.
	Kubeconfig        string
	NodeName          string
	NodeUID           string
	ListenAddress     string
	ControlDir        string
	StepsDir          string
	ScratchDir        string
	CapabilityKeyFile string
	CapabilityTTL     time.Duration

	// The control listener's TLS material. Phase 3 listened on 127.0.0.1 with
	// no TLS, which was right while every caller was a pod on this node. The
	// ATC is not: it revalidates holds, takes writer tickets, records starts
	// and outcomes, seals and publishes, all from the web pod on another node,
	// and a bearer capability over plaintext off-node is interceptable inside
	// its TTL. Spelled the way cmd/artifact-daemon spells them, because the
	// ATC's client-certificate plumbing is the same plumbing.
	TLSCert   string
	TLSKey    string
	TLSCACert string

	ActivationEpoch  uint64
	OperationTimeout time.Duration
	ReadControlURL   string
}

// BindFlags declares the daemon's flags on a set.
//
// Every flag names an output-plane fact. There is deliberately no --durable-*
// flag and no strict-input flag of any kind: this process has no cache client
// and no strict-input client, and a flag that let an operator give it one would
// be the whole isolation undone by a helm value.
func BindFlags(flags *flag.FlagSet, config *Config) {
	flags.StringVar(&config.ReadControlURL, "read-control-url", "", "HTTPS control-plane base URL for validating and releasing managed-output read leases. Empty disables archive downloads.")
	flags.StringVar(&config.OutputStore, "output-store", output.StoreGCS,
		"Store profile for the output plane. Supported profiles: gcs and disk.")
	flags.StringVar(&config.OutputStoreID, "output-store-id", "", "Expected persistent disk storage identity.")
	flags.StringVar(&config.OutputTokenFile, "output-token-file", "", "Disk storage publisher credential file.")
	flags.StringVar(&config.OutputCACert, "output-ca-cert", "", "Disk storage CA certificate.")
	flags.StringVar(&config.OutputEndpoint, "output-endpoint", "",
		"Endpoint override for the output bucket's store. Empty means real GCS; set it for an emulator. It is a different flag from the artifact daemon's --durable-endpoint because it serves a different bucket under a different identity.")
	flags.StringVar(&config.OutputBucket, "output-bucket", "",
		"The dedicated output bucket. It contains only Hangar output-plane objects and is never the durable cache bucket or the caller-published strict-input bucket.")
	flags.StringVar(&config.OutputPrefix, "output-prefix", "",
		"Authenticated deployment key prefix inside the output bucket, so one bucket can serve several deployments. It is server configuration; no task, consumer or receipt can select or broaden it.")
	flags.StringVar(&config.OutputTenant, "output-tenant", "",
		"Authenticated deployment/tenant identity the opaque output scope is derived from. It is never rendered into an object key.")
	flags.StringVar(&config.CacheBucket, "cache-bucket", "",
		"The durable resource-cache bucket, named so that this daemon can refuse to be pointed at it. Empty means this deployment has none.")
	flags.StringVar(&config.StrictInputBucket, "strict-input-bucket", "",
		"The caller-published strict-input Hangar bucket, named so that this daemon can refuse to be pointed at it. Empty means this deployment has none.")
	flags.BoolVar(&config.SharedBucketPrefixOnlyIsolation, "shared-bucket-prefix-only-isolation", false,
		"Declare that trust domains are separated by key prefix inside one shared bucket. This is refused: object-level permission is not expressible in a bucket policy, so prefix-only IAM is not an activation-compatible substitute for a dedicated bucket.")
	flags.StringVar(&config.ReceiptKeyID, "receipt-key-id", "",
		"Identifier of the Ed25519 receipt signing key. A receipt names it so a verifier knows which activation-pinned public key can check it.")
	flags.StringVar(&config.ReceiptKeyFile, "receipt-key-file", "",
		"Path to the PKCS#8 PEM Ed25519 private key used to sign receipts. It is mounted only in this Pod: the control plane, the web node, the existing artifact daemon, the controllers, the control init container, the task and the sidecar hold the public key and the key id only.")
	flags.StringVar(&config.MaterializationKeyID, "materialization-key-id", "",
		"Identifier of the key output read warrants are minted and verified with. A warrant names it so a verifier knows which activation-pinned key can check it.")
	flags.StringVar(&config.MaterializationKeyFile, "materialization-key-file", "",
		"Path to the raw 32-byte key output read warrants are signed with, under the hangar-output-materialize-v1 domain. It is never the receipt key and never the foundation's strict-input materialization key.")
	flags.StringVar(&config.ControlKeyID, "control-key-id", "",
		"Identifier of the Ed25519 key this node signs execution and source ledger statements with. A control plane pins its public half per activation epoch.")
	flags.StringVar(&config.ControlKeyFile, "control-key-file", "",
		"Path to the PKCS#8 PEM Ed25519 private key used to sign ledger statements. It is a different key from the receipt key: rotating one must not rotate the other.")
	flags.IntVar(&config.PublishConcurrency, "publish-concurrency", 1,
		"How many trees may be canonicalized and spooled to scratch at once. The scratch volume's size limit must cover this many maximum-sized trees; the chart renders both from one pair of values and refuses a product that does not fit.")
	flags.StringVar(&config.Kubeconfig, "kubeconfig", "", "Kubernetes configuration file; empty uses in-cluster service-account authentication.")
	flags.StringVar(&config.NodeName, "node-name", "",
		"This node's Kubernetes name, from the Downward API. It is what the daemon patches its two ready labels onto. Empty means no labeling at all, which is how this binary runs in the conformance tier and in its own tests.")
	flags.StringVar(&config.NodeUID, "node-uid", "",
		"Explicit node UID for standalone operation. With --node-name, the UID is resolved from Kubernetes and an explicit mismatch is refused.")
	flags.StringVar(&config.ListenAddress, "listen", "127.0.0.1:0",
		"Address the control API listens on. It is node-local: nothing outside this node's pods speaks this API.")
	flags.StringVar(&config.ControlDir, "control-dir", "",
		"Parent of the private control directory holding the execution and source ledgers. It is inside the shared managed hostPath and excluded from Registry, alias persistence, steps traversal and the Sweeper.")
	flags.StringVar(&config.StepsDir, "steps-dir", "",
		"The managed steps directory holding source incarnations. The daemon opens it as an os.Root handle; no request ever names a path beneath it.")
	flags.StringVar(&config.ScratchDir, "scratch-dir", "",
		"Absolute scratch directory for canonicalization, outside the storage root.")
	flags.StringVar(&config.CapabilityKeyFile, "capability-key", "",
		"Path to the raw 32-byte key control capabilities are minted and verified with. It is shared with the control plane and with nothing else.")
	flags.DurationVar(&config.CapabilityTTL, "capability-ttl", 15*time.Minute,
		"Maximum accepted lifetime of a control capability. A capability is presented once, within one operation; an hour-long one is a credential.")
	flags.StringVar(&config.TLSCert, "tls-cert", "",
		"Path to the TLS server certificate for the control API. Setting all three TLS flags makes the listener HTTPS and requires a verified client certificate on every route but the node-local capture hold.")
	flags.StringVar(&config.TLSKey, "tls-key", "",
		"Path to the TLS server private key.")
	flags.StringVar(&config.TLSCACert, "tls-ca-cert", "",
		"Path to the CA certificate this daemon verifies control-plane client certificates against.")
	flags.Uint64Var(&config.ActivationEpoch, "activation-epoch", 0,
		"The active activation epoch this daemon publishes under. Rotation creates a new epoch rather than replacing a key in place.")
	flags.DurationVar(&config.OperationTimeout, "output-timeout", output.DefaultOperationTimeout,
		"Per-operation timeout against the output bucket.")
}

// OutputFacetEnabled reports whether this daemon carries the durable-capture
// extension as well as the base exact-execution-control protocol.
//
// The bucket is the discriminator and not a separate boolean, because a boolean
// and a bucket can disagree: "output enabled, no bucket" has no honest reading,
// and a daemon that took both would have to pick one. A base-only cohort is a
// real deployment -- the sibling `exact_execution_control` track schedules onto
// exactly it, and Req 58's output-only downgrade has to be able to REACH it
// from a running plane without taking exact process control away.
func (config Config) OutputFacetEnabled() bool {
	return strings.TrimSpace(config.OutputBucket) != ""
}

// Validate refuses a configuration before anything is built.
//
// The bucket rules are delegated to output.DeriveNamespace rather than
// reimplemented here, so there is exactly one statement of Req 20 and this
// binary cannot drift from it.
func (config Config) Validate() error {
	if config.OperationTimeout <= 0 {
		return fmt.Errorf("%w: --output-timeout must be positive", output.ErrIncomplete)
	}
	if strings.TrimSpace(config.ControlKeyID) == "" {
		return fmt.Errorf("%w: --control-key-id is required; a ledger statement names the key "+
			"that can check it", output.ErrIncomplete)
	}
	if strings.TrimSpace(config.ControlKeyFile) == "" {
		return fmt.Errorf("%w: --control-key-file is required; an unsigned acknowledgement is "+
			"not proof", output.ErrIncomplete)
	}
	if config.PublishConcurrency < 1 {
		return fmt.Errorf("%w: --publish-concurrency must be at least 1; zero would admit no "+
			"capture at all", output.ErrIncomplete)
	}
	if config.ActivationEpoch == 0 {
		return fmt.Errorf("%w: --activation-epoch is required; a stale or absent epoch "+
			"authorizes nothing, and zero is the absence", output.ErrIncomplete)
	}

	if err := config.validateOutputFacet(); err != nil {
		return err
	}
	if err := config.validateTLS(); err != nil {
		return err
	}
	if config.ReadControlURL != "" {
		u, err := url.Parse(config.ReadControlURL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !config.TLSEnabled() {
			return fmt.Errorf("%w: --read-control-url requires an HTTPS URL without credentials, query or fragment and a TLS-enabled daemon", output.ErrIncomplete)
		}
		if err := output.ValidateMaterializationTimeout(config.OperationTimeout); err != nil {
			return err
		}
	}
	if !config.OutputFacetEnabled() {
		return nil
	}

	_, err := config.Namespace()

	return err
}

// validateOutputFacet is the whole of the optional half, stated in one place.
//
// Two directions, and the second is the one that is easy to leave out: the
// facet's own values are required when it is ON, and REFUSED when it is off. A
// daemon configured with a receipt private key and no bucket is a process
// holding a signing key it can never need, which is a key an exploit of that
// process gets for free (Req 24); and a prefix or a tenant with no bucket is an
// operator who believes the plane is on.
func (config Config) validateOutputFacet() error {
	if !config.OutputFacetEnabled() {
		for _, set := range []struct{ flag, value string }{
			{"--read-control-url", config.ReadControlURL},
			{"--output-prefix", config.OutputPrefix},
			{"--output-tenant", config.OutputTenant},
			{"--output-endpoint", config.OutputEndpoint},
			{"--output-store-id", config.OutputStoreID},
			{"--output-token-file", config.OutputTokenFile},
			{"--output-ca-cert", config.OutputCACert},
			{"--receipt-key-id", config.ReceiptKeyID},
			{"--receipt-key-file", config.ReceiptKeyFile},
			{"--materialization-key-id", config.MaterializationKeyID},
			{"--materialization-key-file", config.MaterializationKeyFile},
		} {
			if strings.TrimSpace(set.value) != "" {
				return fmt.Errorf("%w: %s is set and --output-bucket is not. This daemon "+
					"carries the base exact-execution-control facet only; a half-configured "+
					"output facet is not a base-only daemon, it is a deployment that believes "+
					"it is publishing", output.ErrIncomplete, set.flag)
			}
		}

		return nil
	}

	for _, required := range []struct{ flag, value, why string }{
		{"--receipt-key-id", config.ReceiptKeyID,
			"a receipt names the key that can check it"},
		{"--receipt-key-file", config.ReceiptKeyFile,
			"this is the only process that holds the private half"},
		{"--materialization-key-id", config.MaterializationKeyID,
			"a read warrant names the key that can check it"},
		{"--materialization-key-file", config.MaterializationKeyFile,
			"the output read warrant uses its own key and its own domain, never the receipt key"},
	} {
		if strings.TrimSpace(required.value) == "" {
			return fmt.Errorf("%w: %s is required when --output-bucket is set; %s",
				output.ErrIncomplete, required.flag, required.why)
		}
	}

	// Three key roles, three files. They say different things, an activation
	// epoch pins them separately, and one file for two of them means rotating
	// either rotates both.
	for _, pair := range []struct{ left, right, leftFlag, rightFlag string }{
		{config.ControlKeyFile, config.ReceiptKeyFile, "--control-key-file", "--receipt-key-file"},
		{config.ControlKeyFile, config.MaterializationKeyFile, "--control-key-file", "--materialization-key-file"},
		{config.ReceiptKeyFile, config.MaterializationKeyFile, "--receipt-key-file", "--materialization-key-file"},
		{config.ReceiptKeyFile, config.CapabilityKeyFile, "--receipt-key-file", "--capability-key"},
		{config.MaterializationKeyFile, config.CapabilityKeyFile, "--materialization-key-file", "--capability-key"},
	} {
		if pair.left != "" && pair.left == pair.right {
			return fmt.Errorf("%w: %s and %s name the same key. They say different things and "+
				"an activation epoch pins them separately, so one key would mean rotating "+
				"either rotates both", output.ErrIncomplete, pair.leftFlag, pair.rightFlag)
		}
	}
	if config.ReceiptKeyID == config.MaterializationKeyID {
		return fmt.Errorf("%w: the receipt and materialization key ids are the same; a read "+
			"warrant must not be signable by anything that can mint a publication receipt",
			output.ErrIncomplete)
	}

	return nil
}

// RefuseCollidingKeyMaterial compares the key BYTES, not the paths.
//
// Validate above refuses five path pairs and two equal key ids, and every one
// of those comparisons is over NAMES: two flags pointing at symlinks to one
// file pass all of them, and so do two Secrets holding identical material. The
// separation the plan promises is a separation of authority -- a read warrant
// must not be signable by anything that can mint a publication receipt -- and
// authority follows the bytes.
//
// The comparison is constant-time. It compares secrets, and a comparison that
// returns early on the first differing byte is a comparison somebody can time.
// The refusal names the two flags and nothing about the material.
func (config Config) RefuseCollidingKeyMaterial() error {
	loaded := map[string][]byte{}
	for _, key := range []struct{ flag, file string }{
		{"--control-key-file", config.ControlKeyFile},
		{"--receipt-key-file", config.ReceiptKeyFile},
		{"--materialization-key-file", config.MaterializationKeyFile},
		{"--capability-key", config.CapabilityKeyFile},
	} {
		if strings.TrimSpace(key.file) == "" {
			continue
		}
		material, err := os.ReadFile(key.file)
		if err != nil {
			return fmt.Errorf("%w: reading the key named by %s: %v",
				output.ErrIncomplete, key.flag, err)
		}
		loaded[key.flag] = material
	}

	flags := make([]string, 0, len(loaded))
	for flag := range loaded {
		flags = append(flags, flag)
	}
	sort.Strings(flags)
	for i := range flags {
		for j := i + 1; j < len(flags); j++ {
			if subtle.ConstantTimeCompare(loaded[flags[i]], loaded[flags[j]]) == 1 {
				return fmt.Errorf("%w: %s and %s name different files holding the SAME key "+
					"material. They say different things and an activation epoch pins them "+
					"separately, so one key would mean rotating either rotates both -- and a "+
					"read warrant must not be signable by anything that can mint a publication "+
					"receipt", output.ErrIncomplete, flags[i], flags[j])
			}
		}
	}

	return nil
}

// TLSEnabled is the single predicate for "this daemon serves the control API
// over mTLS". All three files or none: a partial configuration has no honest
// reading, and silently falling back to plaintext would put the control plane's
// capabilities on the wire in the clear for an operator who asked for TLS.
func (config Config) TLSEnabled() bool {
	return config.TLSCert != "" && config.TLSKey != "" && config.TLSCACert != ""
}

func (config Config) validateTLS() error {
	var missing []string
	for _, flag := range []struct{ name, value string }{
		{"--tls-cert", config.TLSCert},
		{"--tls-key", config.TLSKey},
		{"--tls-ca-cert", config.TLSCACert},
	} {
		if strings.TrimSpace(flag.value) == "" {
			missing = append(missing, flag.name)
		}
	}
	if len(missing) == 0 || len(missing) == 3 {
		return nil
	}

	return fmt.Errorf("%w: the control API's TLS is partially configured; %s must also be set. "+
		"mTLS needs the server certificate, its key and the client CA together, and with only "+
		"part of them this daemon would listen in plaintext for a control plane that believes "+
		"it is dialling https", output.ErrIncomplete, strings.Join(missing, " and "))
}

// Namespace is the derived output namespace this daemon publishes into.
func (config Config) Namespace() (output.OutputNamespace, error) {
	return output.DeriveNamespace(output.NamespaceConfig{
		Store:                           config.OutputStore,
		StoreID:                         config.OutputStoreID,
		Bucket:                          config.OutputBucket,
		DeploymentPrefix:                config.OutputPrefix,
		TenantID:                        config.OutputTenant,
		CacheBucket:                     config.CacheBucket,
		StrictInputBucket:               config.StrictInputBucket,
		SharedBucketPrefixOnlyIsolation: config.SharedBucketPrefixOnlyIsolation,
		ActivationEpoch:                 activationEpoch(config.ActivationEpoch),
	})
}

// PrepareScratch creates the canonicalization scratch directory AND sweeps what
// a previous process left in it.
//
// It must be ABSOLUTE. The canonicalizer resolves it once and then works
// descriptor-relative beneath it, and a relative path would be resolved against
// whatever directory this process happened to start in -- which on a DaemonSet
// is not a thing anybody chose.
//
// The sweep is not tidiness. hangar.Canonicalizer spools a tree into
// `hangar-tree-*` beneath here and removes it in CapturedTree.Close, which both
// PublishSealedTree and CanonicalizeSealedTree defer -- and a SIGKILL between
// the two loses it. What is left is `canonical.tar`: the PLAINTEXT of a durable
// output, outliving its capture on the node with no record that it is there,
// and an emptyDir is per-Pod rather than per-container, so a crash-loop keeps
// every one of them. It also invalidates the arithmetic this file and the chart
// both reason from -- the scratch volume's ceiling is concurrency times the
// content limit, and an emptyDir that exceeds its sizeLimit evicts the Pod --
// because both assume the directory starts empty.
//
// It is safe by the plane's own rules: a restarted daemon owns no in-flight
// canonicalization, every capture is retried under its capture fence, and
// SealedIncarnation re-derives the tree from the held source rather than from
// scratch. Only `hangar-tree-*` entries are removed, and deliberately so: a
// misconfigured --scratch-dir pointing at something shared must not turn a
// restart into a deletion of somebody else's data.
func (config Config) PrepareScratch() error {
	if strings.TrimSpace(config.ScratchDir) == "" {
		return fmt.Errorf("%w: --scratch-dir is required; canonicalization needs a trusted "+
			"temporary parent", output.ErrIncomplete)
	}
	if !filepath.IsAbs(config.ScratchDir) {
		return fmt.Errorf("%w: --scratch-dir %q is relative", output.ErrIncomplete, config.ScratchDir)
	}
	if err := os.MkdirAll(config.ScratchDir, 0o700); err != nil {
		return fmt.Errorf("%w: creating the canonicalization scratch directory: %v",
			output.ErrInfrastructure, err)
	}

	entries, err := os.ReadDir(config.ScratchDir)
	if err != nil {
		return fmt.Errorf("%w: reading the canonicalization scratch directory: %v",
			output.ErrInfrastructure, err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), hangar.CanonicalizerTempPrefix) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(config.ScratchDir, entry.Name())); err != nil {
			return fmt.Errorf("%w: removing %s, which a killed canonicalization left in the "+
				"scratch volume: %v", output.ErrInfrastructure, entry.Name(), err)
		}
	}

	return nil
}

// LoadControlKey reads the node's control signing key off disk.
func (config Config) LoadControlKey() (ed25519.PrivateKey, error) {
	return loadEd25519(config.ControlKeyFile, "control")
}

// LoadReceiptKey reads the private key off disk.
//
// It refuses anything that is not an Ed25519 private key, including an RSA key
// that would otherwise parse: the receipt algorithm is closed, and a daemon
// that silently accepted another algorithm would produce receipts no verifier
// in this cohort can check.
func (config Config) LoadReceiptKey() (ed25519.PrivateKey, error) {
	return loadEd25519(config.ReceiptKeyFile, "receipt")
}

func loadEd25519(file, what string) (ed25519.PrivateKey, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("%w: reading the %s signing key: %v", output.ErrIncomplete, what, err)
	}

	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("%w: %s is not PEM", output.ErrCorrupt, file)
	}

	return parsePKCS8Ed25519(block.Bytes)
}
