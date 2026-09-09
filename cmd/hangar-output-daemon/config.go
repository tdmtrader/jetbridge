package main

import (
	"crypto/ed25519"
	"encoding/pem"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

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
	OutputStore    string
	OutputEndpoint string
	OutputBucket   string
	OutputPrefix   string
	OutputTenant   string

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

	// The node's control key: a SECOND Ed25519 key, for the statements the
	// execution and source ledgers make. It is separate from the receipt key
	// because the two say different things -- a receipt says an object exists
	// in a bucket, a control statement says a process on this node did
	// something -- and an activation epoch pins them separately, so rotating
	// one does not rotate the other.
	ControlKeyID   string
	ControlKeyFile string

	// The node-local surfaces.
	NodeUID           string
	ListenAddress     string
	ControlDir        string
	StepsDir          string
	ScratchDir        string
	CapabilityKeyFile string
	CapabilityTTL     time.Duration

	ActivationEpoch  uint64
	OperationTimeout time.Duration
}

// BindFlags declares the daemon's flags on a set.
//
// Every flag names an output-plane fact. There is deliberately no --durable-*
// flag and no strict-input flag of any kind: this process has no cache client
// and no strict-input client, and a flag that let an operator give it one would
// be the whole isolation undone by a helm value.
func BindFlags(flags *flag.FlagSet, config *Config) {
	flags.StringVar(&config.OutputStore, "output-store", output.StoreGCS,
		"Store profile for the output plane. Only \"gcs\" is admissible: the strict native-GCS profile is the one that offers create-if-absent at an exact generation, and a store that cannot refuse an overwrite cannot make the collision guarantee.")
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
	flags.StringVar(&config.ControlKeyID, "control-key-id", "",
		"Identifier of the Ed25519 key this node signs execution and source ledger statements with. A control plane pins its public half per activation epoch.")
	flags.StringVar(&config.ControlKeyFile, "control-key-file", "",
		"Path to the PKCS#8 PEM Ed25519 private key used to sign ledger statements. It is a different key from the receipt key: rotating one must not rotate the other.")
	flags.StringVar(&config.NodeUID, "node-uid", "",
		"This node's Kubernetes UID, from the Downward API. It is the UID and not the name: a name can be reused for new hardware, and a ledger sequence is only meaningful alongside the node that issued it.")
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
	flags.Uint64Var(&config.ActivationEpoch, "activation-epoch", 0,
		"The active activation epoch this daemon publishes under. Rotation creates a new epoch rather than replacing a key in place.")
	flags.DurationVar(&config.OperationTimeout, "output-timeout", time.Minute,
		"Per-operation timeout against the output bucket.")
}

// Validate refuses a configuration before anything is built.
//
// The bucket rules are delegated to output.DeriveNamespace rather than
// reimplemented here, so there is exactly one statement of Req 20 and this
// binary cannot drift from it.
func (config Config) Validate() error {
	if strings.TrimSpace(config.ReceiptKeyID) == "" {
		return fmt.Errorf("%w: --receipt-key-id is required; a receipt names the key that can "+
			"check it", output.ErrIncomplete)
	}
	if strings.TrimSpace(config.ReceiptKeyFile) == "" {
		return fmt.Errorf("%w: --receipt-key-file is required; this is the only process that "+
			"holds the private half", output.ErrIncomplete)
	}
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
	if config.ControlKeyFile == config.ReceiptKeyFile {
		return fmt.Errorf("%w: --control-key-file and --receipt-key-file name the same key. They "+
			"say different things and an activation epoch pins them separately, so one key would "+
			"mean rotating either rotates both", output.ErrIncomplete)
	}

	_, err := config.Namespace()

	return err
}

// Namespace is the derived output namespace this daemon publishes into.
func (config Config) Namespace() (output.OutputNamespace, error) {
	return output.DeriveNamespace(output.NamespaceConfig{
		Store:                           config.OutputStore,
		Bucket:                          config.OutputBucket,
		DeploymentPrefix:                config.OutputPrefix,
		TenantID:                        config.OutputTenant,
		CacheBucket:                     config.CacheBucket,
		StrictInputBucket:               config.StrictInputBucket,
		SharedBucketPrefixOnlyIsolation: config.SharedBucketPrefixOnlyIsolation,
		ActivationEpoch:                 activationEpoch(config.ActivationEpoch),
	})
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
