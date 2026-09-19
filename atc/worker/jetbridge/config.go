package jetbridge

import (
	"fmt"
	"strings"
	"time"

	"github.com/concourse/concourse/hangar"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	// DefaultArtifactResolveCapabilityTTL covers bounded pod admission plus the
	// init container's retry budget.
	DefaultArtifactResolveCapabilityTTL = 2 * time.Hour

	// ArtifactResolveInitRetryBudget matches ten wget attempts at 180 seconds
	// plus the backoff between them.
	ArtifactResolveInitRetryBudget = 31 * time.Minute

	artifactResolveExpirySafetyMargin = 5 * time.Minute

	// DefaultPodStartupTimeout is the default maximum time to wait for a
	// pod to reach Running state before failing the task.
	DefaultPodStartupTimeout = 5 * time.Minute

	// DefaultPodSchedulingTimeout is the default maximum time to wait for
	// an Unschedulable pod to be scheduled before failing the task.
	DefaultPodSchedulingTimeout = 15 * time.Minute

	// workerLabelKey is the Pod label used to identify Pods managed by a
	// particular Concourse K8s worker.
	workerLabelKey = "concourse.ci/worker"

	// typeLabelKey is the Pod label used to record the Concourse container
	// type (task, get, put, etc.).
	typeLabelKey = "concourse.ci/type"

	// handleLabelKey is the Pod label that stores the DB container handle
	// (UUID). With readable pod names, this label maps back to the DB row.
	handleLabelKey = "concourse.ci/handle"

	// CacheBasePath is the mount path inside pods where task caches are
	// attached. Cache entries live in subdirectories keyed by volume handle.
	// The volume behind it is node-local or an emptyDir -- see CacheStore.
	CacheBasePath = "/concourse/cache"

	// DefaultArtifactHelperImage is the container image used for init
	// containers that fetch artifacts from the DaemonSet. Only needs tar.
	DefaultArtifactHelperImage = "alpine:latest"

	// ArtifactMountPath is the mount path inside init containers where
	// the artifact hostPath volume is attached.
	ArtifactMountPath = "/artifacts"
)

// ArtifactKey returns the artifact key for a given volume handle. The key
// is the handle itself (identity function). Kept for readability and
// greppability — callers use ArtifactKey(h) instead of bare h so artifact
// key construction sites are easy to find.
func ArtifactKey(handle string) string {
	return handle
}

// DefaultResourceTypeImages maps base Concourse resource type names to their
// Docker image references. These are the official Concourse resource type
// images used when no custom resource type is defined in the pipeline.
var DefaultResourceTypeImages = map[string]string{
	"time":           "concourse/time-resource",
	"registry-image": "concourse/registry-image-resource",
	"git":            "concourse/git-resource",
	"s3":             "concourse/s3-resource",
	"docker-image":   "concourse/docker-image-resource",
	"pool":           "concourse/pool-resource",
	"semver":         "concourse/semver-resource",
	"mock":           "concourse/mock-resource",
}

// MergeResourceTypeImages returns a new map that starts with a copy of
// DefaultResourceTypeImages and applies operator overrides on top. Each
// override entry is "name=image" (e.g. "git=my-registry/git-resource:v2").
// Entries without an "=" separator are silently skipped.
func MergeResourceTypeImages(overrides []string) map[string]string {
	merged := make(map[string]string, len(DefaultResourceTypeImages))
	for k, v := range DefaultResourceTypeImages {
		merged[k] = v
	}
	for _, entry := range overrides {
		name, image, ok := strings.Cut(entry, "=")
		if !ok || name == "" || image == "" {
			continue
		}
		merged[name] = image
	}
	return merged
}

// CacheStore values for --kubernetes-cache-store. Controls which backend
// is used for task caches.
const (
	// CacheStoreHostPath stores caches as directories on the node filesystem,
	// surviving pod restarts on the same node.
	CacheStoreHostPath = "hostpath"

	// CacheStoreEmptyDir uses ephemeral emptyDir volumes. Caches are lost
	// on pod termination.
	CacheStoreEmptyDir = "emptydir"
)

// ValidCacheStores is the set of valid --kubernetes-cache-store values.
var ValidCacheStores = map[string]bool{
	CacheStoreHostPath: true,
	CacheStoreEmptyDir: true,
}

// Config holds the configuration for connecting to a Kubernetes cluster
// and running Concourse tasks as K8s Jobs.
type Config struct {
	// Namespace is the Kubernetes namespace in which to create Jobs and Pods.
	Namespace string

	// KubeconfigPath is the path to a kubeconfig file. If empty, in-cluster
	// configuration is attempted.
	KubeconfigPath string

	// PodStartupTimeout is the maximum time to wait for a pod to reach
	// Running state. If zero, DefaultPodStartupTimeout is used.
	PodStartupTimeout time.Duration

	// PodSchedulingTimeout is the maximum time to wait for an Unschedulable
	// pod to be scheduled. If zero, DefaultPodSchedulingTimeout is used.
	PodSchedulingTimeout time.Duration

	// ResourceTypeImages maps base resource type names (e.g. "time", "git")
	// to Docker image references. When the ATC requests a container for a
	// base resource type, this mapping is used to resolve the image name
	// for the K8s pod. If nil, DefaultResourceTypeImages is used.
	ResourceTypeImages map[string]string

	// ImagePullSecrets is a list of Kubernetes Secret names to use as
	// imagePullSecrets on every created pod. These secrets must exist in
	// the configured namespace.
	ImagePullSecrets []string

	// ServiceAccount is the Kubernetes ServiceAccount name to set on
	// created pods. If empty, the namespace's default SA is used.
	ServiceAccount string

	// CacheStore selects the task cache backend explicitly. Valid values:
	// "hostpath" (node-local directories), "emptydir" (ephemeral).
	// When empty, the backend is auto-selected based on which config
	// fields are set (hostpath > emptydir).
	CacheStore string

	// CacheHostPath is the base directory on the node filesystem for
	// persistent task caches. Each cache gets a subdirectory keyed by
	// (jobID, stepName, path). Data survives pod restarts on the same
	// node. When empty, caches fall back to emptyDir (ephemeral).
	CacheHostPath string

	// ArtifactHelperImage overrides DefaultArtifactHelperImage for init
	// containers that fetch artifacts from the DaemonSet.
	ArtifactHelperImage string

	// ImageRegistry configures a container image registry for custom resource
	// type images. When set, its SecretName is auto-added to imagePullSecrets
	// on every pod and its Prefix is used when resolving custom resource type
	// images. Nil means disabled.
	ImageRegistry *ImageRegistryConfig

	// ArtifactDaemonPort is the HTTP port for the DaemonSet artifact server.
	ArtifactDaemonPort int

	// ArtifactDaemonWarmTimeout bounds a durable-tier restore, which pulls a
	// whole resource cache out of object storage and so is legitimately much
	// slower than a probe. Zero means the default.
	//
	// It must stay well under the daemon's own artifact TTL: a restore lands in
	// a temporary directory under steps/, and the sweeper is what reclaims one
	// left behind by a crash.
	ArtifactDaemonWarmTimeout time.Duration

	// ArtifactDaemonHostPath is the hostPath directory for artifact storage
	// on each node when using the DaemonSet backend.
	ArtifactDaemonHostPath string

	// ArtifactDaemonNamespace is the namespace the artifact daemon runs in,
	// when that differs from the namespace this config schedules pods into.
	// It only affects which SAN the daemon's server certificate is verified
	// against; empty means the daemon shares Namespace.
	ArtifactDaemonNamespace string

	// ArtifactDaemonService is the headless Service name for per-pod DNS
	// resolution of the DaemonSet pods.
	ArtifactDaemonService string

	// ArtifactDaemonTLSCert is the path to the client certificate for mTLS
	// connections to the artifact daemon.
	ArtifactDaemonTLSCert string

	// ArtifactDaemonTLSKey is the path to the client private key for mTLS
	// connections to the artifact daemon.
	ArtifactDaemonTLSKey string

	// ArtifactDaemonTLSCACert is the path to the CA certificate for verifying
	// the artifact daemon's server certificate.
	ArtifactDaemonTLSCACert string

	// ArtifactDaemonResolveCapabilityKey is the raw 32-byte key the ATC signs
	// resolve capabilities with. The daemon verifies with the same key. Empty
	// disables signing, and the daemon then accepts unsigned resolves.
	ArtifactDaemonResolveCapabilityKey []byte

	// ArtifactDaemonResolveCapabilityTTL is how long a signed capability stays
	// valid. Must exceed MinimumArtifactResolveCapabilityTTL or a slow-to-start
	// pod gets a 403 on a legitimate request.
	ArtifactDaemonResolveCapabilityTTL time.Duration

	// ArtifactDaemonTLSEnabled indicates whether TLS is enabled for daemon
	// communication. Derived from the presence of TLS cert/key/CA paths.
	ArtifactDaemonTLSEnabled bool

	// OutputPlaneEnabled turns on the durable output-capture extension: the
	// capture control init, the ledger-checked cleanup probe, the output
	// cohort's ready labels and the ATC's control calls. Off, every one of
	// those is absent and an ordinary pod is byte-identical to the one this
	// runtime built before the output plane existed (Req 59).
	OutputPlaneEnabled bool

	// OutputDaemonPort is the control port of the node-local output daemon.
	// It is a different daemon from the artifact daemon on a different port,
	// because Req 20 forbids the two sharing a bucket and a Kubernetes service
	// account is Pod-wide, so the isolation is a second Pod.
	OutputDaemonPort int

	// OutputDaemonTLSCert, OutputDaemonTLSKey and OutputDaemonTLSCACert are
	// the OUTPUT plane's client credential and trust root, and they are not
	// the artifact daemon's.
	//
	// The two daemons are separate processes under separate identities on
	// separate buckets, and they are separate trust domains for the same
	// reason. The output daemon's control routes refuse any operation whose
	// request carries no verified peer certificate, and its ClientCAs pool is
	// loaded from its own Secret, so a certificate issued by the artifact
	// daemon's CA handshakes and is then refused by every route.
	OutputDaemonTLSCert   string
	OutputDaemonTLSKey    string
	OutputDaemonTLSCACert string

	// OutputDaemonTLSServerName is the DNS name the output daemon's server
	// certificate carries, and the name the ATC verifies it against.
	//
	// The daemon is reached at `<node InternalIP>:<port>` and has no Service.
	// A node IP cannot be a SAN in a certificate issued before that node
	// existed, so verification is against a name the operator puts in the
	// certificate and the chart hands to both halves. Empty falls back to the
	// dial host, which for a node IP means the handshake fails -- loudly,
	// which is the right failure for material that does not match.
	OutputDaemonTLSServerName string

	// OutputActivationEpoch is the epoch this control plane speaks for.
	//
	// It is what makes Req 57 enforceable at the worker: a node label is a
	// scheduling HINT, and a cohort can carry a ready label while its daemons
	// speak for a different epoch -- a rolling upgrade, a half-finished
	// rotation, a node that came back from a long drain. Every capture records
	// the epoch it was admitted under, so a spec whose epoch is not this one
	// was admitted by a control plane this worker is not part of, and a stale
	// label or handshake authorizes nothing.
	//
	// Zero means unconfigured, and an unconfigured epoch checks nothing: the
	// conformance tier and this package's own specs run with no activation row
	// at all, and refusing there would be the chart's rule enforced in the
	// wrong process.
	OutputActivationEpoch int64

	// HangarEnabled permits exact immutable Hangar tree inputs.
	HangarEnabled bool

	// HangarWarrantSigner mints short-lived warrants bound to an exact tree,
	// container handle, and input volume. The raw signing key is never passed
	// to task pods.
	HangarWarrantSigner *hangar.WarrantSigner
}

// ImageRegistryConfig holds configuration for a container image registry
// used for custom resource type images in production K8s environments.
type ImageRegistryConfig struct {
	// Prefix is the registry path prefix (e.g. "gcr.io/my-project/concourse").
	// Custom resource type images are resolved as "<Prefix>/<type-name>".
	Prefix string

	// SecretName is the name of a K8s Secret (type kubernetes.io/dockerconfigjson)
	// to use as an imagePullSecret on every created pod. Must exist in the
	// configured namespace. Empty means no registry auth.
	SecretName string
}

// NewConfig creates a Config with the given namespace and kubeconfig path.
// If namespace is empty, it defaults to "default".
func NewConfig(namespace, kubeconfigPath string) Config {
	if namespace == "" {
		namespace = "default"
	}
	return Config{
		Namespace:         namespace,
		KubeconfigPath:    kubeconfigPath,
		PodStartupTimeout: DefaultPodStartupTimeout,
	}
}

// NewClientset creates a Kubernetes clientset from the Config. If
// KubeconfigPath is set, it builds the client from that file. Otherwise, it
// attempts in-cluster configuration.
func NewClientset(cfg Config) (kubernetes.Interface, error) {
	restConfig, err := RestConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("building k8s rest config: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("creating k8s clientset: %w", err)
	}

	return clientset, nil
}

// RestConfig returns the *rest.Config for the given Config. This is exported
// so callers (e.g. the PodExecutor) can use it alongside the clientset.
func RestConfig(cfg Config) (*rest.Config, error) {
	if cfg.KubeconfigPath != "" {
		return clientcmd.BuildConfigFromFlags("", cfg.KubeconfigPath)
	}
	return rest.InClusterConfig()
}

// MinimumArtifactResolveCapabilityTTL is the shortest capability lifetime that
// cannot expire before a legitimate resolve gets to run.
//
// A capability is signed when the pod SPEC is built, and verified when the init
// container finally executes. Between those, the pod may wait to be scheduled,
// then wait to start, then retry the fetch. A lifetime shorter than that sum
// makes a busy node look like an authorization failure: the request is
// perfectly legitimate and gets a 403.
func MinimumArtifactResolveCapabilityTTL(schedulingTimeout, startupTimeout time.Duration) (time.Duration, error) {
	if schedulingTimeout < 0 || startupTimeout < 0 {
		return 0, fmt.Errorf("pod scheduling and startup timeouts must not be negative")
	}
	minimum := schedulingTimeout + startupTimeout
	if minimum < schedulingTimeout {
		return 0, fmt.Errorf("artifact resolve capability lifetime bound overflows")
	}
	minimum += ArtifactResolveInitRetryBudget
	if minimum < ArtifactResolveInitRetryBudget {
		return 0, fmt.Errorf("artifact resolve capability lifetime bound overflows")
	}
	minimum += artifactResolveExpirySafetyMargin
	if minimum < artifactResolveExpirySafetyMargin {
		return 0, fmt.Errorf("artifact resolve capability lifetime bound overflows")
	}
	return minimum, nil
}
