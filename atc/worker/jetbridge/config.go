package jetbridge

import (
	"fmt"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	// DefaultPodStartupTimeout is the default maximum time to wait for a
	// pod to reach Running state before failing the task.
	DefaultPodStartupTimeout = 5 * time.Minute

	// workerLabelKey is the Pod label used to identify Pods managed by a
	// particular Concourse K8s worker.
	workerLabelKey = "concourse.ci/worker"

	// typeLabelKey is the Pod label used to record the Concourse container
	// type (task, get, put, etc.).
	typeLabelKey = "concourse.ci/type"

	// handleLabelKey is the Pod label that stores the DB container handle
	// (UUID). With readable pod names, this label maps back to the DB row.
	handleLabelKey = "concourse.ci/handle"

	// CacheBasePath is the mount path inside pods where the cache PVC is
	// attached. Cache entries live in subdirectories keyed by volume handle.
	CacheBasePath = "/concourse/cache"

	// DefaultArtifactHelperImage is the container image used for init
	// containers and the artifact-helper sidecar. Only needs tar.
	DefaultArtifactHelperImage = "alpine:latest"

	// artifactHelperContainerName is the name of the sidecar container
	// that handles uploading step outputs to the artifact store PVC.
	artifactHelperContainerName = "artifact-helper"

	// artifactPVCVolumeName is the pod volume name for the artifact store PVC.
	artifactPVCVolumeName = "artifact-store"

	// ArtifactMountPath is the mount path inside init containers and the
	// artifact-helper sidecar where the artifact store PVC is attached.
	ArtifactMountPath = "/artifacts"
)

// ArtifactKey returns the canonical relative path on the artifact PVC
// for a given volume handle, e.g. "artifacts/<handle>.tar".
func ArtifactKey(handle string) string {
	return fmt.Sprintf("artifacts/%s.tar", handle)
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

	// CacheVolumeClaim is the name of a PersistentVolumeClaim to mount
	// into every pod at CacheBasePath. Cache entries are stored as
	// subdirectories keyed by volume handle. When empty, caching is
	// disabled and all volumes use emptyDir (the default behavior).
	CacheVolumeClaim string

	// ArtifactStoreClaim is the name of a PersistentVolumeClaim for durable
	// artifact and resource cache storage. In production, this PVC is backed
	// by GCS FUSE (via StorageClass). In local dev, it's backed by local disk.
	// When empty, artifact storage is disabled and existing SPDY streaming
	// is used (the default).
	ArtifactStoreClaim string

	// ArtifactHelperImage overrides DefaultArtifactHelperImage for init
	// containers and the artifact-helper sidecar.
	ArtifactHelperImage string

	// ImageRegistry configures a container image registry for custom resource
	// type images. When set, its SecretName is auto-added to imagePullSecrets
	// on every pod and its Prefix is used when resolving custom resource type
	// images. Nil means disabled.
	ImageRegistry *ImageRegistryConfig
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
