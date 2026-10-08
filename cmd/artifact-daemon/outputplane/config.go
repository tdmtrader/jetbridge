package outputplane

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/output"
)

// The output plane's configuration.
//
// It is bound onto the artifact daemon's flag set. The node name, the
// Kubernetes client and the listener's TLS are the artifact daemon's and are
// not repeated here; the control and steps directories are derived from its
// --storage-path. What is left is every output-plane fact, and the refusal to
// be configured into the cache's bucket.

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

	// The bucket this one may not be. It is configuration rather than
	// inference: a deployment that has a durable cache cannot be told "not
	// that one" unless it can say which it is.
	CacheBucket string

	// MaxContentBytes and MaxEntries bound one tree: the regular-file content
	// and the filesystem entries the canonicalizer admits, whether a capture
	// canonicalizes a sealed step directory or a managed read materializes a
	// published tree. They are the plane's, not the chart's arithmetic alone:
	// the scratch volume's ceiling is PublishConcurrency times this content
	// limit, and a canonicalizer with no limit of its own makes that ceiling a
	// figure nothing enforces.
	MaxContentBytes int64
	MaxEntries      int64

	// Key is the Hangar key: the one raw 32-byte secret every warrant the web
	// presents to this daemon is signed with -- a control warrant at an
	// execution or capture route, a read warrant at a read route. It is not a
	// flag: the daemon loads it once at startup, before the node is labelled,
	// and sets it here.
	Key []byte

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

	// The node-local surfaces. NodeName, ControlDir and StepsDir are set by
	// the artifact daemon from its own configuration.
	NodeName   string
	NodeUID    string
	ControlDir string
	StepsDir   string
	ScratchDir string

	OperationTimeout time.Duration

	// SealWait bounds one background capture job: a seal (the wait for the
	// producing Pod's containers to terminate and the canonicalization after
	// it) or a publish (the upload).
	SealWait time.Duration

	// PodTerminationsNamespace is the namespace task Pods run in. The daemon
	// reads Pods there and nowhere else.
	PodTerminationsNamespace string

	// Terminations answers whether a Pod's containers have all stopped. Nil
	// means the Kubernetes API, read for this node's own Pods; a test sets it.
	Terminations PodTerminations

	// PodTerminationsDir is the standalone answer to the same question, for a
	// daemon run with no Kubernetes API: a file named after a Pod UID in this
	// directory declares that Pod's containers terminated.
	PodTerminationsDir string
}

// BindFlags declares the output plane's flags on the artifact daemon's set.
//
// Every flag names an output-plane fact. The output plane is mounted when
// --execution-control is given; the Hangar key it verifies every warrant
// against is the daemon's --hangar-key, loaded by the daemon, not a flag here.
func BindFlags(flags *flag.FlagSet, config *Config) {
	flags.StringVar(&config.OutputStore, "output-store", output.StoreGCS,
		"Store profile for the output plane. Supported profiles: gcs and disk.")
	flags.StringVar(&config.OutputStoreID, "output-store-id", "", "Expected persistent disk storage identity.")
	flags.StringVar(&config.OutputTokenFile, "output-token-file", "", "Disk storage publisher credential file.")
	flags.StringVar(&config.OutputCACert, "output-ca-cert", "", "Disk storage CA certificate.")
	flags.StringVar(&config.OutputEndpoint, "output-endpoint", "",
		"Endpoint override for the output bucket's store. Empty means real GCS; set it for an emulator. It is a different flag from the artifact daemon's --durable-endpoint because it serves a different bucket under a different identity.")
	flags.StringVar(&config.OutputBucket, "output-bucket", "",
		"The dedicated output bucket. It holds every tree the plane publishes -- captured outputs and input publications alike -- and is never the durable cache bucket.")
	flags.StringVar(&config.OutputPrefix, "output-prefix", "",
		"Authenticated deployment key prefix inside the output bucket, so one bucket can serve several deployments. It is server configuration; no task or consumer can select or broaden it.")
	flags.StringVar(&config.OutputTenant, "output-tenant", "",
		"Authenticated deployment/tenant identity the opaque output scope is derived from. It is never rendered into an object key.")
	flags.StringVar(&config.CacheBucket, "cache-bucket", "",
		"The durable resource-cache bucket, named so that this daemon can refuse to be pointed at it. Empty means this deployment has none.")
	flags.DurationVar(&config.SealWait, "capture-seal-wait", time.Hour,
		"How long one background capture job may run: a seal (the wait for every container of the producing Pod to terminate, and the canonicalization after it) or a publish (the upload). Both are asynchronous -- the control plane polls them -- and one that runs out is started again by the next poll, inside the capture's own deadline. The seal never deletes a Pod to get there.")
	flags.StringVar(&config.PodTerminationsNamespace, "pod-terminations-namespace", "",
		"The namespace task Pods run in. A capture seal reads this node's Pods in it, and only in it, to see that every container of the producing Pod has terminated. Required when the output plane reads Pods from Kubernetes.")
	flags.StringVar(&config.PodTerminationsDir, "pod-terminations-dir", "",
		"Standalone operation only, with no Kubernetes API to read Pods from: a directory in which a file named after a Pod UID declares that every container of that Pod has terminated. A capture seal waits for it. Ignored when the daemon reads Pods from Kubernetes.")
	flags.IntVar(&config.PublishConcurrency, "publish-concurrency", 1,
		"How many trees may be canonicalized and spooled to scratch at once. The scratch volume's size limit must cover this many maximum-sized trees; the chart renders both from one pair of values and refuses a product that does not fit.")
	flags.StringVar(&config.NodeUID, "node-uid", "",
		"Explicit node UID for standalone operation. With --node-name, the UID is resolved from Kubernetes and an explicit mismatch is refused.")
	flags.StringVar(&config.ScratchDir, "output-scratch-dir", "",
		"Absolute scratch directory for canonicalization, outside the storage root.")
	flags.Int64Var(&config.MaxContentBytes, "output-max-content-bytes", 10<<30,
		"Maximum regular-file content admitted in one tree, whether a capture canonicalizes it or a managed read materializes it. The scratch volume's size limit must cover --publish-concurrency trees of this size.")
	flags.Int64Var(&config.MaxEntries, "output-max-entries", 100000,
		"Maximum filesystem entries admitted in one tree, whether a capture canonicalizes it or a managed read materializes it.")
	flags.DurationVar(&config.OperationTimeout, "output-timeout", output.DefaultOperationTimeout,
		"Per-operation timeout against the output bucket.")
}

// CaptureEnabled reports whether this daemon carries the durable-capture
// extension as well as the base exact-execution-control protocol.
//
// The bucket is the discriminator and not a separate boolean, because a boolean
// and a bucket can disagree: "output enabled, no bucket" has no honest reading,
// and a daemon that took both would have to pick one. A base-only daemon is a
// real deployment -- the sibling `exact_execution_control` track schedules onto
// exactly it, and withdrawing the capture extension has to be able to REACH it
// from a running plane without taking exact process control away.
func (config Config) CaptureEnabled() bool {
	return strings.TrimSpace(config.OutputBucket) != ""
}

// Validate refuses a configuration before anything is built.
//
// The bucket rules are delegated to output.DeriveNamespace rather than
// reimplemented here, so there is exactly one statement of how a bucket,
// prefix and tenant derive a namespace, and this binary cannot drift from it.
func (config Config) Validate() error {
	if config.OperationTimeout <= 0 {
		return fmt.Errorf("%w: --output-timeout must be positive", output.ErrIncomplete)
	}
	if len(config.Key) != hangar.WarrantKeyBytes {
		return fmt.Errorf("%w: the output plane needs the Hangar key (--hangar-key, exactly %d raw "+
			"bytes); no route can verify a warrant without it", output.ErrIncomplete, hangar.WarrantKeyBytes)
	}
	if config.PublishConcurrency < 1 {
		return fmt.Errorf("%w: --publish-concurrency must be at least 1; zero would admit no "+
			"capture at all", output.ErrIncomplete)
	}
	if config.MaxContentBytes <= 0 {
		return fmt.Errorf("%w: --output-max-content-bytes must be positive; a tree with no "+
			"content limit is a scratch volume with no ceiling", output.ErrIncomplete)
	}
	if config.MaxEntries <= 0 {
		return fmt.Errorf("%w: --output-max-entries must be positive", output.ErrIncomplete)
	}
	if err := config.validateCaptureExtension(); err != nil {
		return err
	}
	if !config.CaptureEnabled() {
		return nil
	}
	_, err := config.Namespace()

	return err
}

// validateCaptureExtension is the optional half's refusal, stated in one place: the
// capture extension's own values are REFUSED when it is off. A prefix or a
// tenant with no bucket is an operator who believes the plane is on.
func (config Config) validateCaptureExtension() error {
	if config.CaptureEnabled() {
		return nil
	}
	for _, set := range []struct{ flag, value string }{
		{"--output-prefix", config.OutputPrefix},
		{"--output-tenant", config.OutputTenant},
		{"--output-endpoint", config.OutputEndpoint},
		{"--output-store-id", config.OutputStoreID},
		{"--output-token-file", config.OutputTokenFile},
		{"--output-ca-cert", config.OutputCACert},
	} {
		if strings.TrimSpace(set.value) != "" {
			return fmt.Errorf("%w: %s is set and --output-bucket is not. This daemon "+
				"carries exact execution control only; a half-configured capture "+
				"extension is not a base-only daemon, it is a deployment that believes "+
				"it is publishing", output.ErrIncomplete, set.flag)
		}
	}

	return nil
}

// Namespace is the derived output namespace this daemon publishes into.
func (config Config) Namespace() (output.OutputNamespace, error) {
	return output.DeriveNamespace(output.NamespaceConfig{
		Store:            config.OutputStore,
		StoreID:          config.OutputStoreID,
		Bucket:           config.OutputBucket,
		DeploymentPrefix: config.OutputPrefix,
		TenantID:         config.OutputTenant,
		CacheBucket:      config.CacheBucket,
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
// a seal re-canonicalizes the held step directory rather than reading
// scratch. Only `hangar-tree-*` entries are removed, and deliberately so: a
// misconfigured --output-scratch-dir pointing at something shared must not turn a
// restart into a deletion of somebody else's data.
func (config Config) PrepareScratch() error {
	if strings.TrimSpace(config.ScratchDir) == "" {
		return fmt.Errorf("%w: --output-scratch-dir is required; canonicalization needs a trusted "+
			"temporary parent", output.ErrIncomplete)
	}
	if !filepath.IsAbs(config.ScratchDir) {
		return fmt.Errorf("%w: --output-scratch-dir %q is relative", output.ErrIncomplete, config.ScratchDir)
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
