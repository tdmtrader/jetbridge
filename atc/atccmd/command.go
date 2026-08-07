package atccmd

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	_ "net/http/pprof"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"code.cloudfoundry.org/clock"
	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/concourse/concourse"
	experimentsapi "github.com/concourse/concourse/agent/api/experiments"
	noderunsapi "github.com/concourse/concourse/agent/api/noderuns"
	nodeupgradesapi "github.com/concourse/concourse/agent/api/nodeupgrades"
	snapshotsapi "github.com/concourse/concourse/agent/api/snapshots"
	ticketjournalapi "github.com/concourse/concourse/agent/api/ticketjournal"
	workflowoutcomesapi "github.com/concourse/concourse/agent/api/workflowoutcomes"
	workflowoverviewapi "github.com/concourse/concourse/agent/api/workflowoverview"
	workflowrunsapi "github.com/concourse/concourse/agent/api/workflowruns"
	workflowwaitsapi "github.com/concourse/concourse/agent/api/workflowwaits"
	"github.com/concourse/concourse/agent/artifactcap"
	"github.com/concourse/concourse/agent/budget"
	"github.com/concourse/concourse/agent/credentials"
	"github.com/concourse/concourse/agent/dispatch"
	"github.com/concourse/concourse/agent/projection"
	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/resourcecapture"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/agent/workflowrun"
	"github.com/concourse/concourse/agent/workflowrun/occurrence"
	"github.com/concourse/concourse/agent/workflowwait"
	"github.com/concourse/concourse/agent/workitem"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/agentchildexecutions"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/api/buildserver"
	"github.com/concourse/concourse/atc/api/containerserver"
	"github.com/concourse/concourse/atc/api/pipelineserver"
	"github.com/concourse/concourse/atc/api/policychecker"
	"github.com/concourse/concourse/atc/auditor"
	"github.com/concourse/concourse/atc/builds"
	"github.com/concourse/concourse/atc/component"
	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/creds"
	"github.com/concourse/concourse/atc/creds/noop"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/encryption"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/db/migration"
	"github.com/concourse/concourse/atc/engine"
	"github.com/concourse/concourse/atc/gc"
	"github.com/concourse/concourse/atc/imageresolver"
	"github.com/concourse/concourse/atc/lidar"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/pauser"
	"github.com/concourse/concourse/atc/policy"
	"github.com/concourse/concourse/atc/runlifecycle"
	"github.com/concourse/concourse/atc/scheduler"
	"github.com/concourse/concourse/atc/scheduler/algorithm"
	"github.com/concourse/concourse/atc/syslog"
	"github.com/concourse/concourse/atc/util"
	"github.com/concourse/concourse/atc/worker"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/atc/wrappa"
	"github.com/concourse/concourse/skymarshal/dexserver"
	"github.com/concourse/concourse/skymarshal/legacyserver"
	"github.com/concourse/concourse/skymarshal/skycmd"
	"github.com/concourse/concourse/skymarshal/skyserver"
	"github.com/concourse/concourse/skymarshal/storage"
	"github.com/concourse/concourse/skymarshal/token"
	"github.com/concourse/concourse/tracing"
	"github.com/concourse/concourse/web"
	"github.com/concourse/flag/v2"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/hashicorp/go-multierror"
	"github.com/jessevdk/go-flags"
	gocache "github.com/patrickmn/go-cache"
	"github.com/tedsuo/ifrit"
	"github.com/tedsuo/ifrit/grouper"
	"github.com/tedsuo/ifrit/http_server"
	"github.com/tedsuo/ifrit/sigmon"
	"go.yaml.in/yaml/v3"
	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
	"golang.org/x/oauth2"
	"golang.org/x/time/rate"

	// dynamically registered metric emitters
	_ "github.com/concourse/concourse/atc/metric/emitter"

	// dynamically registered policy checkers
	_ "github.com/concourse/concourse/atc/policy/opa"

	// dynamically registered credential managers
	_ "github.com/concourse/concourse/atc/creds/conjur"
	_ "github.com/concourse/concourse/atc/creds/credhub"
	_ "github.com/concourse/concourse/atc/creds/dummy"
	"github.com/concourse/concourse/atc/creds/idtoken"
	_ "github.com/concourse/concourse/atc/creds/kubernetes"
	_ "github.com/concourse/concourse/atc/creds/secretsmanager"
	_ "github.com/concourse/concourse/atc/creds/ssm"
	_ "github.com/concourse/concourse/atc/creds/vault"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	algorithmLimitRows    = 100
	agentSnapshotStageTTL = time.Hour
)

var schedulerCache = gocache.New(10*time.Second, 10*time.Second)

var defaultDriverName = "pgx"
var retryingDriverName = "retrying"

var flyClientID = "fly"
var flyClientSecret = "Zmx5"

type ATCCommand struct {
	RunCommand RunCommand `command:"run"`
	Migration  Migration  `command:"migrate"`
}

type snapshotStorageComposer func(
	db.DbConn,
	db.AgentSnapshotsFactory,
	snapshot.ArchiveLimits,
) (*jetbridge.DaemonClient, snapshot.ContentStore, error)

type snapshotSealerComposer func(
	snapshot.Canonicalizer,
	snapshot.ValidatorRegistry,
	snapshot.MetadataStore,
	snapshot.ContentStore,
	snapshot.DigestLockManager,
	time.Duration,
	time.Duration,
) (snapshot.SnapshotCreator, error)

type snapshotLifecycle interface {
	Collect(context.Context) (snapshot.LifecycleReport, error)
	Repair(context.Context) (snapshot.LifecycleReport, error)
}

// snapshotOrphanSweeper is an optional capability rather than part of
// snapshotLifecycle: the sweep needs a storage-side inventory that not every
// composition supplies, and a deployment without one must degrade to no sweep
// rather than fail to compose.
type snapshotOrphanSweeper interface {
	SweepOrphans(
		context.Context,
		snapshot.DurableInventory,
		snapshot.OrphanSweepMode,
		time.Duration,
	) (snapshot.LifecycleReport, error)
}

type snapshotLifecycleComposer func(
	snapshot.MetadataStore,
	snapshot.ContentStore,
	snapshot.ReplicaRepairer,
	snapshot.DigestLockManager,
) (snapshotLifecycle, error)

// snapshotPublisherComposer is the explicit integration seam for outbound
// provider adapters. Jetbridge supplies the durable audit store and the exact
// snapshot stores; a deployment supplies concrete Git/work-item adapters.
// With no composer, publication remains disabled and fail-closed.
type snapshotPublisherComposer func(
	publisher.Store,
	snapshot.MetadataStore,
	snapshot.ContentStore,
) (publisher.Executor, error)

func isNilDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

type RunCommand struct {
	Logger flag.Lager

	varSourcePool creds.VarSourcePool

	// k8sArtifactLocator is shared between the Reaper and Worker factory for
	// DaemonSet mode: workers record artifact locations into it and the Reaper
	// reads and reclaims them, so both must hold the same instance. Reach it
	// through artifactLocator(), never by assigning this field.
	k8sArtifactLocator *jetbridge.ArtifactLocator

	// Snapshot daemon transport and content storage are command-scoped so API
	// and backend composition share one durable client/store rather than
	// rebuilding restart-unsafe state inside each pool construction.
	agentSnapshotMu                 sync.Mutex
	agentSnapshotDaemonClient       *jetbridge.DaemonClient
	agentSnapshotContentStore       snapshot.ContentStore
	agentSnapshotMetadataStore      db.AgentSnapshotsFactory
	agentSnapshotWorkflowRuns       db.AgentWorkflowRunsFactory
	agentSnapshotDigestLocker       snapshot.DigestLockManager
	agentSnapshotValidatorRegistry  snapshot.ValidatorRegistry
	agentSnapshotOutputSealer       snapshot.OutputSealer
	agentSnapshotCreator            snapshot.SnapshotCreator
	agentSnapshotLifecycle          snapshotLifecycle
	agentSnapshotProjectionRegistry *projection.Registry
	agentResourceCaptureFinalizer   component.Runnable
	agentSnapshotHandlerFactory     *snapshotsapi.HandlerFactory
	agentSnapshotArchiveLimits      snapshot.ArchiveLimits
	agentSnapshotComposer           snapshotStorageComposer
	agentSnapshotSealerComposer     snapshotSealerComposer
	agentSnapshotLifecycleComposer  snapshotLifecycleComposer
	agentSnapshotPublisherComposer  snapshotPublisherComposer
	agentSnapshotPublisher          publisher.Executor

	agentChildAuthorityMu sync.Mutex
	agentChildSigner      *agentchildexecutions.CapabilitySigner
	agentChildVerifier    *agentchildexecutions.CapabilityVerifier
	agentBrokerRuntime    *agentBrokerRuntimeConfig

	// The ticket/workflow admission graph is composed ONCE and shared by the
	// dispatcher component and the dispatch API route. See agentDispatchGraph.
	agentDispatchMu    sync.Mutex
	agentDispatchGraph *agentDispatchGraph

	artifactResolveCapabilityMu  sync.Mutex
	artifactResolveCapabilityKey []byte

	BindIP   flag.IP `long:"bind-ip"   default:"0.0.0.0" description:"IP address on which to listen for web traffic."`
	BindPort uint16  `long:"bind-port" default:"8080"    description:"Port on which to listen for HTTP traffic."`

	TLSBindPort uint16    `long:"tls-bind-port" description:"Port on which to listen for HTTPS traffic."`
	TLSCert     flag.File `long:"tls-cert"      description:"File containing an SSL certificate."`
	TLSKey      flag.File `long:"tls-key"       description:"File containing an RSA private key, used to encrypt HTTPS traffic."`
	TLSCaCert   flag.File `long:"tls-ca-cert"   description:"File containing the client CA certificate, enables mTLS"`

	LetsEncrypt struct {
		Enable  bool     `long:"enable-lets-encrypt"   description:"Automatically configure TLS certificates via Let's Encrypt/ACME."`
		ACMEURL flag.URL `long:"lets-encrypt-acme-url" description:"URL of the ACME CA directory endpoint." default:"https://acme-v02.api.letsencrypt.org/directory"`
	} `group:"Let's Encrypt Configuration"`

	ExternalURL   flag.URL `long:"external-url" description:"URL used to reach any ATC from the outside world."`
	OIDCIssuerURL flag.URL `long:"oidc-issuer-url" description:"URL to use as the OIDC issuer for IDToken generation. If not set, defaults to external-url. Must be publicly accessible for cloud provider OIDC verification."`

	Postgres flag.PostgresConfig `group:"PostgreSQL Configuration" namespace:"postgres"`

	ConcurrentRequestLimits   map[wrappa.LimitedRoute]int `long:"concurrent-request-limit" description:"Limit the number of concurrent requests to an API endpoint (Example: ListAllJobs:5)"`
	APIMaxOpenConnections     int                         `long:"api-max-conns" description:"The maximum number of open connections for the api connection pool." default:"10"`
	BackendMaxOpenConnections int                         `long:"backend-max-conns" description:"The maximum number of open connections for the backend connection pool." default:"50"`

	CredentialManagement creds.CredentialManagementConfig `group:"Credential Management"`
	CredentialManagers   creds.Managers

	SigningKey struct {
		CheckInterval  time.Duration `long:"check-interval" default:"10m" description:"How often to check for outdated or expired signing keys for the idtoken secrets provider"`
		RotationPeriod time.Duration `long:"rotation-period" default:"168h" description:"After which time a new signing key for the idtoken secrets provider should be generated. 0 turns off generation of new keys"`
		GracePeriod    time.Duration `long:"grace-period" default:"24h" description:"How long a key should still be published for the idtoken secrets provider after a new key has been generated"`
	} `group:"Pipeline Identity Tokens" namespace:"signing-key"`

	EncryptionKey    flag.Cipher `long:"encryption-key"     description:"A 16 or 32 length key used to encrypt sensitive information before storing it in the database."`
	OldEncryptionKey flag.Cipher `long:"old-encryption-key" description:"Encryption key previously used for encrypting sensitive information. If provided without a new key, data is encrypted. If provided with a new key, data is re-encrypted."`

	DebugBindIP   flag.IP `long:"debug-bind-ip"   default:"127.0.0.1" description:"IP address on which to listen for the pprof debugger endpoints."`
	DebugBindPort uint16  `long:"debug-bind-port" default:"8079"      description:"Port on which to listen for the pprof debugger endpoints."`

	InterceptIdleTimeout time.Duration `long:"intercept-idle-timeout" default:"0m" description:"Length of time for a intercepted session to be idle before terminating."`

	GlobalResourceCheckTimeout          time.Duration `long:"global-resource-check-timeout" default:"1h" description:"Time limit on checking for new versions of resources."`
	ResourceCheckingInterval            time.Duration `long:"resource-checking-interval" default:"1m" description:"Interval on which to check for new versions of resources."`
	ResourceTypeCheckingInterval        time.Duration `long:"resource-type-checking-interval" default:"1m" description:"Interval on which to check for new versions of resource types."`
	ResourceWithWebhookCheckingInterval time.Duration `long:"resource-with-webhook-checking-interval" default:"1m" description:"Interval on which to check for new versions of resources that has webhook defined."`
	MaxChecksPerSecond                  int           `long:"max-checks-per-second" description:"Maximum number of checks that can be started per second. If not specified, this will be calculated as (# of resources)/(resource checking interval). -1 value will remove this maximum limit of checks per second."`
	PausePipelinesAfter                 int           `long:"pause-pipelines-after" default:"0" description:"The number of days after which a pipeline will be automatically paused if none of its jobs have run in more than the given number of days. A value of zero disables this component."`

	StreamingArtifactsCompression string `long:"streaming-artifacts-compression" default:"gzip" choice:"gzip" choice:"zstd" choice:"s2" choice:"raw" description:"Compression algorithm for internal streaming."`

	Kubernetes struct {
		Namespace                          string        `long:"kubernetes-namespace"              description:"Kubernetes namespace in which to run task Pods. When set, enables the K8s execution backend."`
		Kubeconfig                         string        `long:"kubernetes-kubeconfig"             description:"Path to kubeconfig file for K8s backend. If empty, in-cluster configuration is used."`
		PodStartupTimeout                  time.Duration `long:"kubernetes-pod-startup-timeout"      default:"5m"  description:"Maximum time to wait for a pod to reach Running state before failing the task."`
		PodSchedulingTimeout               time.Duration `long:"kubernetes-pod-scheduling-timeout"   default:"15m" description:"Maximum time to wait for an Unschedulable pod to be scheduled before failing the task. Set to 0 to fail immediately (old behavior)."`
		ImagePullSecrets                   []string      `long:"kubernetes-image-pull-secret"      description:"Kubernetes Secret name to use as imagePullSecrets on task Pods. Can be specified multiple times."`
		ServiceAccount                     string        `long:"kubernetes-service-account"        description:"Kubernetes ServiceAccount name to set on task Pods. Defaults to the namespace default SA."`
		CacheStore                         string        `long:"kubernetes-cache-store"            description:"Task cache backend: hostpath (node-local dirs) or emptydir (ephemeral). Empty = auto-detect."`
		CacheHostPath                      string        `long:"kubernetes-cache-host-path"        description:"Base directory on host node for persistent task caches. Caches are node-local and survive pod restarts."`
		ArtifactHelperImage                string        `long:"kubernetes-artifact-helper-image"     description:"Required artifact helper image pinned to an exact OCI sha256 digest."`
		ArtifactDaemonPort                 int           `long:"kubernetes-artifact-daemon-port"      default:"7780" description:"HTTP port for the DaemonSet artifact server (hostPort)."`
		ArtifactDaemonHostPath             string        `long:"kubernetes-artifact-daemon-host-path" description:"Host path for artifact storage on each node. When set, build pods require concourse.dev/artifact-cache=ready node label."`
		ArtifactDaemonService              string        `long:"kubernetes-artifact-daemon-service"   default:"artifact-daemon" description:"Headless Service name for DaemonSet per-pod DNS."`
		ArtifactDaemonTLSCert              string        `long:"kubernetes-artifact-daemon-tls-cert"    description:"Path to client certificate for mTLS with the artifact daemon."`
		ArtifactDaemonTLSKey               string        `long:"kubernetes-artifact-daemon-tls-key"     description:"Path to client private key for mTLS with the artifact daemon."`
		ArtifactDaemonTLSCACert            string        `long:"kubernetes-artifact-daemon-tls-ca-cert" description:"Path to CA certificate for verifying the artifact daemon's server certificate."`
		ArtifactDaemonResolveCapabilityKey string        `long:"kubernetes-artifact-daemon-resolve-capability-key" description:"Path to the raw 32-byte key used to authorize artifact resolve operations."`
		ArtifactDaemonResolveCapabilityTTL time.Duration `long:"kubernetes-artifact-daemon-resolve-capability-ttl" default:"2h" description:"Lifetime of operation-bound resolve capabilities; must cover the configured pod admission and init retry bounds."`
		ImageRegistryPrefix                string        `long:"kubernetes-image-registry-prefix"     description:"Registry path prefix for custom resource type images (e.g. gcr.io/my-project/concourse). Images are resolved as <prefix>/<type-name>."`
		ImageRegistrySecret                string        `long:"kubernetes-image-registry-secret"     description:"Kubernetes Secret name (type kubernetes.io/dockerconfigjson) for registry auth. Auto-added to imagePullSecrets on every pod."`
		BaseResourceTypes                  []string      `long:"kubernetes-base-resource-type"        description:"Override or add a base resource type image. Format: name=image (e.g. git=my-registry/git-resource:v2). Can be specified multiple times. Merges with built-in defaults." value-name:"NAME=IMAGE"`
	} `group:"Kubernetes Runtime"`

	AgentSnapshots struct {
		Enabled           bool          `long:"agent-snapshot-enabled" description:"Enable durable typed snapshot content storage through the Hangar-backed Kubernetes artifact daemon."`
		TempDir           string        `long:"agent-snapshot-temp-dir" description:"Dedicated disk-backed scratch directory for snapshot canonicalization, upload, repair, and validation."`
		ReplicationFactor int           `long:"agent-snapshot-replication-factor" default:"2" description:"Legacy daemon replica factor while adopting pre-Hangar snapshots; Hangar-backed uploads record one durable location."`
		MaxBytes          int64         `long:"agent-snapshot-max-bytes" default:"10737418240" description:"Maximum regular-file content bytes admitted by snapshot canonicalization; may only lower the built-in limit."`
		MaxFiles          int64         `long:"agent-snapshot-max-files" default:"100000" description:"Maximum entries admitted by snapshot canonicalization; may only lower the built-in limit."`
		BindingRetention  time.Duration `long:"agent-snapshot-binding-retention" default:"168h" description:"Retention period for ordinary workflow input and output bindings."`
		OrphanGracePeriod time.Duration `long:"agent-snapshot-orphan-grace-period" default:"1h" description:"Lease period protecting an in-progress snapshot upload from orphan collection."`
		GCInterval        time.Duration `long:"agent-snapshot-gc-interval" default:"5m" description:"Interval between bounded snapshot garbage-collection passes."`
		RepairInterval    time.Duration `long:"agent-snapshot-repair-interval" default:"10m" description:"Interval between bounded snapshot replica-repair passes."`
		// Defaults to off because this pass deletes durable content. "report"
		// logs exactly what "reclaim" would remove, so an operator can inspect a
		// real inventory before authorizing any deletion.
		OrphanSweepMode     string        `long:"agent-snapshot-orphan-sweep-mode" default:"off" choice:"off" choice:"report" choice:"reclaim" description:"Reclaim durable snapshot objects that no database row references: off disables the pass, report only logs what would be reclaimed, reclaim deletes them."`
		OrphanSweepAge      time.Duration `long:"agent-snapshot-orphan-sweep-age" default:"24h" description:"Minimum age before an unreferenced durable snapshot object may be reclaimed; must be at least 1h so an in-flight upload is never eaten."`
		OrphanSweepInterval time.Duration `long:"agent-snapshot-orphan-sweep-interval" default:"1h" description:"Interval between durable snapshot orphan sweeps."`
	} `group:"Agent Snapshots"`

	AgentWorkflowRuns struct {
		ReconcilerInterval time.Duration `long:"agent-workflow-run-reconciler-interval" default:"10s" description:"Interval between bounded durable workflow-run reconciliation passes."`
		AdmissionTimeout   time.Duration `long:"agent-workflow-run-admission-timeout" default:"15m" description:"Maximum age of an incomplete durable workflow-run admission before it is terminalized as interrupted."`
	} `group:"Agent Workflow Runs"`

	AgentChildExecutions struct {
		Enabled             bool          `long:"agent-child-executions-enabled" description:"Enable ATC authority, inspection, and bounded lease reconciliation for managed broker child executions."`
		BrokerCatalog       flag.File     `long:"agent-child-executions-broker-catalog" description:"Immutable deployment broker profile catalog JSON file. Provider, model, harness, and credentials remain server-only."`
		BrokerRuntime       flag.File     `long:"agent-child-executions-broker-runtime" description:"Strict deployment broker runtime JSON containing image-owned paths, K8s Secret key coordinates, and bounded resources."`
		CapabilityKey       flag.File     `long:"agent-child-executions-capability-key" description:"File containing the raw 32-byte HMAC key for execution-scoped broker capabilities."`
		CapabilityKeyID     string        `long:"agent-child-executions-capability-key-id" default:"agent-child-v1" description:"Stable key identifier embedded in broker execution capabilities."`
		CapabilityTTL       time.Duration `long:"agent-child-executions-capability-ttl" default:"1h" description:"Bounded lifetime for an execution-scoped broker capability."`
		LeaseDuration       time.Duration `long:"agent-child-executions-lease-duration" default:"5m" description:"ATC-owned lease duration renewed by managed broker lifecycle updates."`
		ReconcilerInterval  time.Duration `long:"agent-child-executions-reconciler-interval" default:"30s" description:"Interval between bounded expired broker lease reconciliation passes."`
		ReconcilerBatchSize int           `long:"agent-child-executions-reconciler-batch-size" default:"100" description:"Maximum expired broker leases terminalized in one reconciliation pass."`
	} `group:"Agent Child Executions"`

	AgentExperiments struct {
		Enabled        bool          `long:"agent-experiment-runner-enabled" description:"Enable durable experiment candidate, evaluator, and cancellation reconciliation."`
		Interval       time.Duration `long:"agent-experiment-runner-interval" default:"10s" description:"Interval between bounded durable experiment reconciliation passes."`
		MaxConcurrency int           `long:"agent-experiment-runner-max-concurrency" default:"4" description:"Maximum number of experiment cells claimed or reconciled per pass."`
	} `group:"Agent Experiments"`

	AgentPublisher struct {
		Enabled             bool                          `long:"agent-publisher-enabled" description:"Enable policy-controlled outbound publication inside ATC."`
		CredentialRoot      string                        `long:"agent-publisher-credential-root" default:"/run/concourse-publisher" description:"Trusted absolute non-root directory containing destination-scoped publisher credentials."`
		PolicyFile          string                        `long:"agent-publisher-policy-file" description:"Absolute path to the mounted exact-destination publisher policy JSON file."`
		CredentialFiles     agentPublisherCredentialFiles `long:"agent-publisher-credential-file" description:"Map one policy credential reference to one mounted absolute file path. May be specified multiple times." value-name:"REFERENCE:ABSOLUTE-PATH"`
		DirectGitEnabled    bool                          `long:"agent-publisher-direct-git-enabled" description:"Enable the in-process direct Git branch and trunk publication adapter."`
		RequestTimeout      time.Duration                 `long:"agent-publisher-request-timeout" default:"30s" description:"End-to-end timeout for one provider reconciliation or publication."`
		LeaseDuration       time.Duration                 `long:"agent-publisher-lease-duration" default:"5m" description:"Durable publication lease before lookup-based retry reconciliation."`
	} `group:"Agent Publisher"`

	CLIArtifactsDir flag.Dir `long:"cli-artifacts-dir" description:"Directory containing downloadable CLI binaries."`
	WebPublicDir    flag.Dir `long:"web-public-dir" description:"Web public/ directory to serve live for local development."`

	Metrics struct {
		HostName            string            `long:"metrics-host-name" description:"Host string to attach to emitted metrics."`
		Attributes          map[string]string `long:"metrics-attribute" description:"A key-value attribute to attach to emitted metrics. Can be specified multiple times." value-name:"NAME:VALUE"`
		BufferSize          uint32            `long:"metrics-buffer-size" default:"1000" description:"The size of the buffer used in emitting event metrics."`
		CaptureErrorMetrics bool              `long:"capture-error-metrics" description:"Enable capturing of error log metrics"`
	} `group:"Metrics & Diagnostics"`

	OTelMetrics tracing.MetricsConfig `group:"OTel Metrics" namespace:"otel-metrics"`

	Tracing tracing.Config `group:"Tracing" namespace:"tracing"`

	PolicyCheckers struct {
		Filter policy.Filter
	} `group:"Policy Checking"`

	Server struct {
		XFrameOptions           string `long:"x-frame-options" default:"deny" description:"The value to set for the X-Frame-Options header."`
		ContentSecurityPolicy   string `long:"content-security-policy" default:"frame-ancestors 'none'" description:"The value to set for the Content-Security-Policy header."`
		StrictTransportSecurity string `long:"strict-transport-security" description:"The value to set for the Strict-Transport-Security header."`
		ClusterName             string `long:"cluster-name" description:"A name for this Concourse cluster, to be displayed on the dashboard page."`
		ClientID                string `long:"client-id" default:"concourse-web" description:"Client ID to use for login flow"`
		ClientSecret            string `long:"client-secret" required:"true" description:"Client secret to use for login flow"`
	} `group:"Web Server"`

	AgentStepImage string `long:"agent-step-image" description:"Container image for the agent: step's main container (must contain the claude CLI and agent-runner). Schema-v3 workflow runs and resource snapshot captures require an exact @sha256 digest; agent steps error at runtime when unset."`

	AgentPlatformTokenSecret string `long:"agent-platform-token-secret" default:"agent-platform-credential" description:"Name of the K8s secret (keys 'anthropic-token' and 'kind') every agent pod mounts its Anthropic token from. The default is the secret the platform-credential syncer maintains from the vaulted platform credential (fly agent auth --platform); point it elsewhere only to supply the secret out of band. Empty means agent steps have no token path."`

	AgentDailyBudgetUSD float64 `long:"agent-daily-budget-usd" default:"0" description:"Global daily agent LLM spend cap in USD across all agent work, enforced by atomic workflow and experiment reservations and reported by the cost rollup API. 0 disables the cap."`

	LogDBQueries   bool `long:"log-db-queries" description:"Log database queries."`
	LogClusterName bool `long:"log-cluster-name" description:"Log cluster name."`

	GC struct {
		Interval time.Duration `long:"interval" default:"30s" description:"Interval on which to perform garbage collection."`

		OneOffBuildGracePeriod     time.Duration `long:"one-off-grace-period" default:"5m" description:"Period after which one-off build containers will be garbage-collected."`
		MissingGracePeriod         time.Duration `long:"missing-grace-period" default:"5m" description:"Period after which to reap containers and volumes that were created but went missing from the worker."`
		HijackGracePeriod          time.Duration `long:"hijack-grace-period" default:"5m" description:"Period after which hijacked containers will be garbage collected"`
		FailedGracePeriod          time.Duration `long:"failed-grace-period" default:"120h" description:"Period after which failed containers will be garbage collected"`
		CheckRecyclePeriod         time.Duration `long:"check-recycle-period" default:"1m" description:"Period after which to reap checks that are completed."`
		VarSourceRecyclePeriod     time.Duration `long:"var-source-recycle-period" default:"5m" description:"Period after which to reap var_sources that are not used."`
		DeprecatedScopeGracePeriod time.Duration `long:"deprecated-scope-grace-period" default:"720h" description:"Period after which deprecated resource config scopes (from resource type/source changes) will be garbage collected. Default 30 days."`

		WorkflowRunTemplateGracePeriod time.Duration `long:"workflow-run-template-grace-period" default:"24h" description:"Period after which a server-owned workflow-run template that never executed will be garbage collected. Must exceed --agent-workflow-run-admission-timeout."`

		WorkflowRunTemplateRetirementPeriod time.Duration `long:"workflow-run-template-retirement-period" default:"720h" description:"Period after which the fully archived execution history of a superseded workflow-run template (its run records, run instance pipelines, and the template itself) is destroyed as one unit. Templates that declare their own run_retention are only destroyed once that policy would discard every run. 0 disables retirement. Default 30 days."`
	} `group:"Garbage Collection" namespace:"gc"`

	TelemetryOptIn bool `long:"telemetry-opt-in" hidden:"true" description:"Enable anonymous concourse version reporting."`

	DefaultBuildLogsToRetain uint64 `long:"default-build-logs-to-retain" description:"Default build logs to retain, 0 means all"`
	MaxBuildLogsToRetain     uint64 `long:"max-build-logs-to-retain" description:"Maximum build logs to retain, 0 means not specified. Will override values configured in jobs"`

	DefaultDaysToRetainBuildLogs uint64 `long:"default-days-to-retain-build-logs" description:"Default days to retain build logs. 0 means unlimited"`
	MaxDaysToRetainBuildLogs     uint64 `long:"max-days-to-retain-build-logs" description:"Maximum days to retain build logs, 0 means not specified. Will override values configured in jobs"`

	JobSchedulingMaxInFlight uint64 `long:"job-scheduling-max-in-flight" default:"32" description:"Maximum number of jobs to be scheduling at the same time"`

	DefaultCpuLimit    *int    `long:"default-task-cpu-limit" description:"Default max number of cpu shares per task, 0 means unlimited"`
	DefaultMemoryLimit *string `long:"default-task-memory-limit" description:"Default maximum memory per task, 0 means unlimited"`

	DefaultCpuRequest    *int    `long:"default-task-cpu-request" description:"Default CPU request (shares) per task for Burstable QoS"`
	DefaultMemoryRequest *string `long:"default-task-memory-request" description:"Default memory request per task for Burstable QoS"`

	Auditor struct {
		EnableBuildAuditLog     bool `long:"enable-build-auditing" description:"Enable auditing for all api requests connected to builds."`
		EnableContainerAuditLog bool `long:"enable-container-auditing" description:"Enable auditing for all api requests connected to containers."`
		EnableJobAuditLog       bool `long:"enable-job-auditing" description:"Enable auditing for all api requests connected to jobs."`
		EnablePipelineAuditLog  bool `long:"enable-pipeline-auditing" description:"Enable auditing for all api requests connected to pipelines."`
		EnableResourceAuditLog  bool `long:"enable-resource-auditing" description:"Enable auditing for all api requests connected to resources."`
		EnableSystemAuditLog    bool `long:"enable-system-auditing" description:"Enable auditing for all api requests connected to system transactions."`
		EnableTeamAuditLog      bool `long:"enable-team-auditing" description:"Enable auditing for all api requests connected to teams."`
		EnableWorkerAuditLog    bool `long:"enable-worker-auditing" description:"Enable auditing for all api requests connected to workers."`
		EnableVolumeAuditLog    bool `long:"enable-volume-auditing" description:"Enable auditing for all api requests connected to volumes."`
	}

	Syslog struct {
		Hostname  string   `long:"syslog-hostname" description:"Client hostname with which the build logs will be sent to the syslog server." default:"atc-syslog-drainer"`
		Address   string   `long:"syslog-address" description:"Remote syslog server address with port (Example: 0.0.0.0:514)."`
		Transport string   `long:"syslog-transport" description:"Transport protocol for syslog messages (Currently supporting tcp, udp & tls)."`
		CACerts   []string `long:"syslog-ca-cert"              description:"Paths to PEM-encoded CA cert files to use to verify the Syslog server SSL cert."`
	} ` group:"Syslog Drainer Configuration"`

	Auth struct {
		AuthFlags     skycmd.AuthFlags
		MainTeamFlags skycmd.AuthTeamFlags `group:"Authentication (Main Team)" namespace:"main-team"`
	} `group:"Authentication"`

	ConfigRBAC flag.File `long:"config-rbac" description:"Customize RBAC role-action mapping."`

	SystemClaimKey    string   `long:"system-claim-key" default:"aud" description:"The token claim key to use when matching system-claim-values"`
	SystemClaimValues []string `long:"system-claim-value" default:"concourse-worker" description:"Configure which token requests should be considered 'system' requests."`

	FeatureFlags struct {
		EnableGlobalResources                bool `long:"enable-global-resources" description:"Enable equivalent resources across pipelines and teams to share a single version history."`
		EnableBuildRerunWhenWorkerDisappears bool `long:"enable-rerun-when-worker-disappears" description:"Enable automatically build rerun when worker disappears or a network error occurs"`
		EnableResourceCausality              bool `long:"enable-resource-causality" description:"Enable the resource causality page. Computing causality can be expensive for the database. "`
	} `group:"Feature Flags"`

	BaseResourceTypeDefaults flag.File `long:"base-resource-type-defaults" description:"Base resource type defaults"`

	DisplayUserIdPerConnector map[string]string `long:"display-user-id-per-connector" description:"Define how to display user ID for each authentication connector. Format is <connector>:<fieldname>. Valid field names are user_id, name, username and email, where name maps to claims field username, and username maps to claims field preferred username"`

	DefaultGetTimeout  time.Duration `long:"default-get-timeout" description:"Default timeout of get steps"`
	DefaultPutTimeout  time.Duration `long:"default-put-timeout" description:"Default timeout of put steps"`
	DefaultTaskTimeout time.Duration `long:"default-task-timeout" description:"Default timeout of task steps"`

	DisableRedactSecrets bool `long:"disable-redact-secrets" description:"Disables secret redaction in build logs."`
}

type Migration struct {
	lockFactory lock.LockFactory

	Postgres               flag.PostgresConfig `group:"PostgreSQL Configuration" namespace:"postgres"`
	EncryptionKey          flag.Cipher         `long:"encryption-key"     description:"A 16 or 32 length key used to encrypt sensitive information before storing it in the database."`
	OldEncryptionKey       flag.Cipher         `long:"old-encryption-key" description:"Encryption key previously used for encrypting sensitive information. If provided without a new key, data is decrypted. If provided with a new key, data is re-encrypted."`
	CurrentDBVersion       bool                `long:"current-db-version" description:"Print the current database version and exit"`
	SupportedDBVersion     bool                `long:"supported-db-version" description:"Print the max supported database version and exit"`
	MigrateDBToVersion     int                 `long:"migrate-db-to-version" description:"Migrate to the specified database version and exit"`
	MigrateToLatestVersion bool                `long:"migrate-to-latest-version" description:"Migrate to the latest migration version and exit"`
}

func (m *Migration) Execute(args []string) error {
	db.SetupConnectionRetryingDriver(
		defaultDriverName,
		m.Postgres.ConnectionString(),
		retryingDriverName,
	)

	lockConns, err := constructLockConns(retryingDriverName, m.Postgres.ConnectionString())
	if err != nil {
		return err
	}
	defer func() {
		for _, conn := range lockConns {
			conn.Close()
		}
	}()

	m.lockFactory = lock.NewLockFactory(lockConns, metric.LogLockAcquired, metric.LogLockReleased)

	if m.MigrateToLatestVersion {
		return m.migrateToLatestVersion()
	}
	if m.CurrentDBVersion {
		return m.currentDBVersion()
	}
	if m.SupportedDBVersion {
		return m.supportedDBVersion()
	}
	if m.MigrateDBToVersion > 0 {
		return m.migrateDBToVersion()
	}
	if m.OldEncryptionKey.AEAD != nil {
		return m.rotateEncryptionKey()
	}
	return errors.New("must specify one of `--migrate-to-latest-version`, `--current-db-version`, `--supported-db-version`, `--migrate-db-to-version`, or `--old-encryption-key`")
}

func (cmd *Migration) currentDBVersion() error {
	helper := migration.NewOpenHelper(
		defaultDriverName,
		cmd.Postgres.ConnectionString(),
		cmd.lockFactory,
		nil,
		nil,
	)

	version, err := helper.CurrentVersion()
	if err != nil {
		return err
	}

	fmt.Println(version)
	return nil
}

func (cmd *Migration) supportedDBVersion() error {
	helper := migration.NewOpenHelper(
		defaultDriverName,
		cmd.Postgres.ConnectionString(),
		cmd.lockFactory,
		nil,
		nil,
	)

	version, err := helper.SupportedVersion()
	if err != nil {
		return err
	}

	fmt.Println(version)
	return nil
}

func (cmd *Migration) migrateDBToVersion() error {
	version := cmd.MigrateDBToVersion

	var newKey *encryption.Key
	var oldKey *encryption.Key

	if cmd.EncryptionKey.AEAD != nil {
		newKey = encryption.NewKey(cmd.EncryptionKey.AEAD)
	}
	if cmd.OldEncryptionKey.AEAD != nil {
		oldKey = encryption.NewKey(cmd.OldEncryptionKey.AEAD)
	}

	helper := migration.NewOpenHelper(
		defaultDriverName,
		cmd.Postgres.ConnectionString(),
		cmd.lockFactory,
		newKey,
		oldKey,
	)

	err := helper.MigrateToVersion(version)
	if err != nil {
		return fmt.Errorf("could not migrate to version: %d Reason: %s", version, err.Error())
	}

	fmt.Println("Successfully migrated to version:", version)
	return nil
}

func (cmd *Migration) rotateEncryptionKey() error {
	var newKey *encryption.Key
	var oldKey *encryption.Key

	if cmd.EncryptionKey.AEAD != nil {
		newKey = encryption.NewKey(cmd.EncryptionKey.AEAD)
	}
	if cmd.OldEncryptionKey.AEAD != nil {
		oldKey = encryption.NewKey(cmd.OldEncryptionKey.AEAD)
	}

	helper := migration.NewOpenHelper(
		defaultDriverName,
		cmd.Postgres.ConnectionString(),
		cmd.lockFactory,
		newKey,
		oldKey,
	)

	version, err := helper.CurrentVersion()
	if err != nil {
		return err
	}

	return helper.MigrateToVersion(version)
}

func (cmd *Migration) migrateToLatestVersion() error {
	helper := migration.NewOpenHelper(
		defaultDriverName,
		cmd.Postgres.ConnectionString(),
		cmd.lockFactory,
		nil,
		nil,
	)

	version, err := helper.SupportedVersion()
	if err != nil {
		return err
	}

	return helper.MigrateToVersion(version)
}

func (cmd *ATCCommand) WireDynamicFlags(commandFlags *flags.Command) {
	cmd.RunCommand.WireDynamicFlags(commandFlags)
}

func (cmd *RunCommand) WireDynamicFlags(commandFlags *flags.Command) {
	var (
		metricsGroup      *flags.Group
		policyChecksGroup *flags.Group
		credsGroup        *flags.Group
		authGroup         *flags.Group
	)

	groups := commandFlags.Groups()
	for i := 0; i < len(groups); i++ {
		group := groups[i]

		if credsGroup == nil && group.ShortDescription == "Credential Management" {
			credsGroup = group
		}

		if metricsGroup == nil && group.ShortDescription == "Metrics & Diagnostics" {
			metricsGroup = group
		}

		if policyChecksGroup == nil && group.ShortDescription == "Policy Checking" {
			policyChecksGroup = group
		}

		if authGroup == nil && group.ShortDescription == "Authentication" {
			authGroup = group
		}

		if metricsGroup != nil && credsGroup != nil && authGroup != nil && policyChecksGroup != nil {
			break
		}

		groups = append(groups, group.Groups()...)
	}

	if metricsGroup == nil {
		panic("could not find Metrics & Diagnostics group for registering emitters")
	}

	if policyChecksGroup == nil {
		panic("could not find Policy Checking group for registering policy checkers")
	}

	if credsGroup == nil {
		panic("could not find Credential Management group for registering managers")
	}

	if authGroup == nil {
		panic("could not find Authentication group for registering connectors")
	}

	managerConfigs := make(creds.Managers)
	for name, p := range creds.ManagerFactories() {
		managerConfigs[name] = p.AddConfig(credsGroup)
	}

	cmd.CredentialManagers = managerConfigs

	metric.Metrics.WireEmitters(metricsGroup)

	policy.WireCheckers(policyChecksGroup)

	skycmd.WireConnectors(authGroup)
	skycmd.WireTeamConnectors(authGroup.Find("Authentication (Main Team)"))
}

func (cmd *RunCommand) Execute(args []string) error {
	runner, err := cmd.Runner(args)
	if err != nil {
		return err
	}

	return <-ifrit.Invoke(sigmon.New(runner)).Wait()
}

func (cmd *RunCommand) Runner(positionalArguments []string) (ifrit.Runner, error) {
	if cmd.ExternalURL.URL == nil {
		cmd.ExternalURL = cmd.DefaultURL()
	}

	if len(positionalArguments) != 0 {
		return nil, fmt.Errorf("unexpected positional arguments: %v", positionalArguments)
	}

	err := cmd.validate()
	if err != nil {
		return nil, err
	}

	logger, reconfigurableSink := cmd.Logger.Logger("atc")
	if cmd.LogClusterName {
		logger = logger.WithData(lager.Data{
			"cluster": cmd.Server.ClusterName,
		})
	}

	commandSession := logger.Session("cmd")
	startTime := time.Now()

	commandSession.Info("start")
	defer func() {
		commandSession.Info("finish", lager.Data{
			"duration": time.Since(startTime),
		})
	}()

	atc.EnableGlobalResources = cmd.FeatureFlags.EnableGlobalResources
	atc.EnableBuildRerunWhenWorkerDisappears = cmd.FeatureFlags.EnableBuildRerunWhenWorkerDisappears
	atc.EnableResourceCausality = cmd.FeatureFlags.EnableResourceCausality
	atc.DefaultCheckInterval = cmd.ResourceCheckingInterval
	atc.DefaultWebhookInterval = cmd.ResourceWithWebhookCheckingInterval
	atc.DefaultResourceTypeInterval = cmd.ResourceTypeCheckingInterval
	atc.DisableRedactSecrets = cmd.DisableRedactSecrets

	if cmd.BaseResourceTypeDefaults.Path() != "" {
		content, err := os.ReadFile(cmd.BaseResourceTypeDefaults.Path())
		if err != nil {
			return nil, err
		}

		defaults := map[string]atc.Source{}
		err = yaml.Unmarshal(content, &defaults)
		if err != nil {
			return nil, err
		}

		atc.LoadBaseResourceTypeDefaults(defaults)
	}

	db.SetupConnectionRetryingDriver(
		defaultDriverName,
		cmd.Postgres.ConnectionString(),
		retryingDriverName,
	)

	// Register the sink that collects error metrics
	if cmd.Metrics.CaptureErrorMetrics {
		errorSinkCollector := metric.NewErrorSinkCollector(
			logger,
			metric.Metrics,
		)
		logger.RegisterSink(&errorSinkCollector)
	}

	err = cmd.Tracing.Prepare()
	if err != nil {
		return nil, err
	}

	mp, mpShutdown, err := cmd.OTelMetrics.MeterProvider()
	if err != nil {
		return nil, fmt.Errorf("otel metrics: %w", err)
	}
	if mp != nil {
		tracing.ConfigureMeterProvider(mp)
		logger.Info("otel-metrics-configured")
		_ = mpShutdown // shutdown handled by process lifecycle
	}

	metric.InitOTelStepDuration()
	metric.InitOTelMetrics()
	metric.InitOTelBuildLifecycle()
	metric.InitOTelStepWaiting()
	metric.InitOTelScheduling()
	metric.InitOTelGC()
	metric.InitOTelDBChecks()
	metric.InitOTelArtifactUpload()
	metric.InitOTelWorkflowRunReconciler()

	// Connection tracker is off by default. Can be turned on/ff at runtime.
	http.HandleFunc("/debug/connections", func(w http.ResponseWriter, r *http.Request) {
		for _, stack := range db.GlobalConnectionTracker.Current() {
			fmt.Fprintln(w, stack)
		}
	})
	http.HandleFunc("/debug/connections/on", func(w http.ResponseWriter, r *http.Request) {
		db.InitConnectionTracker(true)
	})
	http.HandleFunc("/debug/connections/off", func(w http.ResponseWriter, r *http.Request) {
		db.InitConnectionTracker(false)
	})

	if err := cmd.configureMetrics(logger); err != nil {
		return nil, err
	}

	lockConns, err := constructLockConns(retryingDriverName, cmd.Postgres.ConnectionString())
	if err != nil {
		return nil, err
	}

	lockFactory := lock.NewLockFactory(lockConns, metric.LogLockAcquired, metric.LogLockReleased)

	apiConn, err := cmd.constructDBConn(retryingDriverName, logger, cmd.APIMaxOpenConnections, cmd.APIMaxOpenConnections/2, "api", lockFactory)
	if err != nil {
		return nil, err
	}

	backendConn, err := cmd.constructDBConn(retryingDriverName, logger, cmd.BackendMaxOpenConnections, cmd.BackendMaxOpenConnections/2, "backend", lockFactory)
	if err != nil {
		return nil, err
	}

	gcConn, err := cmd.constructDBConn(retryingDriverName, logger, 5, 2, "gc", lockFactory)
	if err != nil {
		return nil, err
	}

	workerConn, err := cmd.constructDBConn(retryingDriverName, logger, 1, 1, "worker", lockFactory)
	if err != nil {
		return nil, err
	}

	err = db.CacheWarmUp(backendConn)
	if err != nil {
		return nil, err
	}

	storage, err := storage.NewPostgresStorage(logger, cmd.Postgres)
	if err != nil {
		return nil, err
	}

	issuer := cmd.ExternalURL.String()
	if cmd.OIDCIssuerURL.String() != "" {
		issuer = cmd.OIDCIssuerURL.String()
	}

	idtoken.UpdateGlobalManagerFactory(func(f *idtoken.ManagerFactory) {
		f.SetIssuer(issuer)
	})

	secretManager, err := cmd.secretManager(logger)
	if err != nil {
		return nil, err
	}

	cmd.varSourcePool = creds.NewVarSourcePool(
		logger.Session("var-source-pool"),
		cmd.CredentialManagement,
		cmd.GC.VarSourceRecyclePeriod,
		1*time.Minute,
		clock.NewClock(),
	)

	members, err := cmd.constructMembers(logger, reconfigurableSink, apiConn, workerConn, backendConn, gcConn, storage, lockFactory, secretManager)
	if err != nil {
		return nil, err
	}

	members = append(members, grouper.Member{
		Name: "periodic-metrics",
		Runner: metric.PeriodicallyEmit(
			logger.Session("periodic-metrics"),
			metric.Metrics,
			10*time.Second,
		),
	})

	onReady := func() {
		logData := lager.Data{
			"http":  cmd.nonTLSBindAddr(),
			"debug": cmd.debugBindAddr(),
		}

		if cmd.isTLSEnabled() {
			logData["https"] = cmd.tlsBindAddr()
		}

		logger.Info("listening", logData)
	}

	onExit := func() {
		for _, closer := range []Closer{apiConn, backendConn, gcConn, storage, workerConn} {
			closer.Close()
		}
		for _, closer := range lockConns {
			closer.Close()
		}
		cmd.varSourcePool.Close()
	}

	return run(grouper.NewParallel(os.Interrupt, members), onReady, onExit), nil
}

func (cmd *RunCommand) constructMembers(
	logger lager.Logger,
	reconfigurableSink *lager.ReconfigurableSink,
	apiConn db.DbConn,
	workerConn db.DbConn,
	backendConn db.DbConn,
	gcConn db.DbConn,
	storage storage.Storage,
	lockFactory lock.LockFactory,
	secretManager creds.Secrets,
) ([]grouper.Member, error) {
	if cmd.TelemetryOptIn {
		url := fmt.Sprintf("http://telemetry.concourse-ci.org/?version=%s", concourse.Version)
		go func() {
			_, err := http.Get(url)
			if err != nil {
				logger.Error("telemetry-version", err)
			}
		}()
	}

	policyChecker, err := policy.Initialize(logger, cmd.Server.ClusterName, concourse.Version, cmd.PolicyCheckers.Filter)
	if err != nil {
		return nil, err
	}

	workerCache, err := db.NewWorkerCache(logger.Session("worker-cache"), backendConn, 1*time.Minute)
	if err != nil {
		return nil, err
	}
	// Snapshot content is a command-scoped service, not a property of whichever
	// worker pool happens to be constructed first. Select backendConn
	// deliberately, compose once, and inject the exact daemon client into both
	// API/backend pools below.
	if err := cmd.composeAgentSnapshots(backendConn, logger); err != nil {
		return nil, err
	}
	checkBuildsChan := make(chan db.Build, 2000)
	apiMembers, err := cmd.constructAPIMembers(logger, reconfigurableSink, apiConn, workerConn, storage, lockFactory, secretManager, policyChecker, workerCache, checkBuildsChan)
	if err != nil {
		return nil, err
	}

	backendComponents, err := cmd.backendComponents(logger, backendConn, lockFactory, secretManager, policyChecker, workerCache, checkBuildsChan)
	if err != nil {
		return nil, err
	}

	gcComponents, err := cmd.gcComponents(logger, gcConn, lockFactory)
	if err != nil {
		return nil, err
	}

	// use backendConn so that the Component objects created by the factory uses
	// the backend connection pool when reloading.
	componentFactory := db.NewComponentFactory(backendConn)
	bus := backendConn.Bus()

	// Default polling interval for components that don't specify one.
	// Components with NOTIFY triggers will wake immediately on signals;
	// this interval is a safety net so that components without triggers
	// (scheduler, GC collectors, k8s reaper, etc.) still run periodically.
	const defaultComponentInterval = 10 * time.Second

	members := apiMembers
	components := append(backendComponents, gcComponents...)
	for _, c := range components {
		dbComponent, err := componentFactory.CreateOrUpdate(c.Component)
		if err != nil {
			return nil, err
		}

		componentLogger := logger.Session(c.Component.Name)

		interval := c.Interval
		if interval == 0 {
			interval = defaultComponentInterval
		}

		members = append(members, grouper.Member{
			Name: c.Component.Name,
			Runner: &component.Runner{
				Logger:    componentLogger,
				Interval:  interval,
				Component: dbComponent,
				Bus:       bus,
				Schedulable: &component.Coordinator{
					Locker:    lockFactory,
					Component: dbComponent,
					Runnable:  c.Runnable,
				},
			},
		})

		if drainable, ok := c.Runnable.(component.Drainable); ok {
			members = append(members, grouper.Member{
				Name: c.Component.Name + "-drainer",
				Runner: drainRunner{
					logger:  componentLogger.Session("drain"),
					drainer: drainable,
				},
			})
		}
	}

	return members, nil
}

func (cmd *RunCommand) constructAPIMembers(
	logger lager.Logger,
	reconfigurableSink *lager.ReconfigurableSink,
	dbConn db.DbConn,
	workerConn db.DbConn,
	storage storage.Storage,
	lockFactory lock.LockFactory,
	secretManager creds.Secrets,
	policyChecker policy.Checker,
	workerCache *db.WorkerCache,
	checkBuildsChan chan db.Build,
) ([]grouper.Member, error) {

	httpClient, err := cmd.skyHttpClient()
	if err != nil {
		return nil, err
	}

	teamFactory := db.NewTeamFactory(dbConn, lockFactory)
	workerTeamFactory := db.NewTeamFactory(workerConn, lockFactory)

	_, err = teamFactory.CreateDefaultTeamIfNotExists()
	if err != nil {
		return nil, err
	}

	err = cmd.configureAuthForDefaultTeam(teamFactory)
	if err != nil {
		return nil, err
	}

	userFactory := db.NewUserFactory(dbConn)

	dbResourceConfigFactory := db.NewResourceConfigFactory(dbConn, lockFactory)

	// Materialize the shared ArtifactLocator BEFORE constructPool, so the
	// pool's worker factory receives it.
	cmd.artifactLocator()

	pool, err := cmd.constructPool(dbConn, lockFactory, workerCache)
	if err != nil {
		return nil, err
	}

	// The worker factory has its own connection pool (for worker registration)
	dbWorkerFactory := db.NewWorkerFactory(workerConn, workerCache)

	credsManagers := cmd.CredentialManagers
	dbPipelineFactory := db.NewPipelineFactory(dbConn, lockFactory)
	dbJobFactory := db.NewJobFactory(dbConn, lockFactory)
	dbResourceFactory := db.NewResourceFactory(dbConn, lockFactory)
	dbContainerRepository := db.NewContainerRepository(dbConn)
	dbVolumeRepository := db.NewVolumeRepository(dbConn)
	gcContainerDestroyer := gc.NewDestroyer(logger, dbContainerRepository, dbVolumeRepository)
	dbBuildFactory := db.NewBuildFactory(dbConn, lockFactory, cmd.GC.OneOffBuildGracePeriod, cmd.GC.FailedGracePeriod)
	dbCheckFactory := db.NewCheckFactory(dbConn, lockFactory, secretManager, cmd.varSourcePool, checkBuildsChan, nil)
	dbPipelineRunFactory := db.NewPipelineRunFactory(logger, dbConn, lockFactory, dbCheckFactory)
	dbSigningKeyFactory := db.NewSigningKeyFactory(dbConn)
	dbClock := db.NewClock()
	dbWall := db.NewWall(dbConn, &dbClock)

	tokenVerifier := cmd.constructTokenVerifier()

	teamsCacher := accessor.NewTeamsCacher(
		logger,
		dbConn.Bus(),
		teamFactory,
		time.Minute,
		time.Minute,
	)

	displayUserIdGenerator, err := skycmd.NewSkyDisplayUserIdGenerator(cmd.DisplayUserIdPerConnector)
	if err != nil {
		return nil, err
	}

	accessFactory := accessor.NewAccessFactory(
		tokenVerifier,
		teamsCacher,
		cmd.SystemClaimKey,
		cmd.SystemClaimValues,
		displayUserIdGenerator,
	)

	middleware := token.NewMiddleware(cmd.Auth.AuthFlags.SecureCookies)

	apiHandler, err := cmd.constructAPIHandler(
		logger,
		reconfigurableSink,
		teamFactory,
		workerTeamFactory,
		dbPipelineFactory,
		dbJobFactory,
		dbResourceFactory,
		dbWorkerFactory,
		dbVolumeRepository,
		dbContainerRepository,
		gcContainerDestroyer,
		dbBuildFactory,
		dbCheckFactory,
		dbPipelineRunFactory,
		dbResourceConfigFactory,
		userFactory,
		pool,
		secretManager,
		credsManagers,
		accessFactory,
		dbWall,
		policyChecker,
		dbSigningKeyFactory,
		lockFactory,
		dbConn,
	)
	if err != nil {
		return nil, err
	}

	webHandler, err := cmd.constructWebHandler(logger)
	if err != nil {
		return nil, err
	}

	authHandler, err := cmd.constructAuthHandler(
		logger,
		storage,
		userFactory,
		displayUserIdGenerator,
	)
	if err != nil {
		return nil, err
	}

	skyHandler, err := cmd.constructSkyHandler(
		logger,
		httpClient,
		middleware,
	)
	if err != nil {
		return nil, err
	}

	legacyHandler, err := cmd.constructLegacyHandler(
		logger,
	)

	if err != nil {
		return nil, err
	}

	var httpHandler, httpsHandler http.Handler
	if cmd.isTLSEnabled() {
		httpHandler = cmd.constructHTTPHandler(
			logger,

			tlsRedirectHandler{
				matchHostname: cmd.ExternalURL.URL.Hostname(),
				externalHost:  cmd.ExternalURL.URL.Host,
				baseHandler:   webHandler,
			},

			// note: intentionally not wrapping API; redirecting is more trouble than
			// it's worth.

			// we're mainly interested in having the web UI consistently https:// -
			// API requests will likely not respect the redirected https:// URI upon
			// the next request, plus the payload will have already been sent in
			// plaintext
			apiHandler,

			tlsRedirectHandler{
				matchHostname: cmd.ExternalURL.URL.Hostname(),
				externalHost:  cmd.ExternalURL.URL.Host,
				baseHandler:   authHandler,
			},
			tlsRedirectHandler{
				matchHostname: cmd.ExternalURL.URL.Hostname(),
				externalHost:  cmd.ExternalURL.URL.Host,
				baseHandler:   skyHandler,
			},
			tlsRedirectHandler{
				matchHostname: cmd.ExternalURL.URL.Hostname(),
				externalHost:  cmd.ExternalURL.URL.Host,
				baseHandler:   legacyHandler,
			},
			middleware,
		)

		httpsHandler = cmd.constructHTTPHandler(
			logger,
			webHandler,
			apiHandler,
			authHandler,
			skyHandler,
			legacyHandler,
			middleware,
		)
	} else {
		httpHandler = cmd.constructHTTPHandler(
			logger,
			webHandler,
			apiHandler,
			authHandler,
			skyHandler,
			legacyHandler,
			middleware,
		)
	}

	members := []grouper.Member{
		{Name: "debug", Runner: http_server.New(
			cmd.debugBindAddr(),
			http.DefaultServeMux,
		)},
		{Name: "web", Runner: http_server.New(
			cmd.nonTLSBindAddr(),
			httpHandler,
		)},
	}

	if httpsHandler != nil {
		tlsConfig, err := cmd.tlsConfig(logger, dbConn)
		if err != nil {
			return nil, err
		}
		members = append(members, grouper.Member{Name: "web-tls", Runner: http_server.NewTLSServer(
			cmd.tlsBindAddr(),
			httpsHandler,
			tlsConfig,
		)})
	}

	return members, nil
}

func (cmd *RunCommand) backendComponents(
	logger lager.Logger,
	dbConn db.DbConn,
	lockFactory lock.LockFactory,
	secretManager creds.Secrets,
	policyChecker policy.Checker,
	workerCache *db.WorkerCache,
	checkBuildsChan chan db.Build,
) ([]RunnableComponent, error) {

	if cmd.Syslog.Address != "" && cmd.Syslog.Transport == "" {
		return nil, fmt.Errorf("syslog Drainer is misconfigured, cannot configure a drainer without a transport")
	}

	syslogDrainConfigured := true
	if cmd.Syslog.Address == "" {
		syslogDrainConfigured = false
	}

	teamFactory := db.NewTeamFactory(dbConn, lockFactory)

	dbResourceCacheFactory := db.NewResourceCacheFactory(dbConn, lockFactory)
	dbResourceConfigFactory := db.NewResourceConfigFactory(dbConn, lockFactory)

	dbBuildFactory := db.NewBuildFactory(dbConn, lockFactory, cmd.GC.OneOffBuildGracePeriod, cmd.GC.FailedGracePeriod)
	dbCheckFactory := db.NewCheckFactory(dbConn, lockFactory, secretManager, cmd.varSourcePool, checkBuildsChan, util.NewSequenceGenerator(1))
	dbPipelineRunFactory := db.NewPipelineRunFactory(logger, dbConn, lockFactory, dbCheckFactory)
	dbPipelineFactory := db.NewPipelineFactory(dbConn, lockFactory)
	dbJobFactory := db.NewJobFactory(dbConn, lockFactory)
	dbPipelineLifecycle := db.NewPipelineLifecycle(dbConn, lockFactory)
	dbPipelinePauser := db.NewPipelinePauser(dbConn, lockFactory)
	dbSigningKeyFactory := db.NewSigningKeyFactory(dbConn)
	dbAgentWorkflowRunsFactory := db.NewAgentWorkflowRunsFactory(dbConn)
	dbAgentChildExecutionsFactory := db.NewAgentChildExecutionsFactory(dbConn)
	dbAgentWorkflowWaitsFactory := db.NewAgentWorkflowWaitsFactory(
		dbConn, cmd.AgentSnapshots.BindingRetention,
	)
	// One admission graph for the whole process. The API side composed it
	// first (it is what creates the default team); this call returns that
	// identical graph, so the dispatcher component, the dispatch route and the
	// terminalizer below all share one binder, canceler and workflow store.
	dispatchGraph, err := cmd.composeAgentDispatch(
		logger, dbConn, lockFactory, teamFactory, dbBuildFactory, dbCheckFactory, dbPipelineRunFactory,
	)
	if err != nil {
		return nil, err
	}
	// The durable per-node projection. It shares dispatchGraph's workflow
	// store deliberately: the freeze resolves each run's own workflow version,
	// and a second store is a second chance for the version a run executed and
	// the version its history describes to disagree.
	nodeOccurrenceFreezer, err := occurrence.NewFreezer(
		db.NewAgentWorkflowRunEvidenceFactory(dbConn),
		dispatchGraph.workflows,
		// A reusable-node run names a NODE definition, which the workflow
		// store's definition_kind = 'workflow' reads cannot see. Without this
		// the freeze failed — loudly, in the log — on every healthy node run.
		dispatchGraph.nodes,
		db.NewAgentWorkflowRunNodeOccurrencesFactory(dbConn),
	)
	if err != nil {
		return nil, fmt.Errorf("construct workflow run node occurrence freezer: %w", err)
	}
	reconcilerOptions := []workflowrun.ReconcilerOption{
		workflowrun.WithWaitCanceler(dbAgentWorkflowWaitsFactory),
		// THE terminalizer: a finished workflow run projects its owning ticket
		// to needs_review from here, in the always-on reconciler, and nowhere
		// else.
		workflowrun.WithTicketProjector(dispatchGraph.projector),
		// Deterministic task steps exist nowhere else once build events are
		// reclaimed, so this is the one call that preserves them.
		workflowrun.WithNodeOccurrenceFreezer(nodeOccurrenceFreezer),
	}
	cmd.agentSnapshotMu.Lock()
	agentSnapshotCreator := cmd.agentSnapshotCreator
	cmd.agentSnapshotMu.Unlock()
	if !isNilDependency(agentSnapshotCreator) {
		waitResolutionCompleter, err := workflowwait.NewResolutionCompleter(
			dbAgentWorkflowWaitsFactory,
			agentSnapshotCreator,
		)
		if err != nil {
			return nil, fmt.Errorf("construct workflow wait resolution completer: %w", err)
		}
		reconcilerOptions = append(
			reconcilerOptions,
			workflowrun.WithWaitResolutionCompleter(waitResolutionCompleter),
		)
	}
	agentWorkflowRunReconciler, err := workflowrun.NewReconciler(
		dbAgentWorkflowRunsFactory,
		logger.Session(atc.ComponentAgentWorkflowRunReconciler),
		cmd.AgentWorkflowRuns.AdmissionTimeout,
		cmd.AgentWorkflowRuns.ReconcilerInterval,
		reconcilerOptions...,
	)
	if err != nil {
		return nil, err
	}
	var sourceBuildReconciler *workflowrun.SourceBuildReconciler
	if dispatchGraph.sourceRuntime.captures != nil && dispatchGraph.sourceRuntime.admitter != nil {
		sourceBuildReconciler, err = newAutomaticSourceBuildReconciler(
			dispatchGraph.teamID,
			dispatchGraph.teamName,
			db.NewWorkflowResourceSourcePipelinesFactory(dbConn),
			db.NewWorkflowResourceSourceBuildStore(dbConn, lockFactory, dbCheckFactory),
			db.NewWorkflowResourceSourceAdmissionStore(dbConn),
			dispatchGraph.workflows,
			dispatchGraph.sourceRuntime.captures,
			dispatchGraph.sourceRuntime.admitter,
			dispatchGraph.binder,
		)
		if err != nil {
			return nil, fmt.Errorf("construct automatic workflow resource source build reconciler: %w", err)
		}
	}

	dbWorkerFactory := db.NewWorkerFactory(dbConn, workerCache)

	alg := algorithm.New(db.NewVersionsDB(dbConn, algorithmLimitRows, schedulerCache))

	// Materialize the shared ArtifactLocator BEFORE constructPool, so the
	// pool's worker factory receives it. Without this, workers have a nil
	// locator and recordOutputLocations silently skips.
	cmd.artifactLocator()

	pool, err := cmd.constructPool(dbConn, lockFactory, workerCache)
	if err != nil {
		return nil, err
	}

	defaultLimits, err := cmd.parseDefaultLimits()
	if err != nil {
		return nil, err
	}

	defaultRequests, err := cmd.parseDefaultRequests()
	if err != nil {
		return nil, err
	}

	rateLimiter := db.NewResourceCheckRateLimiter(
		rate.Limit(cmd.MaxChecksPerSecond),
		rate.Limit(1),
		cmd.ResourceCheckingInterval,
		dbConn,
		time.Minute,
		clock.NewClock(),
	)

	imgResolver := imageresolver.NewResolver(nil)

	engine, err := cmd.constructEngine(
		pool,
		dbWorkerFactory,
		teamFactory,
		dbBuildFactory,
		dbResourceCacheFactory,
		dbResourceConfigFactory,
		secretManager,
		defaultLimits,
		defaultRequests,
		lockFactory,
		rateLimiter,
		policyChecker,
		imgResolver,
		dbConn,
		dbPipelineRunFactory,
	)
	if err != nil {
		return nil, err
	}

	// In case that a user configures resource-checking-interval, but forgets to
	// configure resource-with-webhook-checking-interval, keep both checking-
	// intervals consistent. Even if both intervals are configured, there is no
	// reason webhooked resources take shorter checking interval than normal
	// resources.
	if cmd.ResourceWithWebhookCheckingInterval < cmd.ResourceCheckingInterval {
		logger.Info("update-resource-with-webhook-checking-interval",
			lager.Data{
				"oldValue": cmd.ResourceWithWebhookCheckingInterval,
				"newValue": cmd.ResourceCheckingInterval,
			})
		cmd.ResourceWithWebhookCheckingInterval = cmd.ResourceCheckingInterval
	}

	if cmd.ResourceTypeCheckingInterval < cmd.ResourceCheckingInterval {
		logger.Info("update-resource-type-checking-interval",
			lager.Data{
				"oldValue": cmd.ResourceTypeCheckingInterval,
				"newValue": cmd.ResourceCheckingInterval,
			})
		cmd.ResourceTypeCheckingInterval = cmd.ResourceCheckingInterval
	}

	components := []RunnableComponent{
		{
			Component: atc.Component{
				Name: atc.ComponentLidarScanner,
			},
			Runnable: lidar.NewScanner(
				dbCheckFactory,
				atc.NewPlanFactory(time.Now().Unix()),
				1000,
				imgResolver,
				dbResourceConfigFactory,
			),
		},
		{
			Component: atc.Component{
				Name: atc.ComponentPipelinePauser,
			},
			Runnable: pauser.NewPipelinePauser(
				dbPipelinePauser,
				cmd.PausePipelinesAfter,
			),
		},
		{
			Component: atc.Component{
				Name: atc.ComponentScheduler,
			},
			Runnable: scheduler.NewRunner(
				logger.Session("scheduler"),
				dbJobFactory,
				&scheduler.Scheduler{
					Algorithm: alg,
					BuildStarter: scheduler.NewBuildStarter(
						builds.NewPlanner(atc.NewPlanFactory(time.Now().Unix())),
						alg),
				},
				cmd.JobSchedulingMaxInFlight,
			),
		},
		{
			Component: atc.Component{
				Name: atc.ComponentBuildTracker,
			},
			Runnable: builds.NewTracker(logger, dbBuildFactory, engine, checkBuildsChan),
		},
		{
			Component: atc.Component{
				Name: atc.ComponentBuildReaper,
			},
			Runnable: gc.NewBuildLogCollector(
				dbPipelineFactory,
				dbPipelineLifecycle,
				500,
				gc.NewBuildLogRetentionCalculator(
					cmd.DefaultBuildLogsToRetain,
					cmd.MaxBuildLogsToRetain,
					cmd.DefaultDaysToRetainBuildLogs,
					cmd.MaxDaysToRetainBuildLogs,
				),
				syslogDrainConfigured,
			),
		},
		{
			Component: atc.Component{
				Name: atc.ComponentSigningKeyLifecycler,
			},
			Runnable: &idtoken.SigningKeyLifecycler{
				Logger:              logger.Session(atc.ComponentSigningKeyLifecycler),
				DBSigningKeyFactory: dbSigningKeyFactory,
				KeyRotationPeriod:   cmd.SigningKey.RotationPeriod,
				KeyGracePeriod:      cmd.SigningKey.GracePeriod,
			},
		},
		{
			Component: atc.Component{
				Name: atc.ComponentPipelineRunLifecycler,
			},
			Runnable: runlifecycle.NewLifecycler(dbPipelineRunFactory, cmd.GC.WorkflowRunTemplateRetirementPeriod),
		},
		{
			Component: atc.Component{
				Name: atc.ComponentAgentWorkflowRunReconciler,
			},
			Runnable: agentWorkflowRunReconciler,
			Interval: cmd.AgentWorkflowRuns.ReconcilerInterval,
		},
	}
	if dispatchGraph.sourceLifecycle != nil {
		components = append(components, RunnableComponent{
			Component: atc.Component{Name: atc.ComponentAgentWorkflowResourceSourceLifecycle},
			Runnable:  dispatchGraph.sourceLifecycle,
			Interval:  cmd.AgentWorkflowRuns.ReconcilerInterval,
		})
	}
	if sourceBuildReconciler != nil {
		components = append(components, RunnableComponent{
			Component: atc.Component{Name: atc.ComponentAgentWorkflowResourceSourceBuildReconciler},
			Runnable:  component.RunFunc(sourceBuildReconciler.Reconcile),
			Interval:  cmd.AgentWorkflowRuns.ReconcilerInterval,
		})
	}
	if cmd.AgentChildExecutions.Enabled {
		components = append(components, RunnableComponent{
			Component: atc.Component{Name: atc.ComponentAgentChildExecutionReconciler},
			Runnable: component.RunFunc(func(ctx context.Context) error {
				_, err := dbAgentChildExecutionsFactory.ReconcileExpiredLeases(ctx, cmd.AgentChildExecutions.ReconcilerBatchSize)
				return err
			}),
			Interval: cmd.AgentChildExecutions.ReconcilerInterval,
		})
	}

	idtoken.UpdateGlobalManagerFactory(func(f *idtoken.ManagerFactory) {
		f.SetSigningKeyFactory(dbSigningKeyFactory)
	})

	// The shared ArtifactLocator for DaemonSet mode is created above, before
	// constructPool, and is reused here for the Reaper. Do not re-create it:
	// constructPool has already handed the existing instance to the worker
	// factory, so a fresh one here would leave the Reaper reading an empty map
	// that no worker ever writes to, and artifact cleanup would never run.
	if cmd.Kubernetes.Namespace != "" {
		resolveCapabilityKey, err := cmd.loadArtifactResolveCapabilityKey()
		if err != nil {
			return nil, fmt.Errorf("load artifact resolve capability key: %w", err)
		}
		k8sCfg := jetbridge.NewConfig(cmd.Kubernetes.Namespace, cmd.Kubernetes.Kubeconfig)
		k8sCfg.PodStartupTimeout = cmd.Kubernetes.PodStartupTimeout
		k8sCfg.PodSchedulingTimeout = cmd.Kubernetes.PodSchedulingTimeout
		k8sCfg.ImagePullSecrets = cmd.Kubernetes.ImagePullSecrets
		k8sCfg.ServiceAccount = cmd.Kubernetes.ServiceAccount
		k8sCfg.CacheStore = cmd.Kubernetes.CacheStore
		k8sCfg.CacheHostPath = cmd.Kubernetes.CacheHostPath
		k8sCfg.ArtifactHelperImage = cmd.Kubernetes.ArtifactHelperImage
		k8sCfg.ArtifactDaemonPort = cmd.Kubernetes.ArtifactDaemonPort
		k8sCfg.ArtifactDaemonHostPath = cmd.Kubernetes.ArtifactDaemonHostPath
		k8sCfg.ArtifactDaemonService = cmd.Kubernetes.ArtifactDaemonService
		k8sCfg.ArtifactDaemonTLSCert = cmd.Kubernetes.ArtifactDaemonTLSCert
		k8sCfg.ArtifactDaemonTLSKey = cmd.Kubernetes.ArtifactDaemonTLSKey
		k8sCfg.ArtifactDaemonTLSCACert = cmd.Kubernetes.ArtifactDaemonTLSCACert
		k8sCfg.ArtifactDaemonTLSEnabled = cmd.Kubernetes.ArtifactDaemonTLSCert != ""
		k8sCfg.ArtifactDaemonResolveCapabilityKey = resolveCapabilityKey
		k8sCfg.ArtifactDaemonResolveCapabilityTTL = cmd.Kubernetes.ArtifactDaemonResolveCapabilityTTL
		if cmd.Kubernetes.CacheStore != "" && !jetbridge.ValidCacheStores[cmd.Kubernetes.CacheStore] {
			return nil, fmt.Errorf("invalid --kubernetes-cache-store value %q (valid: hostpath, emptydir)", cmd.Kubernetes.CacheStore)
		}
		if cmd.Kubernetes.ImageRegistryPrefix != "" || cmd.Kubernetes.ImageRegistrySecret != "" {
			k8sCfg.ImageRegistry = &jetbridge.ImageRegistryConfig{
				Prefix:     cmd.Kubernetes.ImageRegistryPrefix,
				SecretName: cmd.Kubernetes.ImageRegistrySecret,
			}
		}
		if len(cmd.Kubernetes.BaseResourceTypes) > 0 {
			k8sCfg.ResourceTypeImages = jetbridge.MergeResourceTypeImages(cmd.Kubernetes.BaseResourceTypes)
		}
		k8sClientset, err := jetbridge.NewClientset(k8sCfg)
		if err != nil {
			return nil, fmt.Errorf("creating k8s clientset for registrar: %w", err)
		}
		components = append(components, RunnableComponent{
			Component: atc.Component{
				Name: atc.ComponentK8sWorkerRegistrar,
			},
			Runnable: jetbridge.NewRegistrar(logger.Session(atc.ComponentK8sWorkerRegistrar), k8sClientset, k8sCfg, dbWorkerFactory),
			Interval: 15 * time.Second, // heartbeat TTL is 30s, so re-register every 15s
		})

		k8sContainerRepo := db.NewContainerRepository(dbConn)
		k8sVolumeRepo := db.NewVolumeRepository(dbConn)
		k8sDestroyer := gc.NewDestroyer(logger, k8sContainerRepo, k8sVolumeRepo)
		k8sReaper := jetbridge.NewReaper(logger.Session(atc.ComponentK8sWorkerReaper), k8sClientset, k8sCfg, k8sContainerRepo, k8sDestroyer)
		// The reaper reaps a completed step's pod only once its build is no
		// longer running: until then the pod's exit-status annotation is the
		// only thing that lets a restarted web resume the plan instead of
		// re-executing the step.
		k8sReaper.SetBuildLookup(dbBuildFactory)
		k8sReaper.SetArtifactLocator(cmd.artifactLocator())
		components = append(components, RunnableComponent{
			Component: atc.Component{
				Name: atc.ComponentK8sWorkerReaper,
			},
			Runnable: k8sReaper,
		})

		components = append(components, RunnableComponent{
			Component: atc.Component{
				Name: atc.ComponentAgentPlatformCredentialSyncer,
			},
			Runnable: credentials.NewPlatformSecretSyncer(
				logger.Session(atc.ComponentAgentPlatformCredentialSyncer),
				db.NewAgentUserCredentialsFactory(dbConn),
				k8sClientset,
				cmd.Kubernetes.Namespace,
			),
			Interval: time.Minute,
		})

		// The dispatcher component is ALWAYS wired: the seeded agent_settings
		// row is the only control, read HOT on the loop's next tick, and an
		// admin flips it at runtime via PUT /api/v1/agent/dispatcher. The
		// component ONLY dispatches queued tickets — terminalizing a ticket
		// whose run finished is the always-on workflow-run reconciler's job,
		// so no dispatcher mode can strand a running ticket.
		//
		// NOTE for the human: housekeeping components (e.g. ticket #42's
		// unmerged pipeline-archiver) MUST wire independently of this dispatch
		// mode — they are NOT part of the dispatch loop and must keep running
		// even when the dispatcher is paused/off. Do not fold them into any
		// dispatch-mode conditional.
		{
			dispatcherSettings := db.NewAgentSettingsFactory(dbConn)
			modeResolver := func() string {
				mode, found, err := dispatcherSettings.GetDispatcherMode()
				if err != nil {
					// Fail-safe: a read fault must never auto-dispatch against
					// an admin's explicit pause/off. EffectiveModeFromRead
					// returns ModePaused on error.
					logger.Error("failed-to-read-dispatcher-mode", err)
				}
				return dispatch.EffectiveModeFromRead(mode, found, err)
			}

			components = append(components, RunnableComponent{
				Component: atc.Component{
					Name: atc.ComponentAgentDispatcher,
				},
				Runnable: dispatch.NewDispatcher(dispatchGraph.deps, dispatch.LoopConfig{
					Mode: modeResolver,
				}),
				// Interval deliberately omitted: defaultComponentInterval (10s)
				// polling — agent_tickets has no NOTIFY trigger, and this fork
				// never runs a component notify-only (see docs/agentic/README.md
				// and the dropped-notification rule in CLAUDE.md).
			})
		}
	}

	if syslogDrainConfigured {
		components = append(components, RunnableComponent{
			Component: atc.Component{
				Name: atc.ComponentSyslogDrainer,
			},
			Runnable: syslog.NewDrainer(
				cmd.Syslog.Transport,
				cmd.Syslog.Address,
				cmd.Syslog.Hostname,
				cmd.Syslog.CACerts,
				dbBuildFactory,
			),
		})
	}

	snapshotLifecycleComponents, err := cmd.agentSnapshotLifecycleComponents()
	if err != nil {
		return nil, err
	}
	components = append(components, snapshotLifecycleComponents...)

	experimentComponents, err := cmd.agentExperimentComponents(
		dbConn,
		lockFactory,
		teamFactory,
		dbBuildFactory,
		dbPipelineRunFactory,
	)
	if err != nil {
		return nil, err
	}
	components = append(components, experimentComponents...)

	return components, err
}

// agentDispatchGraph is the ONE ticket/workflow admission object graph. The
// dispatcher component and the POST .../dispatch route both admit through the
// exact same binder, canceler, work-item capturer and promotion-validated
// workflow store; the terminalizer that projects a finished run back onto its
// ticket is built here too, so all three see one truth.
//
// Two graphs meant two chances to diverge, and they had diverged: the
// component's workflow store was built with NO promotion validator (so a
// schema-v3 promotion resolved differently depending on which side asked) and
// its wait store with no binding retention.
type agentDispatchGraph struct {
	teamID   int
	teamName string

	targetRenderer  workflowrun.WorkflowTargetRenderer
	workflows       db.AgentWorkflowsFactory
	nodes           db.AgentNodesFactory
	runs            db.AgentWorkflowRunsFactory
	snapshots       db.AgentSnapshotsFactory
	waits           db.AgentWorkflowWaitsFactory
	templates       *workflowrun.TemplateSaver
	binder          *workflowrun.Binder
	canceler        *workflowrun.Canceler
	tickets         db.AgentTicketsFactory
	projector       *dispatch.TicketProjector
	sourceRuntime   workflowResourceSourceRuntime
	sourceLifecycle component.Runnable

	// deps is what both dispatch entry points pass to DispatchOne. It is a
	// value, but every field in it is one of the shared singletons above.
	deps dispatch.Deps
}

// composeAgentDispatch builds the admission graph once and memoises it. The
// FIRST caller wins and every later caller gets that identical graph, so the
// arguments must describe the same cluster (they do: one process, one DB).
//
// Ordering is not accidental: constructAPIMembers runs before
// backendComponents and is what creates the default team, which this
// composition requires — so the API side composes and the dispatcher component
// reuses. That also fixes which connection pool admission runs on: the API
// one. The alternative — composing on the backend pool — would mean minting a
// SECOND pipeline-run factory (and a second check factory behind it) for the
// same connection, trading the divergence this graph exists to remove for a
// different one.
func (cmd *RunCommand) composeAgentDispatch(
	logger lager.Logger,
	conn db.DbConn,
	lockFactory lock.LockFactory,
	teamFactory db.TeamFactory,
	buildFactory db.BuildFactory,
	checkFactory db.CheckFactory,
	pipelineRunFactory db.PipelineRunFactory,
) (*agentDispatchGraph, error) {
	cmd.agentDispatchMu.Lock()
	defer cmd.agentDispatchMu.Unlock()
	if cmd.agentDispatchGraph != nil {
		return cmd.agentDispatchGraph, nil
	}
	if logger == nil || conn == nil || lockFactory == nil || teamFactory == nil ||
		buildFactory == nil || checkFactory == nil || pipelineRunFactory == nil {
		return nil, errors.New("compose ticket dispatch: incomplete dependencies")
	}

	mainTeam, found, err := teamFactory.FindTeam(atc.DefaultTeamName)
	if err != nil {
		return nil, fmt.Errorf("resolve main team for ticket dispatch: %w", err)
	}
	if !found || mainTeam == nil || mainTeam.ID() <= 0 || mainTeam.Name() != atc.DefaultTeamName {
		return nil, errors.New("resolve main team for ticket dispatch: main team is unavailable")
	}

	targetRenderer := workflowrun.WorkflowTargetRenderer{RuntimeImage: cmd.AgentStepImage}
	brokerCatalog, err := loadAgentBrokerCatalog(cmd.AgentChildExecutions.BrokerCatalog.Path())
	if err != nil {
		return nil, err
	}
	var nodeStore db.AgentNodesFactory
	if brokerCatalog == nil {
		nodeStore = db.NewAgentNodesFactory(conn)
	} else {
		nodeStore = db.NewAgentNodesFactoryWithBrokerCatalog(conn, brokerCatalog)
	}
	workflowStore, sourceLifecycle, err := newWorkflowResourceSourceCompositionWithBrokerCatalog(
		conn, mainTeam.ID(), targetRenderer, nodeStore, brokerCatalog,
	)
	if err != nil {
		return nil, fmt.Errorf("construct workflow resource source composition: %w", err)
	}
	graph := &agentDispatchGraph{
		teamID:          mainTeam.ID(),
		teamName:        mainTeam.Name(),
		targetRenderer:  targetRenderer,
		workflows:       workflowStore,
		nodes:           nodeStore,
		runs:            db.NewAgentWorkflowRunsFactory(conn),
		snapshots:       db.NewAgentSnapshotsFactory(conn),
		waits:           db.NewAgentWorkflowWaitsFactory(conn, cmd.AgentSnapshots.BindingRetention),
		tickets:         db.NewAgentTicketsFactory(conn),
		sourceLifecycle: sourceLifecycle,
	}

	graph.templates, err = workflowrun.NewTemplateSaver(
		teamFactory,
		db.NewWorkflowRunTemplateFactory(conn, lockFactory),
	)
	if err != nil {
		return nil, fmt.Errorf("construct workflow-run template saver: %w", err)
	}
	graph.sourceRuntime, err = cmd.newWorkflowResourceSourceRuntime(
		logger,
		conn,
		lockFactory,
		teamFactory,
		mainTeam,
		checkFactory,
		pipelineRunFactory,
		graph.workflows,
		graph.snapshots,
		graph.templates,
	)
	if err != nil {
		return nil, err
	}
	budget, err := workflowrun.NewGlobalDailyBudgetAdmitter(
		db.NewAgentWorkflowBudgetReservationsFactory(conn, db.AgentWorkflowBudgetConfig{
			GlobalDailyCapUSD: cmd.AgentDailyBudgetUSD,
			Location:          time.Local,
			Now:               time.Now,
		}),
		cmd.AgentDailyBudgetUSD,
	)
	if err != nil {
		return nil, fmt.Errorf("construct workflow-run budget admission: %w", err)
	}
	credential, err := workflowrun.NewPlatformCredentialAdmitter(
		db.NewAgentUserCredentialsFactory(conn),
		cmd.AgentPlatformTokenSecret,
	)
	if err != nil {
		return nil, fmt.Errorf("construct workflow-run model credential admission: %w", err)
	}
	binderOptions := []workflowrun.BinderOption{
		workflowrun.WithNodeStore(graph.nodes),
	}
	if graph.sourceRuntime.admitter != nil {
		binderOptions = append(
			binderOptions,
			workflowrun.WithResourceSourceAdmitter(graph.sourceRuntime.admitter),
		)
	}
	graph.binder, err = workflowrun.NewBinder(
		workflowrun.WorkflowDefinitionStoreResolver{Store: graph.workflows},
		targetRenderer,
		graph.snapshots,
		graph.runs,
		budget,
		graph.templates,
		pipelineRunFactory,
		credential,
		binderOptions...,
	)
	if err != nil {
		return nil, fmt.Errorf("construct workflow-run binder: %w", err)
	}
	graph.canceler, err = workflowrun.NewCancelerWithWaits(graph.runs, buildFactory, graph.waits)
	if err != nil {
		return nil, fmt.Errorf("construct workflow-run canceler: %w", err)
	}
	graph.projector, err = dispatch.NewTicketProjector(graph.tickets)
	if err != nil {
		return nil, fmt.Errorf("construct ticket terminalizer: %w", err)
	}

	graph.deps = dispatch.Deps{
		Tickets:          graph.tickets,
		Workflows:        graph.workflows,
		TeamID:           graph.teamID,
		TeamName:         graph.teamName,
		WorkflowBinder:   graph.binder,
		WorkflowCanceler: graph.canceler,
	}
	if cmd.AgentSnapshots.Enabled {
		cmd.agentSnapshotMu.Lock()
		snapshotCreator := cmd.agentSnapshotCreator
		cmd.agentSnapshotMu.Unlock()
		if isNilDependency(snapshotCreator) {
			return nil, errors.New("construct ticket work-item capturer: snapshot creator is unavailable")
		}
		capturer, err := workitem.NewCapturer(
			graph.tickets,
			snapshotCreator,
			workitem.Authority{
				TeamID: graph.teamID, TeamName: graph.teamName,
				Actor: "ticket-dispatch-adapter", DisplayName: "ticket-dispatch-adapter",
			},
		)
		if err != nil {
			return nil, fmt.Errorf("construct ticket work-item capturer: %w", err)
		}
		graph.deps.WorkItems = capturer
	}

	cmd.agentDispatchGraph = graph
	return graph, nil
}

func (cmd *RunCommand) compression() compression.Compression {
	switch cmd.StreamingArtifactsCompression {
	case "zstd":
		return compression.NewZstdCompression()
	case "s2":
		return compression.NewS2Compression()
	case "raw":
		return compression.NewNoCompression()
	default:
		return compression.NewGzipCompression()
	}
}

func (cmd *RunCommand) streamer() worker.Streamer {
	return worker.NewStreamer(cmd.compression())
}

func (cmd *RunCommand) composeAgentSnapshots(connection db.DbConn, logger lager.Logger) error {
	cmd.agentSnapshotMu.Lock()
	defer cmd.agentSnapshotMu.Unlock()
	if !cmd.AgentSnapshots.Enabled {
		return nil
	}
	initialized := cmd.agentSnapshotDaemonClient != nil ||
		cmd.agentSnapshotContentStore != nil ||
		cmd.agentSnapshotMetadataStore != nil ||
		cmd.agentSnapshotWorkflowRuns != nil ||
		cmd.agentSnapshotDigestLocker != nil ||
		cmd.agentSnapshotValidatorRegistry != nil ||
		cmd.agentSnapshotOutputSealer != nil ||
		cmd.agentSnapshotCreator != nil ||
		cmd.agentSnapshotLifecycle != nil ||
		cmd.agentSnapshotPublisher != nil ||
		cmd.agentResourceCaptureFinalizer != nil ||
		cmd.agentSnapshotHandlerFactory != nil ||
		cmd.agentSnapshotArchiveLimits != (snapshot.ArchiveLimits{})
	if initialized {
		if cmd.agentSnapshotDaemonClient == nil ||
			cmd.agentSnapshotContentStore == nil ||
			cmd.agentSnapshotMetadataStore == nil ||
			cmd.agentSnapshotWorkflowRuns == nil ||
			cmd.agentSnapshotDigestLocker == nil ||
			cmd.agentSnapshotValidatorRegistry == nil ||
			cmd.agentSnapshotOutputSealer == nil ||
			cmd.agentSnapshotCreator == nil ||
			cmd.agentSnapshotLifecycle == nil ||
			cmd.agentResourceCaptureFinalizer == nil ||
			cmd.agentSnapshotHandlerFactory == nil ||
			cmd.agentSnapshotArchiveLimits == (snapshot.ArchiveLimits{}) {
			return fmt.Errorf("snapshot command composition is partially initialized")
		}
		return nil
	}
	if connection == nil {
		return fmt.Errorf("snapshot metadata database connection is required")
	}
	if logger == nil {
		return fmt.Errorf("snapshot production logger is required")
	}

	archiveLimits := cmd.configuredAgentSnapshotArchiveLimits()
	metadataStore := db.NewAgentSnapshotsFactory(connection)
	workflowRuns := db.NewAgentWorkflowRunsFactory(connection)
	digestLocker := db.NewAgentSnapshotDigestLocker(connection)
	canonicalizer := snapshot.Canonicalizer{
		MaxContentBytes: archiveLimits.MaxContentBytes,
		MaxEntries:      archiveLimits.MaxEntries,
		TempDir:         cmd.AgentSnapshots.TempDir,
	}
	validatorRegistry, err := contracts.NewRegistry(contracts.WithCanonicalizer(canonicalizer))
	if err != nil {
		return fmt.Errorf("compose agent snapshot validator registry: %w", err)
	}
	composer := cmd.agentSnapshotComposer
	if composer == nil {
		composer = cmd.buildAgentSnapshotComponents
	}
	daemonClient, contentStore, err := composer(connection, metadataStore, archiveLimits)
	if err != nil {
		return fmt.Errorf("compose agent snapshot storage: %w", err)
	}
	if daemonClient == nil || isNilDependency(contentStore) {
		return fmt.Errorf("compose agent snapshot storage returned incomplete components")
	}
	var snapshotPublisher publisher.Executor
	publisherComposer := cmd.agentSnapshotPublisherComposer
	if publisherComposer == nil && cmd.AgentPublisher.Enabled {
		publisherComposer = cmd.buildAgentPublisher
	}
	if publisherComposer != nil {
		snapshotPublisher, err = publisherComposer(
			db.NewAgentPublicationsFactory(connection),
			metadataStore,
			contentStore,
		)
		if err != nil {
			return fmt.Errorf("compose agent snapshot publisher: %w", err)
		}
		if isNilDependency(snapshotPublisher) {
			return fmt.Errorf("compose agent snapshot publisher returned no executor")
		}
	}
	sealerComposer := cmd.agentSnapshotSealerComposer
	if sealerComposer == nil {
		sealerComposer = buildAgentSnapshotSealer
	}
	creator, err := sealerComposer(
		canonicalizer,
		validatorRegistry,
		metadataStore,
		contentStore,
		digestLocker,
		cmd.AgentSnapshots.OrphanGracePeriod,
		cmd.AgentSnapshots.BindingRetention,
	)
	if err != nil {
		return fmt.Errorf("compose agent snapshot output sealer: %w", err)
	}
	if isNilDependency(creator) {
		return fmt.Errorf("compose agent snapshot output sealer returned no sealer")
	}
	reviewProjector, err := projection.NewReviewProjector(db.NewAgentReviewsFactory(connection), contentStore)
	if err != nil {
		return fmt.Errorf("compose review snapshot projector: %w", err)
	}
	repositoryChanges := db.NewAgentRepositoryChangesFactory(connection)
	repositoryChangeProjector, err := projection.NewRepositoryChangeProjector(
		repositoryChanges,
		contentStore,
		projection.WithRepositoryChangeCanonicalizer(canonicalizer),
	)
	if err != nil {
		return fmt.Errorf("compose repository-change snapshot projector: %w", err)
	}
	projectionRegistry, err := projection.NewRegistry(
		[]projection.Handler{reviewProjector, repositoryChangeProjector},
		projection.WithErrorReporter(func(_ context.Context, projectionErr error) {
			logger.Error("agent-snapshot-projection-failed", projectionErr)
		}),
	)
	if err != nil {
		return fmt.Errorf("compose agent snapshot projectors: %w", err)
	}
	creator, err = projection.NewProjectingCreator(creator, projectionRegistry)
	if err != nil {
		return fmt.Errorf("compose post-seal projection trigger: %w", err)
	}
	replicaRepairer, ok := contentStore.(snapshot.ReplicaRepairer)
	if !ok || isNilDependency(replicaRepairer) {
		return fmt.Errorf("compose agent snapshot storage returned no replica repairer")
	}
	lifecycleComposer := cmd.agentSnapshotLifecycleComposer
	if lifecycleComposer == nil {
		lifecycleComposer = buildAgentSnapshotLifecycle
	}
	lifecycle, err := lifecycleComposer(metadataStore, contentStore, replicaRepairer, digestLocker)
	if err != nil {
		return fmt.Errorf("compose agent snapshot lifecycle: %w", err)
	}
	if isNilDependency(lifecycle) {
		return fmt.Errorf("compose agent snapshot lifecycle returned no lifecycle")
	}
	captureFinder, ok := metadataStore.(resourcecapture.CaptureOutputFinder)
	if !ok || isNilDependency(captureFinder) {
		return fmt.Errorf("compose resource capture finalizer: snapshot metadata store lacks exact output lookup")
	}
	capturePending, ok := metadataStore.(resourcecapture.PendingOutputLister)
	if !ok || isNilDependency(capturePending) {
		return fmt.Errorf("compose resource capture finalizer: snapshot metadata store lacks pending output lookup")
	}
	captureOutputs, err := resourcecapture.NewOutputStore(captureFinder, metadataStore, digestLocker)
	if err != nil {
		return fmt.Errorf("compose resource capture finalizer output store: %w", err)
	}
	captureFinalizer, err := resourcecapture.NewFinalizer(capturePending, captureOutputs)
	if err != nil {
		return fmt.Errorf("compose resource capture finalizer: %w", err)
	}
	handlerFactory, err := snapshotsapi.NewHandlerFactory(snapshotsapi.Config{
		Logger:            logger.Session("snapshots-api"),
		Enabled:           true,
		Creator:           creator,
		Metadata:          metadataStore,
		Content:           contentStore,
		RepositoryChanges: repositoryChanges,
		Locks:             digestLocker,
		ArchiveLimits:     archiveLimits,
		TempDir:           cmd.AgentSnapshots.TempDir,
		Identity: func(request *http.Request) (snapshotsapi.RequestIdentity, error) {
			return agentSnapshotIdentity(accessor.GetAccessor(request).Claims())
		},
		ReportError: agentSnapshotStreamErrorReporter(logger),
	})
	if err != nil {
		return fmt.Errorf("compose agent snapshot API: %w", err)
	}
	if handlerFactory == nil {
		return fmt.Errorf("compose agent snapshot API returned no handler factory")
	}

	cmd.agentSnapshotDaemonClient = daemonClient
	cmd.agentSnapshotContentStore = contentStore
	cmd.agentSnapshotMetadataStore = metadataStore
	cmd.agentSnapshotWorkflowRuns = workflowRuns
	cmd.agentSnapshotDigestLocker = digestLocker
	cmd.agentSnapshotValidatorRegistry = validatorRegistry
	cmd.agentSnapshotOutputSealer = creator
	cmd.agentSnapshotCreator = creator
	cmd.agentSnapshotLifecycle = lifecycle
	cmd.agentSnapshotPublisher = snapshotPublisher
	cmd.agentSnapshotProjectionRegistry = projectionRegistry
	cmd.agentResourceCaptureFinalizer = captureFinalizer
	cmd.agentSnapshotHandlerFactory = handlerFactory
	cmd.agentSnapshotArchiveLimits = archiveLimits
	return nil
}

func agentSnapshotStreamErrorReporter(logger lager.Logger) snapshotsapi.ErrorReporter {
	apiLogger := logger.Session("agent-snapshot-api")
	return func(_ context.Context, category string) {
		if category != "snapshot_content_stream_failed" {
			category = "unknown"
		}
		apiLogger.Error(
			"content-stream-failed",
			errors.New("snapshot content stream verification failed"),
			lager.Data{"category": category},
		)
	}
}

func (cmd *RunCommand) configuredAgentSnapshotArchiveLimits() snapshot.ArchiveLimits {
	return snapshot.ArchiveLimits{
		MaxContentBytes: cmd.AgentSnapshots.MaxBytes,
		MaxEntries:      cmd.AgentSnapshots.MaxFiles,
	}
}

func (cmd *RunCommand) buildAgentSnapshotComponents(
	connection db.DbConn,
	metadataStore db.AgentSnapshotsFactory,
	archiveLimits snapshot.ArchiveLimits,
) (*jetbridge.DaemonClient, snapshot.ContentStore, error) {
	if connection == nil {
		return nil, nil, fmt.Errorf("snapshot metadata database connection is required")
	}
	if metadataStore == nil {
		return nil, nil, fmt.Errorf("snapshot metadata store is required")
	}
	if cmd.Kubernetes.ArtifactDaemonTLSCert == "" || cmd.Kubernetes.ArtifactDaemonTLSKey == "" || cmd.Kubernetes.ArtifactDaemonTLSCACert == "" {
		return nil, nil, fmt.Errorf("snapshot daemon mTLS certificate, key, and CA certificate are required")
	}
	k8sConfig := jetbridge.NewConfig(cmd.Kubernetes.Namespace, cmd.Kubernetes.Kubeconfig)
	k8sConfig.ArtifactDaemonPort = cmd.Kubernetes.ArtifactDaemonPort
	k8sConfig.ArtifactDaemonService = cmd.Kubernetes.ArtifactDaemonService
	k8sClientset, err := jetbridge.NewClientset(k8sConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("create snapshot daemon discovery client: %w", err)
	}
	daemonPort := k8sConfig.ArtifactDaemonPort
	if daemonPort == 0 {
		daemonPort = 7780
	}
	daemonTLSConfig := &jetbridge.DaemonClientTLSConfig{
		CertPath:   cmd.Kubernetes.ArtifactDaemonTLSCert,
		KeyPath:    cmd.Kubernetes.ArtifactDaemonTLSKey,
		CACertPath: cmd.Kubernetes.ArtifactDaemonTLSCACert,
	}
	daemonLogger := lager.NewLogger("snapshot-daemon-client")
	daemonLogger.RegisterSink(lager.NewWriterSink(os.Stderr, lager.INFO))
	daemonClient, err := jetbridge.NewDaemonClientChecked(
		daemonLogger,
		k8sClientset,
		k8sConfig.Namespace,
		k8sConfig.ArtifactDaemonService,
		daemonPort,
		daemonTLSConfig,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("construct checked snapshot daemon client: %w", err)
	}
	contentStore, err := jetbridge.NewSnapshotContentStore(
		daemonClient,
		metadataStore,
		cmd.AgentSnapshots.ReplicationFactor,
		archiveLimits,
		jetbridge.WithSnapshotContentTempDir(cmd.AgentSnapshots.TempDir),
		// The durable-metadata repair budget refills once per repair pass, so
		// it has to track the interval the repair component actually runs at.
		jetbridge.WithSnapshotDurableMetadataRepairBudget(4, cmd.AgentSnapshots.RepairInterval),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("construct snapshot content store: %w", err)
	}
	return daemonClient, contentStore, nil
}

func buildAgentSnapshotSealer(
	canonicalizer snapshot.Canonicalizer,
	registry snapshot.ValidatorRegistry,
	metadataStore snapshot.MetadataStore,
	contentStore snapshot.ContentStore,
	digestLocker snapshot.DigestLockManager,
	stageTTL time.Duration,
	bindingRetention time.Duration,
) (snapshot.SnapshotCreator, error) {
	return snapshot.NewBatchSealer(
		canonicalizer,
		registry,
		metadataStore,
		contentStore,
		digestLocker,
		snapshot.WithBatchSealerStageTTL(stageTTL),
		snapshot.WithBatchSealerBindingRetention(bindingRetention),
	)
}

func buildAgentSnapshotLifecycle(
	metadata snapshot.MetadataStore,
	content snapshot.ContentStore,
	replicas snapshot.ReplicaRepairer,
	locks snapshot.DigestLockManager,
) (snapshotLifecycle, error) {
	return snapshot.NewLifecycle(metadata, content, replicas, locks)
}

func (cmd *RunCommand) agentSnapshotLifecycleComponents() ([]RunnableComponent, error) {
	if !cmd.AgentSnapshots.Enabled {
		return nil, nil
	}
	cmd.agentSnapshotMu.Lock()
	lifecycle := cmd.agentSnapshotLifecycle
	projectionRegistry := cmd.agentSnapshotProjectionRegistry
	captureFinalizer := cmd.agentResourceCaptureFinalizer
	cmd.agentSnapshotMu.Unlock()
	if lifecycle == nil {
		return nil, fmt.Errorf("snapshot lifecycle is not composed")
	}
	components := make([]RunnableComponent, 0, 4)
	if captureFinalizer != nil {
		components = append(components, RunnableComponent{
			Component: atc.Component{Name: atc.ComponentAgentResourceCaptureFinalizer},
			Runnable:  captureFinalizer,
		})
	}
	components = append(components,
		RunnableComponent{
			Component: atc.Component{Name: atc.ComponentAgentSnapshotGC},
			Runnable: component.RunFunc(func(ctx context.Context) error {
				return runAgentSnapshotLifecyclePass(ctx, "collect", lifecycle.Collect)
			}),
			Interval: cmd.AgentSnapshots.GCInterval,
		},
		RunnableComponent{
			Component: atc.Component{Name: atc.ComponentAgentSnapshotRepair},
			Runnable: component.RunFunc(func(ctx context.Context) error {
				return runAgentSnapshotLifecyclePass(ctx, "repair", lifecycle.Repair)
			}),
			Interval: cmd.AgentSnapshots.RepairInterval,
		},
	)
	if sweepComponent, err := cmd.agentSnapshotOrphanSweepComponent(lifecycle); err != nil {
		return nil, err
	} else if sweepComponent != nil {
		components = append(components, *sweepComponent)
	}
	if projectionRegistry != nil {
		projectionInterval := min(cmd.AgentSnapshots.RepairInterval, time.Minute)
		components = append(components, RunnableComponent{
			Component: atc.Component{Name: atc.ComponentAgentSnapshotProjection},
			Runnable: component.RunFunc(func(ctx context.Context) error {
				if err := projectionRegistry.Reconcile(ctx, 100); err != nil {
					lagerctx.FromContext(ctx).Session("agent-snapshot-projection").Info("pass-failed")
					return errAgentSnapshotProjectionPass
				}
				return nil
			}),
			Interval: projectionInterval,
		})
	}
	return components, nil
}

// agentSnapshotOrphanSweepComponent returns nil when the sweep is not
// configured or cannot be supported, so an unsupported deployment simply does
// not run the pass. A configured-but-unsupportable sweep is an error: silently
// not reclaiming is what allowed the store to fill in the first place.
func (cmd *RunCommand) agentSnapshotOrphanSweepComponent(
	lifecycle snapshotLifecycle,
) (*RunnableComponent, error) {
	// An unset mode is "not configured", which is off. Only a non-empty value
	// the flag parser did not produce is a real misconfiguration.
	configured := strings.TrimSpace(cmd.AgentSnapshots.OrphanSweepMode)
	if configured == "" {
		return nil, nil
	}
	mode := snapshot.OrphanSweepMode(configured)
	if err := mode.Validate(); err != nil {
		return nil, err
	}
	if mode == snapshot.OrphanSweepOff {
		return nil, nil
	}
	if cmd.AgentSnapshots.OrphanSweepAge < snapshot.MinOrphanSweepAge {
		return nil, fmt.Errorf(
			"--agent-snapshot-orphan-sweep-age must be at least %s", snapshot.MinOrphanSweepAge,
		)
	}
	if cmd.AgentSnapshots.OrphanSweepInterval <= 0 {
		return nil, fmt.Errorf("--agent-snapshot-orphan-sweep-interval must be positive")
	}
	sweeper, ok := lifecycle.(snapshotOrphanSweeper)
	if !ok {
		return nil, fmt.Errorf("snapshot lifecycle does not support durable orphan sweeping")
	}
	cmd.agentSnapshotMu.Lock()
	contentStore := cmd.agentSnapshotContentStore
	cmd.agentSnapshotMu.Unlock()
	inventory, ok := contentStore.(snapshot.DurableInventory)
	if !ok {
		return nil, fmt.Errorf("snapshot content store does not expose a durable inventory")
	}
	age := cmd.AgentSnapshots.OrphanSweepAge
	return &RunnableComponent{
		Component: atc.Component{Name: atc.ComponentAgentSnapshotOrphanSweep},
		Runnable: component.RunFunc(func(ctx context.Context) error {
			return runAgentSnapshotLifecyclePass(ctx, "orphan-sweep",
				func(ctx context.Context) (snapshot.LifecycleReport, error) {
					return sweeper.SweepOrphans(ctx, inventory, mode, age)
				})
		}),
		Interval: cmd.AgentSnapshots.OrphanSweepInterval,
	}, nil
}

var errAgentSnapshotLifecyclePass = errors.New("agent snapshot lifecycle pass failed")
var errAgentSnapshotProjectionPass = errors.New("agent snapshot projection pass failed")

func runAgentSnapshotLifecyclePass(
	ctx context.Context,
	operation string,
	run func(context.Context) (snapshot.LifecycleReport, error),
) error {
	report, err := run(ctx)
	data := lager.Data{
		"operation":         operation,
		"scanned":           report.Scanned,
		"deferred":          report.Deferred,
		"failed":            report.Failed,
		"stages_removed":    report.StagesRemoved,
		"digests_expired":   report.DigestsExpired,
		"locations_deleted": report.LocationsDeleted,
		"locations_added":   report.LocationsAdded,
		"stale_pruned":      report.StalePruned,
		// Reclamation of durable content is never silent: every pass reports
		// what it found and what it removed, including a zero.
		"orphans_reclaimable": report.OrphansReclaimable,
		"orphans_reclaimed":   report.OrphansReclaimed,
		"orphan_bytes":        report.OrphanBytes,
	}
	logger := lagerctx.FromContext(ctx).Session("agent-snapshot-lifecycle")
	if err != nil {
		// Candidate errors contain digests, storage nodes, and transport/SQL
		// details. Keep those out of component logs while preserving bounded
		// aggregate progress and a stable health category for Coordinator.
		logger.Info("pass-failed", data)
		return errAgentSnapshotLifecyclePass
	}
	logger.Info("pass-complete", data)
	return nil
}

func (cmd *RunCommand) agentSnapshotAPIHandlers() (*snapshotsapi.HandlerFactory, error) {
	cmd.agentSnapshotMu.Lock()
	defer cmd.agentSnapshotMu.Unlock()
	if !cmd.AgentSnapshots.Enabled {
		return snapshotsapi.NewHandlerFactory(snapshotsapi.Config{Enabled: false})
	}
	if cmd.agentSnapshotHandlerFactory == nil {
		return nil, fmt.Errorf("snapshot API is not composed")
	}
	return cmd.agentSnapshotHandlerFactory, nil
}

func (cmd *RunCommand) agentSnapshotCoreStepFactoryOptions() ([]engine.CoreStepFactoryOption, bool) {
	if !cmd.AgentSnapshots.Enabled {
		return nil, false
	}
	cmd.agentSnapshotMu.Lock()
	defer cmd.agentSnapshotMu.Unlock()
	if cmd.agentSnapshotOutputSealer == nil || cmd.agentSnapshotMetadataStore == nil ||
		cmd.agentSnapshotContentStore == nil || cmd.agentSnapshotWorkflowRuns == nil {
		return nil, false
	}
	options := []engine.CoreStepFactoryOption{
		engine.WithOutputSealer(cmd.agentSnapshotOutputSealer),
		engine.WithSnapshotCanonicalizer(snapshot.Canonicalizer{
			MaxContentBytes: cmd.agentSnapshotArchiveLimits.MaxContentBytes,
			MaxEntries:      cmd.agentSnapshotArchiveLimits.MaxEntries,
			TempDir:         cmd.AgentSnapshots.TempDir,
		}),
		engine.WithSnapshotLoader(
			cmd.agentSnapshotMetadataStore,
			cmd.agentSnapshotContentStore,
			cmd.agentSnapshotWorkflowRuns,
		),
	}
	if !isNilDependency(cmd.agentSnapshotPublisher) {
		options = append(options, engine.WithSnapshotPublisher(cmd.agentSnapshotPublisher))
	}
	return options, true
}

// artifactLocator returns the process-wide DaemonSet ArtifactLocator, creating
// it on first use.
//
// It exists so the single-instance invariant cannot be broken by a later
// assignment. It was: constructPool captured the locator the workers write to,
// and a subsequent `cmd.k8sArtifactLocator = NewArtifactLocator()` handed the
// Reaper a fresh empty one, leaving the two components on disjoint maps. The
// Reaper then found nothing to clean up, and nothing ever reclaimed an entry.
func (cmd *RunCommand) artifactLocator() *jetbridge.ArtifactLocator {
	if cmd.k8sArtifactLocator == nil {
		cmd.k8sArtifactLocator = jetbridge.NewArtifactLocator()
	}
	return cmd.k8sArtifactLocator
}

func (cmd *RunCommand) constructPool(dbConn db.DbConn, lockFactory lock.LockFactory, workerCache *db.WorkerCache) (worker.Pool, error) {
	dbResourceCacheFactory := db.NewResourceCacheFactory(dbConn, lockFactory)
	dbWorkerBaseResourceTypeFactory := db.NewWorkerBaseResourceTypeFactory(dbConn)
	dbTaskCacheFactory := db.NewTaskCacheFactory(dbConn)
	dbWorkerTaskCacheFactory := db.NewWorkerTaskCacheFactory(dbConn)
	dbVolumeRepository := db.NewVolumeRepository(dbConn)
	dbWorkerFactory := db.NewWorkerFactory(dbConn, workerCache)
	dbTeamFactory := db.NewTeamFactory(dbConn, lockFactory)

	workerDB := worker.NewDB(
		dbWorkerFactory,
		dbTeamFactory,
		dbVolumeRepository,
		dbTaskCacheFactory,
		dbWorkerTaskCacheFactory,
		dbResourceCacheFactory,
		dbWorkerBaseResourceTypeFactory,
		lockFactory,
	)

	factory := worker.DefaultFactory{
		DB:       workerDB,
		Streamer: cmd.streamer(),
	}

	if cmd.Kubernetes.Namespace != "" {
		resolveCapabilityKey, err := cmd.loadArtifactResolveCapabilityKey()
		if err != nil {
			return worker.Pool{}, fmt.Errorf("load artifact resolve capability key: %w", err)
		}
		k8sCfg := jetbridge.NewConfig(cmd.Kubernetes.Namespace, cmd.Kubernetes.Kubeconfig)
		k8sCfg.PodStartupTimeout = cmd.Kubernetes.PodStartupTimeout
		k8sCfg.PodSchedulingTimeout = cmd.Kubernetes.PodSchedulingTimeout
		k8sCfg.ImagePullSecrets = cmd.Kubernetes.ImagePullSecrets
		k8sCfg.ServiceAccount = cmd.Kubernetes.ServiceAccount
		k8sCfg.CacheStore = cmd.Kubernetes.CacheStore
		k8sCfg.CacheHostPath = cmd.Kubernetes.CacheHostPath
		k8sCfg.ArtifactHelperImage = cmd.Kubernetes.ArtifactHelperImage
		k8sCfg.ArtifactDaemonPort = cmd.Kubernetes.ArtifactDaemonPort
		k8sCfg.ArtifactDaemonHostPath = cmd.Kubernetes.ArtifactDaemonHostPath
		k8sCfg.ArtifactDaemonService = cmd.Kubernetes.ArtifactDaemonService
		k8sCfg.ArtifactDaemonTLSCert = cmd.Kubernetes.ArtifactDaemonTLSCert
		k8sCfg.ArtifactDaemonTLSKey = cmd.Kubernetes.ArtifactDaemonTLSKey
		k8sCfg.ArtifactDaemonTLSCACert = cmd.Kubernetes.ArtifactDaemonTLSCACert
		k8sCfg.ArtifactDaemonTLSEnabled = cmd.Kubernetes.ArtifactDaemonTLSCert != ""
		k8sCfg.ArtifactDaemonResolveCapabilityKey = resolveCapabilityKey
		k8sCfg.ArtifactDaemonResolveCapabilityTTL = cmd.Kubernetes.ArtifactDaemonResolveCapabilityTTL
		if cmd.Kubernetes.ImageRegistryPrefix != "" || cmd.Kubernetes.ImageRegistrySecret != "" {
			k8sCfg.ImageRegistry = &jetbridge.ImageRegistryConfig{
				Prefix:     cmd.Kubernetes.ImageRegistryPrefix,
				SecretName: cmd.Kubernetes.ImageRegistrySecret,
			}
		}
		if len(cmd.Kubernetes.BaseResourceTypes) > 0 {
			k8sCfg.ResourceTypeImages = jetbridge.MergeResourceTypeImages(cmd.Kubernetes.BaseResourceTypes)
		}
		k8sClientset, err := jetbridge.NewClientset(k8sCfg)
		if err != nil {
			return worker.Pool{}, fmt.Errorf("creating k8s clientset: %w", err)
		}
		k8sRestConfig, err := jetbridge.RestConfig(k8sCfg)
		if err != nil {
			return worker.Pool{}, fmt.Errorf("creating k8s rest config: %w", err)
		}
		factory.K8sClientset = k8sClientset
		factory.K8sConfig = &k8sCfg
		factory.K8sExecutor = jetbridge.NewSPDYExecutor(k8sClientset, k8sRestConfig)
		factory.K8sArtifactLocator = cmd.artifactLocator()

		if k8sCfg.ArtifactDaemonService != "" {
			daemonPort := k8sCfg.ArtifactDaemonPort
			if daemonPort == 0 {
				daemonPort = 7780
			}
			dcLogger := lager.NewLogger("daemon-client")
			dcLogger.RegisterSink(lager.NewWriterSink(os.Stderr, lager.INFO))

			var daemonTLSCfg *jetbridge.DaemonClientTLSConfig
			if k8sCfg.ArtifactDaemonTLSCert != "" || k8sCfg.ArtifactDaemonTLSKey != "" || k8sCfg.ArtifactDaemonTLSCACert != "" {
				daemonTLSCfg = &jetbridge.DaemonClientTLSConfig{
					CertPath:   k8sCfg.ArtifactDaemonTLSCert,
					KeyPath:    k8sCfg.ArtifactDaemonTLSKey,
					CACertPath: k8sCfg.ArtifactDaemonTLSCACert,
				}
			}

			if cmd.AgentSnapshots.Enabled {
				cmd.agentSnapshotMu.Lock()
				sharedDaemonClient := cmd.agentSnapshotDaemonClient
				cmd.agentSnapshotMu.Unlock()
				if sharedDaemonClient == nil {
					return worker.Pool{}, fmt.Errorf("snapshot command components must be composed before constructing worker pools")
				}
				factory.K8sDaemonClient = sharedDaemonClient
			} else {
				factory.K8sDaemonClient = jetbridge.NewDaemonClient(
					dcLogger,
					k8sClientset,
					k8sCfg.Namespace,
					k8sCfg.ArtifactDaemonService,
					daemonPort,
					daemonTLSCfg,
				)
			}
		}
	}

	return worker.NewPool(
		factory,
		workerDB,
	), nil
}

func (cmd *RunCommand) gcComponents(
	logger lager.Logger,
	gcConn db.DbConn,
	lockFactory lock.LockFactory,
) ([]RunnableComponent, error) {
	dbWorkerLifecycle := db.NewWorkerLifecycle(gcConn)
	dbResourceCacheLifecycle := db.NewResourceCacheLifecycle(gcConn)
	dbTaskCacheLifecycle := db.NewTaskCacheLifecycle(gcConn)
	dbContainerRepository := db.NewContainerRepository(gcConn)
	dbArtifactLifecycle := db.NewArtifactLifecycle(gcConn)
	dbAccessTokenLifecycle := db.NewAccessTokenLifecycle(gcConn)
	resourceConfigCheckSessionLifecycle := db.NewResourceConfigCheckSessionLifecycle(gcConn)
	dbBuildFactory := db.NewBuildFactory(gcConn, lockFactory, cmd.GC.OneOffBuildGracePeriod, cmd.GC.FailedGracePeriod)
	dbResourceConfigFactory := db.NewResourceConfigFactory(gcConn, lockFactory)
	dbPipelineLifecycle := db.NewPipelineLifecycle(gcConn, lockFactory)
	dbCheckLifecycle := db.NewCheckLifecycle(gcConn)

	dbVolumeRepository := db.NewVolumeRepository(gcConn)

	// set the 'unreferenced resource config' grace period to be the longer than
	// the check timeout, just to make sure it doesn't get removed out from under
	// a running check
	//
	// 5 minutes is arbitrary - this really shouldn't matter a whole lot, but
	// exposing a config specifically for it is a little risky, since you don't
	// want to set it too low.
	unreferencedConfigGracePeriod := cmd.GlobalResourceCheckTimeout + 5*time.Minute

	collectors := map[string]component.Runnable{
		atc.ComponentCollectorBuilds:            gc.NewBuildCollector(dbBuildFactory),
		atc.ComponentCollectorWorkers:           gc.NewWorkerCollector(dbWorkerLifecycle),
		atc.ComponentCollectorResourceConfigs:   gc.NewResourceConfigCollector(dbResourceConfigFactory, unreferencedConfigGracePeriod),
		atc.ComponentCollectorResourceCaches:    gc.NewResourceCacheCollector(dbResourceCacheLifecycle),
		atc.ComponentCollectorTaskCaches:        gc.NewTaskCacheCollector(dbTaskCacheLifecycle),
		atc.ComponentCollectorResourceCacheUses: gc.NewResourceCacheUseCollector(dbResourceCacheLifecycle),
		atc.ComponentCollectorArtifacts:         gc.NewArtifactCollector(dbArtifactLifecycle),
		atc.ComponentCollectorVolumes:           gc.NewVolumeCollector(dbVolumeRepository, cmd.GC.MissingGracePeriod),
		atc.ComponentCollectorContainers:        gc.NewContainerCollector(dbContainerRepository, cmd.GC.MissingGracePeriod, cmd.GC.HijackGracePeriod),
		atc.ComponentCollectorCheckSessions:     gc.NewResourceConfigCheckSessionCollector(resourceConfigCheckSessionLifecycle),
		atc.ComponentCollectorPipelines:         gc.NewPipelineCollector(dbPipelineLifecycle),
		atc.ComponentCollectorAccessTokens:      gc.NewAccessTokensCollector(dbAccessTokenLifecycle, jwt.DefaultLeeway),
		atc.ComponentCollectorChecks:            gc.NewChecksCollector(dbCheckLifecycle),
		atc.ComponentCollectorDeprecatedScopes:  gc.NewDeprecatedScopeCollector(gcConn, cmd.GC.DeprecatedScopeGracePeriod),
	}

	var components []RunnableComponent
	for collectorName, collector := range collectors {
		components = append(components, RunnableComponent{
			Component: atc.Component{
				Name: collectorName,
			},
			Runnable: collector,
		})
	}

	// Reclaiming one-shot workflow-run templates is never urgent and the pass
	// takes the pipelines row lock that workflow admission needs, so it runs on
	// its own slow cadence instead of the 10s collector default.
	components = append(components, RunnableComponent{
		Component: atc.Component{
			Name: atc.ComponentCollectorWorkflowRunTemplates,
		},
		Runnable: gc.NewWorkflowRunTemplateCollector(
			db.NewWorkflowRunTemplateLifecycle(gcConn),
			cmd.GC.WorkflowRunTemplateGracePeriod,
			cmd.GC.WorkflowRunTemplateRetirementPeriod,
		),
		Interval: 5 * time.Minute,
	})

	return components, nil
}

func (cmd *RunCommand) validateCustomRoles() error {
	path := cmd.ConfigRBAC.Path()
	if path == "" {
		return nil
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to open RBAC config file (%s): %w", cmd.ConfigRBAC, err)
	}

	var data map[string][]string
	if err = yaml.Unmarshal(content, &data); err != nil {
		return fmt.Errorf("failed to parse RBAC config file (%s): %w", cmd.ConfigRBAC, err)
	}

	allKnownRoles := map[string]bool{}
	for _, roleName := range accessor.DefaultRoles {
		allKnownRoles[roleName] = true
	}

	for role, actions := range data {
		if _, ok := allKnownRoles[role]; !ok {
			return fmt.Errorf("failed to customize roles: %w", fmt.Errorf("unknown role %s", role))
		}

		for _, action := range actions {
			if _, ok := accessor.DefaultRoles[action]; !ok {
				return fmt.Errorf("failed to customize roles: %w", fmt.Errorf("unknown action %s", action))
			}
		}
	}

	return nil
}

func (cmd *RunCommand) parseCustomRoles() (map[string]string, error) {
	mapping := map[string]string{}

	path := cmd.ConfigRBAC.Path()
	if path == "" {
		return mapping, nil
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var data map[string][]string
	if err = yaml.Unmarshal(content, &data); err != nil {
		return nil, err
	}

	for role, actions := range data {
		for _, action := range actions {
			mapping[action] = role
		}
	}

	return mapping, nil
}

func (cmd *RunCommand) secretManager(logger lager.Logger) (creds.Secrets, error) {
	var secretsFactory creds.SecretsFactory = noop.NewNoopFactory()
	for name, manager := range cmd.CredentialManagers {
		if !manager.IsConfigured() {
			continue
		}

		credsLogger := logger.Session("credential-manager", lager.Data{
			"name": name,
		})

		credsLogger.Info("configured credentials manager")

		err := manager.Init(credsLogger)
		if err != nil {
			return nil, err
		}

		err = manager.Validate()
		if err != nil {
			return nil, fmt.Errorf("credential manager '%s' misconfigured: %s", name, err)
		}

		secretsFactory, err = manager.NewSecretsFactory(credsLogger)
		if err != nil {
			return nil, err
		}

		break
	}

	return cmd.CredentialManagement.NewSecrets(secretsFactory), nil
}

func (cmd *RunCommand) newKey() *encryption.Key {
	var newKey *encryption.Key
	if cmd.EncryptionKey.AEAD != nil {
		newKey = encryption.NewKey(cmd.EncryptionKey.AEAD)
	}
	return newKey
}

func (cmd *RunCommand) oldKey() *encryption.Key {
	var oldKey *encryption.Key
	if cmd.OldEncryptionKey.AEAD != nil {
		oldKey = encryption.NewKey(cmd.OldEncryptionKey.AEAD)
	}
	return oldKey
}

func (cmd *RunCommand) constructWebHandler(logger lager.Logger) (http.Handler, error) {
	webHandler, err := web.NewHandler(logger, cmd.WebPublicDir.Path())
	if err != nil {
		return nil, err
	}
	return metric.WrapHandler(logger, metric.Metrics, "web", webHandler), nil
}

func (cmd *RunCommand) skyHttpClient() (*http.Client, error) {
	httpClient := http.DefaultClient

	if cmd.isTLSEnabled() {
		certpool, err := x509.SystemCertPool()
		if err != nil {
			return nil, err
		}

		if !cmd.LetsEncrypt.Enable {
			cert, err := tls.LoadX509KeyPair(string(cmd.TLSCert), string(cmd.TLSKey))
			if err != nil {
				return nil, err
			}

			x509Cert, err := x509.ParseCertificate(cert.Certificate[0])
			if err != nil {
				return nil, err
			}

			certpool.AddCert(x509Cert)
		}

		httpClient.Transport = &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			TLSClientConfig: &tls.Config{
				RootCAs: certpool,
			},
		}
	} else {
		httpClient.Transport = http.DefaultTransport
	}

	httpClient.Transport = mitmRoundTripper{
		RoundTripper: httpClient.Transport,

		SourceHost: cmd.ExternalURL.URL.Host,
		TargetURL:  cmd.DefaultURL().URL,
	}

	return httpClient, nil
}

type mitmRoundTripper struct {
	http.RoundTripper

	SourceHost string
	TargetURL  *url.URL
}

func (tripper mitmRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host == tripper.SourceHost {
		req.URL.Scheme = tripper.TargetURL.Scheme
		req.URL.Host = tripper.TargetURL.Host
	}

	return tripper.RoundTripper.RoundTrip(req)
}

func (cmd *RunCommand) tlsConfig(logger lager.Logger, dbConn db.DbConn) (*tls.Config, error) {
	tlsConfig := atc.DefaultTLSConfig()

	if cmd.isTLSEnabled() {
		tlsLogger := logger.Session("tls-enabled")

		if cmd.isMTLSEnabled() {
			tlsLogger.Debug("mTLS-Enabled")
			clientCACert, err := os.ReadFile(string(cmd.TLSCaCert))
			if err != nil {
				return nil, err
			}
			clientCertPool := x509.NewCertPool()
			clientCertPool.AppendCertsFromPEM(clientCACert)

			tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert
			tlsConfig.ClientCAs = clientCertPool
		}

		if cmd.LetsEncrypt.Enable {
			tlsLogger.Debug("using-autocert-manager")

			cache, err := newDbCache(dbConn)
			if err != nil {
				return nil, err
			}
			m := autocert.Manager{
				Prompt:     autocert.AcceptTOS,
				Cache:      cache,
				HostPolicy: autocert.HostWhitelist(cmd.ExternalURL.URL.Hostname()),
				Client:     &acme.Client{DirectoryURL: cmd.LetsEncrypt.ACMEURL.String()},
			}
			tlsConfig.NextProtos = append(tlsConfig.NextProtos, acme.ALPNProto)
			tlsConfig.GetCertificate = m.GetCertificate
		} else {
			tlsLogger.Debug("loading-tls-certs")
			cert, err := tls.LoadX509KeyPair(string(cmd.TLSCert), string(cmd.TLSKey))
			if err != nil {
				return nil, err
			}
			tlsConfig.Certificates = []tls.Certificate{cert}
		}
	}
	return tlsConfig, nil
}

func (cmd *RunCommand) parseDefaultLimits() (atc.ContainerLimits, error) {
	limits := atc.ContainerLimits{}
	if cmd.DefaultCpuLimit != nil {
		cpu := atc.CPULimit(*cmd.DefaultCpuLimit)
		limits.CPU = &cpu
	}
	if cmd.DefaultMemoryLimit != nil {
		memory, err := atc.ParseMemoryLimit(*cmd.DefaultMemoryLimit)
		if err != nil {
			return atc.ContainerLimits{}, err
		}
		limits.Memory = &memory
	}
	return limits, nil
}

func (cmd *RunCommand) parseDefaultRequests() (atc.ContainerLimits, error) {
	requests := atc.ContainerLimits{}
	if cmd.DefaultCpuRequest != nil {
		cpu := atc.CPULimit(*cmd.DefaultCpuRequest)
		requests.CPU = &cpu
	}
	if cmd.DefaultMemoryRequest != nil {
		memory, err := atc.ParseMemoryLimit(*cmd.DefaultMemoryRequest)
		if err != nil {
			return atc.ContainerLimits{}, err
		}
		requests.Memory = &memory
	}
	return requests, nil
}

func (cmd *RunCommand) defaultBindIP() net.IP {
	URL := cmd.BindIP.String()
	if URL == "0.0.0.0" {
		URL = "127.0.0.1"
	}

	return net.ParseIP(URL)
}

func (cmd *RunCommand) DefaultURL() flag.URL {
	return flag.URL{
		URL: &url.URL{
			Scheme: "http",
			Host:   fmt.Sprintf("%s:%d", cmd.defaultBindIP().String(), cmd.BindPort),
		},
	}
}

func run(runner ifrit.Runner, onReady func(), onExit func()) ifrit.Runner {
	return ifrit.RunFunc(func(signals <-chan os.Signal, ready chan<- struct{}) error {
		process := ifrit.Background(runner)

		subExited := process.Wait()
		subReady := process.Ready()

		for {
			select {
			case <-subReady:
				onReady()
				close(ready)
				subReady = nil
			case err := <-subExited:
				onExit()
				return err
			case sig := <-signals:
				process.Signal(sig)
			}
		}
	})
}

func (cmd *RunCommand) validate() error {
	var errs *multierror.Error

	switch {
	case cmd.TLSBindPort == 0:
		if cmd.TLSCert != "" || cmd.TLSKey != "" || cmd.LetsEncrypt.Enable {
			errs = multierror.Append(
				errs,
				errors.New("must specify --tls-bind-port to use TLS"),
			)
		}
	case cmd.LetsEncrypt.Enable:
		if cmd.TLSCert != "" || cmd.TLSKey != "" {
			errs = multierror.Append(
				errs,
				errors.New("cannot specify --enable-lets-encrypt if --tls-cert or --tls-key are set"),
			)
		}
	case cmd.TLSCert != "" && cmd.TLSKey != "":
		if cmd.ExternalURL.URL.Scheme != "https" {
			errs = multierror.Append(
				errs,
				errors.New("must specify HTTPS external-url to use TLS"),
			)
		}
	default:
		errs = multierror.Append(
			errs,
			errors.New("must specify --tls-cert and --tls-key, or --enable-lets-encrypt to use TLS"),
		)
	}

	if err := cmd.validateCustomRoles(); err != nil {
		errs = multierror.Append(errs, err)
	}

	if err := cmd.validateK8sRuntime(); err != nil {
		errs = multierror.Append(errs, err)
	}

	if err := cmd.validateAgentSnapshots(); err != nil {
		errs = multierror.Append(errs, err)
	}

	if err := cmd.validateAgentWorkflowRuns(); err != nil {
		errs = multierror.Append(errs, err)
	}
	if err := cmd.validateAgentChildExecutions(); err != nil {
		errs = multierror.Append(errs, err)
	}

	if err := cmd.validateAgentExperiments(); err != nil {
		errs = multierror.Append(errs, err)
	}

	if err := cmd.validateAgentPublisher(); err != nil {
		errs = multierror.Append(errs, err)
	}

	if err := cmd.validateGarbageCollection(); err != nil {
		errs = multierror.Append(errs, err)
	}

	return errs.ErrorOrNil()
}

func (cmd *RunCommand) validateAgentWorkflowRuns() error {
	var errs *multierror.Error
	if cmd.AgentWorkflowRuns.ReconcilerInterval <= 0 {
		errs = multierror.Append(errs, errors.New("--agent-workflow-run-reconciler-interval must be positive"))
	}
	if cmd.AgentWorkflowRuns.AdmissionTimeout <= 0 {
		errs = multierror.Append(errs, errors.New("--agent-workflow-run-admission-timeout must be positive"))
	}
	if cmd.AgentWorkflowRuns.ReconcilerInterval > 0 &&
		cmd.AgentWorkflowRuns.AdmissionTimeout > 0 &&
		cmd.AgentWorkflowRuns.ReconcilerInterval > cmd.AgentWorkflowRuns.AdmissionTimeout/2 {
		errs = multierror.Append(errs, errors.New("--agent-workflow-run-admission-timeout must be at least twice --agent-workflow-run-reconciler-interval"))
	}
	return errs.ErrorOrNil()
}

func (cmd *RunCommand) validateAgentChildExecutions() error {
	if !cmd.AgentChildExecutions.Enabled {
		return nil
	}
	var errs *multierror.Error
	if !cmd.AgentSnapshots.Enabled {
		errs = multierror.Append(errs, errors.New("agent child executions require --agent-snapshot-enabled"))
	}
	if cmd.AgentChildExecutions.CapabilityKey.Path() == "" {
		errs = multierror.Append(errs, errors.New("--agent-child-executions-capability-key is required when enabled"))
	}
	if cmd.AgentChildExecutions.BrokerRuntime.Path() == "" {
		errs = multierror.Append(errs, errors.New("--agent-child-executions-broker-runtime is required when enabled"))
	} else if _, err := loadAgentBrokerRuntime(cmd.AgentChildExecutions.BrokerRuntime.Path()); err != nil {
		errs = multierror.Append(errs, err)
	}
	if strings.TrimSpace(cmd.AgentChildExecutions.CapabilityKeyID) == "" {
		errs = multierror.Append(errs, errors.New("--agent-child-executions-capability-key-id is required when enabled"))
	}
	if cmd.AgentChildExecutions.CapabilityTTL <= 0 || cmd.AgentChildExecutions.CapabilityTTL > agentchildexecutions.MaxExecutionCapabilityTTL {
		errs = multierror.Append(errs, errors.New("--agent-child-executions-capability-ttl must be positive and at most one hour"))
	}
	if cmd.AgentChildExecutions.LeaseDuration <= 0 || cmd.AgentChildExecutions.ReconcilerInterval <= 0 || cmd.AgentChildExecutions.ReconcilerBatchSize <= 0 {
		errs = multierror.Append(errs, errors.New("agent child execution lease, reconciler interval, and batch size must be positive"))
	}
	return errs.ErrorOrNil()
}

func (cmd *RunCommand) validateGarbageCollection() error {
	var errs *multierror.Error
	if cmd.GC.WorkflowRunTemplateGracePeriod <= 0 {
		errs = multierror.Append(errs, errors.New("--gc-workflow-run-template-grace-period must be positive"))
	}
	// An admission holds a template reference for as long as it may still run,
	// so reclaiming templates younger than that could delete one out from under
	// an admission that is about to execute it.
	if cmd.GC.WorkflowRunTemplateGracePeriod > 0 &&
		cmd.AgentWorkflowRuns.AdmissionTimeout > 0 &&
		cmd.GC.WorkflowRunTemplateGracePeriod <= cmd.AgentWorkflowRuns.AdmissionTimeout {
		errs = multierror.Append(errs, errors.New("--gc-workflow-run-template-grace-period must be greater than --agent-workflow-run-admission-timeout"))
	}
	// Zero disables the retirement pass entirely; only a negative value is a
	// misconfiguration.
	if cmd.GC.WorkflowRunTemplateRetirementPeriod < 0 {
		errs = multierror.Append(errs, errors.New("--gc-workflow-run-template-retirement-period must not be negative"))
	}
	return errs.ErrorOrNil()
}

func (cmd *RunCommand) validateAgentExperiments() error {
	var errs *multierror.Error
	if cmd.AgentExperiments.Interval <= 0 {
		errs = multierror.Append(errs, errors.New("--agent-experiment-runner-interval must be positive"))
	}
	if cmd.AgentExperiments.MaxConcurrency <= 0 {
		errs = multierror.Append(errs, errors.New("--agent-experiment-runner-max-concurrency must be positive"))
	}
	if cmd.AgentExperiments.Enabled && !cmd.AgentSnapshots.Enabled {
		errs = multierror.Append(errs, errors.New("--agent-snapshot-enabled is required when --agent-experiment-runner-enabled is set"))
	}
	return errs.ErrorOrNil()
}

func (cmd *RunCommand) validateAgentSnapshots() error {
	var errs *multierror.Error
	if cmd.AgentSnapshots.ReplicationFactor <= 0 {
		errs = multierror.Append(errs, errors.New("--agent-snapshot-replication-factor must be positive"))
	}
	if cmd.AgentSnapshots.MaxBytes <= 0 {
		errs = multierror.Append(errs, errors.New("--agent-snapshot-max-bytes must be positive"))
	}
	if cmd.AgentSnapshots.MaxFiles <= 0 {
		errs = multierror.Append(errs, errors.New("--agent-snapshot-max-files must be positive"))
	}
	if cmd.AgentSnapshots.BindingRetention <= 0 {
		errs = multierror.Append(errs, errors.New("--agent-snapshot-binding-retention must be positive"))
	}
	if cmd.AgentSnapshots.OrphanGracePeriod <= 0 {
		errs = multierror.Append(errs, errors.New("--agent-snapshot-orphan-grace-period must be positive"))
	}
	if cmd.AgentSnapshots.GCInterval <= 0 {
		errs = multierror.Append(errs, errors.New("--agent-snapshot-gc-interval must be positive"))
	}
	if cmd.AgentSnapshots.RepairInterval <= 0 {
		errs = multierror.Append(errs, errors.New("--agent-snapshot-repair-interval must be positive"))
	}
	if cmd.AgentSnapshots.MaxBytes > snapshot.DefaultMaxSnapshotContentBytes {
		errs = multierror.Append(errs, fmt.Errorf("--agent-snapshot-max-bytes must not exceed %d", snapshot.DefaultMaxSnapshotContentBytes))
	}
	if cmd.AgentSnapshots.MaxFiles > snapshot.DefaultMaxSnapshotEntries {
		errs = multierror.Append(errs, fmt.Errorf("--agent-snapshot-max-files must not exceed %d", snapshot.DefaultMaxSnapshotEntries))
	}
	if !cmd.AgentSnapshots.Enabled {
		return errs.ErrorOrNil()
	}
	if cmd.AgentSnapshots.TempDir == "" {
		errs = multierror.Append(errs, errors.New("--agent-snapshot-temp-dir is required when --agent-snapshot-enabled is set"))
	} else if !filepath.IsAbs(cmd.AgentSnapshots.TempDir) {
		errs = multierror.Append(errs, errors.New("--agent-snapshot-temp-dir must be an absolute path"))
	} else if err := snapshot.ValidateTempDir(cmd.AgentSnapshots.TempDir); err != nil {
		errs = multierror.Append(errs, fmt.Errorf("--agent-snapshot-temp-dir: %w", err))
	}
	if cmd.Kubernetes.Namespace == "" {
		errs = multierror.Append(errs, errors.New("--kubernetes-namespace is required when --agent-snapshot-enabled is set"))
	}
	if cmd.Kubernetes.ArtifactDaemonHostPath == "" {
		errs = multierror.Append(errs, errors.New("--kubernetes-artifact-daemon-host-path is required when --agent-snapshot-enabled is set"))
	}
	if cmd.Kubernetes.ArtifactDaemonService == "" {
		errs = multierror.Append(errs, errors.New("--kubernetes-artifact-daemon-service is required when --agent-snapshot-enabled is set"))
	}
	if cmd.Kubernetes.ArtifactDaemonPort <= 0 || cmd.Kubernetes.ArtifactDaemonPort > 65535 {
		errs = multierror.Append(errs, errors.New("--kubernetes-artifact-daemon-port must be between 1 and 65535 when --agent-snapshot-enabled is set"))
	}
	if cmd.Kubernetes.ArtifactDaemonTLSCert == "" ||
		cmd.Kubernetes.ArtifactDaemonTLSKey == "" ||
		cmd.Kubernetes.ArtifactDaemonTLSCACert == "" {
		errs = multierror.Append(errs, errors.New("artifact daemon mTLS certificate, key, and CA certificate are required when --agent-snapshot-enabled is set"))
	}
	return errs.ErrorOrNil()
}

// validateK8sRuntime enforces the DaemonSet artifact cache as a hard
// requirement for the Kubernetes runtime. Without it, every step-produced
// artifact is read via exec into the producing pod, and downstream consumers
// fail with `exec stream: pods "..." not found` once the reaper deletes the
// producer pod.
//
// When --kubernetes-namespace is set (i.e. the K8s runtime is enabled),
// --kubernetes-artifact-daemon-host-path MUST also be set. This replaces the
// prior silent fallback to exec-backed artifact I/O.
//
// See track
// route_artifact_reads_through_daemonset_remove_exec_backed_artifact_io_20260418.
func (cmd *RunCommand) validateK8sRuntime() error {
	if cmd.Kubernetes.Namespace == "" {
		return nil
	}
	if cmd.Kubernetes.ArtifactDaemonHostPath == "" {
		return errors.New("--kubernetes-artifact-daemon-host-path is required when --kubernetes-namespace is set: " +
			"the DaemonSet artifact cache is mandatory for the K8s runtime, because downstream artifact reads " +
			"must not exec into the producing pod (which is reaped as soon as the step finishes)")
	}
	if cmd.Kubernetes.ArtifactDaemonResolveCapabilityKey == "" {
		return errors.New("--kubernetes-artifact-daemon-resolve-capability-key is required when --kubernetes-namespace is set")
	}
	if err := atc.ValidatePinnedOCIImage(cmd.Kubernetes.ArtifactHelperImage); err != nil {
		return fmt.Errorf("--kubernetes-artifact-helper-image must be pinned to an exact sha256 digest: %w", err)
	}
	minimumCapabilityTTL, err := jetbridge.MinimumArtifactResolveCapabilityTTL(
		cmd.Kubernetes.PodSchedulingTimeout,
		cmd.Kubernetes.PodStartupTimeout,
	)
	if err != nil {
		return err
	}
	if cmd.Kubernetes.ArtifactDaemonResolveCapabilityTTL <= minimumCapabilityTTL {
		return fmt.Errorf("--kubernetes-artifact-daemon-resolve-capability-ttl must exceed the bounded scheduling, startup, retry, and skew window of %s", minimumCapabilityTTL)
	}
	return nil
}

func (cmd *RunCommand) loadArtifactResolveCapabilityKey() ([]byte, error) {
	cmd.artifactResolveCapabilityMu.Lock()
	defer cmd.artifactResolveCapabilityMu.Unlock()
	if len(cmd.artifactResolveCapabilityKey) != 0 {
		return append([]byte(nil), cmd.artifactResolveCapabilityKey...), nil
	}
	key, err := artifactcap.LoadKeyFile(cmd.Kubernetes.ArtifactDaemonResolveCapabilityKey)
	if err != nil {
		return nil, err
	}
	cmd.artifactResolveCapabilityKey = append([]byte(nil), key...)
	return append([]byte(nil), key...), nil
}

func (cmd *RunCommand) nonTLSBindAddr() string {
	return fmt.Sprintf("%s:%d", cmd.BindIP, cmd.BindPort)
}

func (cmd *RunCommand) tlsBindAddr() string {
	return fmt.Sprintf("%s:%d", cmd.BindIP, cmd.TLSBindPort)
}

func (cmd *RunCommand) debugBindAddr() string {
	return fmt.Sprintf("%s:%d", cmd.DebugBindIP, cmd.DebugBindPort)
}

func (cmd *RunCommand) configureMetrics(logger lager.Logger) error {
	host := cmd.Metrics.HostName
	if host == "" {
		host, _ = os.Hostname()
	}

	return metric.Metrics.Initialize(logger.Session("metrics"), host, cmd.Metrics.Attributes, cmd.Metrics.BufferSize)
}

func (cmd *RunCommand) constructDBConn(
	driverName string,
	logger lager.Logger,
	maxConns int,
	idleConns int,
	connectionName string,
	lockFactory lock.LockFactory,
) (db.DbConn, error) {
	dbConn, err := db.Open(logger.Session("db"), driverName, cmd.Postgres.ConnectionString(), cmd.newKey(), cmd.oldKey(), connectionName, lockFactory)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %s", err)
	}

	// Instrument with Metrics
	dbConn = metric.CountQueries(dbConn)
	metric.Metrics.Databases = append(metric.Metrics.Databases, dbConn)

	// Instrument with Logging
	if cmd.LogDBQueries {
		dbConn = db.Log(logger.Session("log-dbconn"), dbConn)
	}

	// Prepare
	dbConn.SetMaxOpenConns(maxConns)
	dbConn.SetMaxIdleConns(idleConns)

	return dbConn, nil
}

type Closer interface {
	Close() error
}

func constructLockConns(driverName, connectionString string) ([lock.FactoryCount]*sql.DB, error) {
	conns := [lock.FactoryCount]*sql.DB{}
	for i := range lock.FactoryCount {
		dbConn, err := sql.Open(driverName, connectionString)
		if err != nil {
			return conns, err
		}

		dbConn.SetMaxOpenConns(1)
		dbConn.SetMaxIdleConns(1)
		dbConn.SetConnMaxLifetime(0)

		conns[i] = dbConn
	}
	return conns, nil
}

func (cmd *RunCommand) configureAuthForDefaultTeam(teamFactory db.TeamFactory) error {
	team, found, err := teamFactory.FindTeam(atc.DefaultTeamName)
	if err != nil {
		return err
	}

	if !found {
		return errors.New("default team not found")
	}

	auth, err := cmd.Auth.MainTeamFlags.Format()
	if err != nil {
		return fmt.Errorf("default team auth not configured: %v", err)
	}

	err = team.UpdateProviderAuth(auth)
	if err != nil {
		return err
	}

	return nil
}

func (cmd *RunCommand) constructEngine(
	workerPool worker.Pool,
	workerFactory db.WorkerFactory,
	teamFactory db.TeamFactory,
	buildFactory db.BuildFactory,
	resourceCacheFactory db.ResourceCacheFactory,
	resourceConfigFactory db.ResourceConfigFactory,
	secretManager creds.Secrets,
	defaultLimits atc.ContainerLimits,
	defaultRequests atc.ContainerLimits,
	lockFactory lock.LockFactory,
	rateLimiter engine.RateLimiter,
	policyChecker policy.Checker,
	resolver imageresolver.Resolver,
	dbConn db.DbConn,
	pipelineRunFactory db.PipelineRunFactory,
) (engine.Engine, error) {
	// Spend ledger for agent: steps. Same construction as the costs API
	// handler (atc/api/handler.go): the DB-backed cost ledger plus the global
	// daily cap from --agent-daily-budget-usd. Per-run admission is the
	// binder's durable reservation, not this checker.
	agentBudgetChecker := budget.NewChecker(
		db.NewAgentCostLedgerFactory(dbConn),
		budget.Config{
			GlobalDailyCapUSD: cmd.AgentDailyBudgetUSD,
		},
	)
	coreStepFactoryOptions := []engine.CoreStepFactoryOption{
		engine.WithCoreImageResolver(resolver),
		engine.WithAgentStepImage(cmd.AgentStepImage),
		engine.WithAgentPlatformTokenSecret(cmd.AgentPlatformTokenSecret),
		engine.WithAgentMetricsStore(db.NewAgentRunMetricsFactory(dbConn)),
		engine.WithAgentTranscriptStore(db.NewAgentRunTranscriptFactory(dbConn)),
		engine.WithAgentBudgetChecker(agentBudgetChecker),
		engine.WithWorkflowWaitStore(db.NewAgentWorkflowWaitsFactory(
			dbConn, cmd.AgentSnapshots.BindingRetention,
		)),
	}
	if snapshotOptions, ok := cmd.agentSnapshotCoreStepFactoryOptions(); ok {
		coreStepFactoryOptions = append(coreStepFactoryOptions, snapshotOptions...)
	}
	if cmd.AgentChildExecutions.Enabled {
		signer, _, runtimeConfig, err := cmd.composeAgentChildAuthority()
		if err != nil {
			return engine.Engine{}, err
		}
		coreStepFactoryOptions = append(coreStepFactoryOptions, engine.WithAgentBrokerAuthorityFactory(agentBrokerAuthorityFactory{
			signer: signer, runtime: runtimeConfig,
			leaseDuration: cmd.AgentChildExecutions.LeaseDuration,
			capabilityTTL: cmd.AgentChildExecutions.CapabilityTTL,
			now:           time.Now,
		}))
	}

	return engine.NewEngine(
		engine.NewStepperFactory(
			engine.NewCoreStepFactory(
				workerPool,
				cmd.streamer(),
				lockFactory,
				teamFactory,
				buildFactory,
				resourceCacheFactory,
				resourceConfigFactory,
				defaultLimits,
				defaultRequests,
				cmd.GlobalResourceCheckTimeout,
				cmd.DefaultGetTimeout,
				cmd.DefaultPutTimeout,
				cmd.DefaultTaskTimeout,
				coreStepFactoryOptions...,
			),
			cmd.ExternalURL.String(),
			rateLimiter,
			policyChecker,
			workerFactory,
			lockFactory,
			resourceConfigFactory,
			resourceCacheFactory,
			resolver,
		),
		secretManager,
		cmd.varSourcePool,
	), nil
}

func (cmd *RunCommand) constructHTTPHandler(
	logger lager.Logger,
	webHandler http.Handler,
	apiHandler http.Handler,
	authHandler http.Handler,
	skyHandler http.Handler,
	legacyHandler http.Handler,
	middleware token.Middleware,
) http.Handler {

	csrfHandler := auth.CSRFValidationHandler(
		apiHandler,
		middleware,
	)

	webMux := http.NewServeMux()
	webMux.Handle("/api/v1/", csrfHandler)
	webMux.Handle("/sky/issuer/", authHandler)
	webMux.Handle("/sky/", skyHandler)
	webMux.Handle("/auth/", legacyHandler)
	webMux.Handle("/login", legacyHandler)
	webMux.Handle("/logout", legacyHandler)
	webMux.Handle("/.well-known/", apiHandler)
	webMux.Handle("/", webHandler)

	httpHandler := wrappa.LoggerHandler{
		Logger: logger,

		Handler: wrappa.SecurityHandler{
			XFrameOptions:           cmd.Server.XFrameOptions,
			ContentSecurityPolicy:   cmd.Server.ContentSecurityPolicy,
			StrictTransportSecurity: cmd.Server.StrictTransportSecurity,

			// proxy Authorization header to/from auth cookie,
			// to support auth from JS (EventSource) and custom JWT auth
			Handler: auth.WebAuthHandler{
				Handler:    webMux,
				Middleware: middleware,
			},
		},
	}

	return httpHandler
}

func (cmd *RunCommand) constructLegacyHandler(
	logger lager.Logger,
) (http.Handler, error) {
	return legacyserver.NewLegacyServer(&legacyserver.LegacyConfig{
		Logger: logger.Session("legacy"),
	})
}

func (cmd *RunCommand) constructAuthHandler(
	logger lager.Logger,
	storage storage.Storage,
	userFactory db.UserFactory,
	displayUserIdGenerator atc.DisplayUserIdGenerator,
) (http.Handler, error) {

	issuerPath, _ := url.Parse("/sky/issuer")
	redirectPath, _ := url.Parse("/sky/callback")

	issuerURL := cmd.ExternalURL.URL.ResolveReference(issuerPath)
	redirectURL := cmd.ExternalURL.URL.ResolveReference(redirectPath)

	// Add public fly client
	cmd.Auth.AuthFlags.Clients[flyClientID] = flyClientSecret

	dexServer, err := dexserver.NewDexServer(&dexserver.DexConfig{
		Logger:            logger.Session("dex"),
		PasswordConnector: cmd.Auth.AuthFlags.PasswordConnector,
		Users:             cmd.Auth.AuthFlags.LocalUsers,
		Clients:           cmd.Auth.AuthFlags.Clients,
		Expiration:        cmd.Auth.AuthFlags.Expiration,
		IssuerURL:         issuerURL.String(),
		RedirectURL:       redirectURL.String(),
		SigningKey:        cmd.Auth.AuthFlags.SigningKey.PrivateKey,
		Storage:           storage,
	})
	if err != nil {
		return nil, err
	}

	// Dex serves /sky/issuer/* endpoints directly — no token interception.
	// JWT validation happens in the JWKS verifier on API requests.
	return token.EnsureUser(
		logger.Session("dex-server"),
		dexServer,
		token.NewClaimsParser(),
		userFactory,
		displayUserIdGenerator,
	), nil
}

func (cmd *RunCommand) constructSkyHandler(
	logger lager.Logger,
	httpClient *http.Client,
	middleware token.Middleware,
) (http.Handler, error) {

	authPath, _ := url.Parse("/sky/issuer/auth")
	tokenPath, _ := url.Parse("/sky/issuer/token")
	redirectPath, _ := url.Parse("/sky/callback")

	authURL := cmd.ExternalURL.URL.ResolveReference(authPath)
	tokenURL := cmd.ExternalURL.URL.ResolveReference(tokenPath)
	redirectURL := cmd.ExternalURL.URL.ResolveReference(redirectPath)

	endpoint := oauth2.Endpoint{
		AuthURL:   authURL.String(),
		TokenURL:  tokenURL.String(),
		AuthStyle: oauth2.AuthStyleInHeader,
	}

	oauth2Config := &oauth2.Config{
		Endpoint:     endpoint,
		ClientID:     cmd.Server.ClientID,
		ClientSecret: cmd.Server.ClientSecret,
		RedirectURL:  redirectURL.String(),
		Scopes:       []string{"openid", "profile", "email", "federated:id", "groups", "offline_access"},
	}

	skyServer, err := skyserver.NewSkyServer(&skyserver.SkyConfig{
		Logger:          logger.Session("sky"),
		TokenMiddleware: middleware,
		OAuthConfig:     oauth2Config,
		HTTPClient:      httpClient,
		StateSigningKey: deriveStateSigningKey(
			oauth2Config.ClientID,
			oauth2Config.ClientSecret,
			cmd.Postgres.User,
			cmd.Postgres.Password),
	})
	if err != nil {
		return nil, err
	}

	return skyserver.NewSkyHandler(skyServer), nil
}

func deriveStateSigningKey(clientID, clientSecret, dbUser, dbPassword string) []byte {
	mac := hmac.New(sha256.New, []byte(clientSecret))
	mac.Write([]byte(clientID))
	mac.Write([]byte(dbUser))
	mac.Write([]byte(dbPassword))
	return mac.Sum(nil)
}

func (cmd *RunCommand) constructTokenVerifier() accessor.TokenVerifier {
	validClients := []string{flyClientID}
	for clientId := range cmd.Auth.AuthFlags.Clients {
		validClients = append(validClients, clientId)
	}

	issuerPath, _ := url.Parse("/sky/issuer/keys")
	jwksURL := cmd.ExternalURL.URL.ResolveReference(issuerPath)

	return accessor.NewJWKSVerifier(jwksURL.String(), validClients)
}

func (cmd *RunCommand) constructAPIHandler(
	logger lager.Logger,
	reconfigurableSink *lager.ReconfigurableSink,
	teamFactory db.TeamFactory,
	workerTeamFactory db.TeamFactory,
	dbPipelineFactory db.PipelineFactory,
	dbJobFactory db.JobFactory,
	dbResourceFactory db.ResourceFactory,
	dbWorkerFactory db.WorkerFactory,
	dbVolumeRepository db.VolumeRepository,
	dbContainerRepository db.ContainerRepository,
	gcContainerDestroyer gc.Destroyer,
	dbBuildFactory db.BuildFactory,
	dbCheckFactory db.CheckFactory,
	dbPipelineRunFactory db.PipelineRunFactory,
	resourceConfigFactory db.ResourceConfigFactory,
	dbUserFactory db.UserFactory,
	workerPool worker.Pool,
	secretManager creds.Secrets,
	credsManagers creds.Managers,
	accessFactory accessor.AccessFactory,
	dbWall db.Wall,
	policyChecker policy.Checker,
	dbSigningKeyFactory db.SigningKeyFactory,
	lockFactory lock.LockFactory,
	dbConn db.DbConn,
) (http.Handler, error) {
	snapshotHandlers, err := cmd.agentSnapshotAPIHandlers()
	if err != nil {
		return nil, err
	}

	checkPipelineAccessHandlerFactory := auth.NewCheckPipelineAccessHandlerFactory(teamFactory)
	checkBuildReadAccessHandlerFactory := auth.NewCheckBuildReadAccessHandlerFactory(dbBuildFactory)
	checkBuildWriteAccessHandlerFactory := auth.NewCheckBuildWriteAccessHandlerFactory(dbBuildFactory)
	checkWorkerTeamAccessHandlerFactory := auth.NewCheckWorkerTeamAccessHandlerFactory(dbWorkerFactory)

	rejectArchivedHandlerFactory := pipelineserver.NewRejectArchivedHandlerFactory(teamFactory)

	aud := auditor.NewAuditor(
		cmd.Auditor.EnableBuildAuditLog,
		cmd.Auditor.EnableContainerAuditLog,
		cmd.Auditor.EnableJobAuditLog,
		cmd.Auditor.EnablePipelineAuditLog,
		cmd.Auditor.EnableResourceAuditLog,
		cmd.Auditor.EnableSystemAuditLog,
		cmd.Auditor.EnableTeamAuditLog,
		cmd.Auditor.EnableWorkerAuditLog,
		cmd.Auditor.EnableVolumeAuditLog,
		logger,
	)

	customRoles, err := cmd.parseCustomRoles()
	if err != nil {
		return nil, err
	}

	apiWrapper := wrappa.MultiWrappa{
		wrappa.NewConcurrentRequestLimitsWrappa(
			logger,
			wrappa.NewConcurrentRequestPolicy(cmd.ConcurrentRequestLimits),
		),
		wrappa.NewAPIMetricsWrappa(logger),
		wrappa.NewPolicyCheckWrappa(logger, policychecker.NewApiPolicyChecker(policyChecker)),
		wrappa.NewAPIAuthWrappa(
			checkPipelineAccessHandlerFactory,
			checkBuildReadAccessHandlerFactory,
			checkBuildWriteAccessHandlerFactory,
			checkWorkerTeamAccessHandlerFactory,
		),
		wrappa.NewRejectArchivedWrappa(rejectArchivedHandlerFactory),
		wrappa.NewConcourseVersionWrappa(concourse.Version),
		wrappa.NewAccessorWrappa(
			logger,
			accessFactory,
			aud,
			customRoles,
		),
		wrappa.NewCompressionWrappa(logger),
	}

	// Compose the one ticket/workflow admission graph here — this runs before
	// backendComponents, and the default team it needs exists by now. The
	// dispatcher component reuses this exact graph.
	dispatchGraph, err := cmd.composeAgentDispatch(
		logger, dbConn, lockFactory, teamFactory, dbBuildFactory, dbCheckFactory, dbPipelineRunFactory,
	)
	if err != nil {
		return nil, err
	}
	targetRenderer := dispatchGraph.targetRenderer
	workflowStore := dispatchGraph.workflows
	nodeStore := dispatchGraph.nodes
	workflowRunStore := dispatchGraph.runs
	snapshotStore := dispatchGraph.snapshots
	var resourceCapturer snapshotsapi.ResourceCapturer
	if dispatchGraph.sourceRuntime.resourceCapturer != nil {
		resourceCapturer = dispatchGraph.sourceRuntime.resourceCapturer
	}
	workflowWaitStore := dispatchGraph.waits
	// The overview and the run page read occurrences through the same
	// derivation the freezer writes: frozen history for terminal runs, live
	// derivation for everything still executing. They share dispatchGraph's
	// workflow store for the same reason the freezer does — a second store is a
	// second chance for the version a run executed and the version its history
	// describes to disagree.
	nodeOccurrenceReader, err := occurrence.NewReader(
		db.NewAgentWorkflowRunNodeOccurrencesFactory(dbConn),
		db.NewAgentWorkflowRunEvidenceFactory(dbConn),
		workflowStore,
		nodeStore,
	)
	if err != nil {
		return nil, fmt.Errorf("construct workflow-run node occurrence reader: %w", err)
	}
	workflowRunHandlers, err := workflowrunsapi.NewHandler(workflowrunsapi.Config{
		Logger: logger.Session("workflow-runs-api"),
		Team:   workflowrunsapi.TrustedTeam{ID: dispatchGraph.teamID, Name: dispatchGraph.teamName},
		Identity: func(r *http.Request) (string, error) {
			return workflowRunCreatorIdentity(accessor.GetAccessor(r).UserInfo())
		},
		Binder: dispatchGraph.binder, Runs: workflowRunStore,
		Canceler: dispatchGraph.canceler, Manifests: snapshotStore,
		Definitions: workflowStore, Occurrences: nodeOccurrenceReader,
	})
	if err != nil {
		return nil, fmt.Errorf("construct workflow-run API: %w", err)
	}
	workflowOverviewHandlers, err := workflowoverviewapi.NewHandler(workflowoverviewapi.Config{
		Logger:      logger.Session("workflow-overview-api"),
		Team:        workflowoverviewapi.TrustedTeam{ID: dispatchGraph.teamID, Name: dispatchGraph.teamName},
		Definitions: workflowStore,
		Runs:        workflowRunStore,
		Occurrences: nodeOccurrenceReader,
	})
	if err != nil {
		return nil, fmt.Errorf("construct workflow-overview API: %w", err)
	}
	nodeRunHandlers, err := noderunsapi.NewHandler(noderunsapi.Config{
		Logger: logger.Session("node-runs-api"),
		Team:   workflowrunsapi.TrustedTeam{ID: dispatchGraph.teamID, Name: dispatchGraph.teamName},
		Identity: func(r *http.Request) (string, error) {
			return workflowRunCreatorIdentity(accessor.GetAccessor(r).UserInfo())
		},
		Binder: dispatchGraph.binder, Runs: workflowRunStore,
		Canceler: dispatchGraph.canceler, Manifests: snapshotStore,
	})
	if err != nil {
		return nil, fmt.Errorf("construct node-run API: %w", err)
	}
	nodeUpgradeHandlers, err := nodeupgradesapi.NewHandler(nodeupgradesapi.Config{
		TeamID: dispatchGraph.teamID, TeamName: dispatchGraph.teamName,
		Store: nodeStore, Upgrader: workflow.NewNodeUpgradeService(nodeStore, workflowStore),
		Identity: func(r *http.Request) (string, error) {
			return workflowRunCreatorIdentity(accessor.GetAccessor(r).UserInfo())
		},
	})
	if err != nil {
		return nil, fmt.Errorf("construct node-upgrade API: %w", err)
	}
	cmd.agentSnapshotMu.Lock()
	workflowWaitContent := cmd.agentSnapshotContentStore
	workflowWaitCreator := cmd.agentSnapshotCreator
	workflowWaitArchiveLimits := cmd.agentSnapshotArchiveLimits
	cmd.agentSnapshotMu.Unlock()
	workflowWaitHandlers, err := workflowwaitsapi.NewHandler(workflowwaitsapi.Config{
		Team: workflowwaitsapi.TrustedTeam{ID: dispatchGraph.teamID, Name: dispatchGraph.teamName},
		Identity: func(r *http.Request) (workflowwaitsapi.RequestIdentity, error) {
			identity, err := agentSnapshotIdentity(accessor.GetAccessor(r).Claims())
			return workflowwaitsapi.RequestIdentity{
				Actor:       identity.Actor,
				DisplayName: identity.DisplayName,
			}, err
		},
		Runs: workflowRunStore, Waits: workflowWaitStore, Manifests: snapshotStore,
		Content: workflowWaitContent, Creator: workflowWaitCreator,
		ArchiveLimits: workflowWaitArchiveLimits,
		TempDir:       cmd.AgentSnapshots.TempDir,
	})
	if err != nil {
		return nil, fmt.Errorf("construct workflow-wait API: %w", err)
	}
	workflowOutcomeStore := db.NewAgentWorkflowOutcomesFactory(dbConn)
	workflowOutcomeHandlers, err := workflowoutcomesapi.NewHandler(workflowoutcomesapi.HandlerConfig{
		TeamID:   dispatchGraph.teamID,
		TeamName: dispatchGraph.teamName,
		Identity: func(request *http.Request) (string, error) {
			return workflowRunCreatorIdentity(accessor.GetAccessor(request).UserInfo())
		},
		Store:      workflowOutcomeStore,
		Authorizer: workflowOutcomeStore,
	})
	if err != nil {
		return nil, fmt.Errorf("construct workflow-outcome API: %w", err)
	}
	var experimentSourcePreparer *workflowrun.ExperimentResourceSourcePreparer
	if dispatchGraph.sourceRuntime.admitter != nil {
		experimentSourcePreparer, err = workflowrun.NewExperimentResourceSourcePreparer(
			workflowrun.WorkflowDefinitionStoreResolver{Store: workflowStore},
			dispatchGraph.sourceRuntime.admitter,
		)
		if err != nil {
			return nil, fmt.Errorf("construct experiment workflow resource source preparer: %w", err)
		}
	}
	experimentStore := cmd.newAgentExperimentsFactory(
		dbConn, targetRenderer, experimentSourcePreparer,
	)
	experimentHandlers, err := experimentsapi.NewHandler(experimentsapi.Config{
		TeamID:   dispatchGraph.teamID,
		TeamName: dispatchGraph.teamName,
		Identity: func(r *http.Request) (string, error) {
			return workflowRunCreatorIdentity(accessor.GetAccessor(r).UserInfo())
		},
		Store:           experimentStore,
		RunnerAvailable: cmd.AgentExperiments.Enabled,
	})
	if err != nil {
		return nil, fmt.Errorf("construct experiment API: %w", err)
	}
	var agentChildHandlerOptions []api.AgentChildExecutionHandlers
	if cmd.AgentChildExecutions.Enabled {
		handlers, err := cmd.agentChildExecutionHandlers(dbConn)
		if err != nil {
			return nil, err
		}
		agentChildHandlerOptions = append(agentChildHandlerOptions, *handlers)
	}

	return api.NewHandler(
		logger,
		cmd.ExternalURL.String(),
		cmd.OIDCIssuerURL.String(),
		cmd.Server.ClusterName,
		apiWrapper,

		teamFactory,
		dbPipelineFactory,
		dbJobFactory,
		dbResourceFactory,
		dbWorkerFactory,
		workerTeamFactory,
		dbVolumeRepository,
		dbBuildFactory,
		dbCheckFactory,
		dbPipelineRunFactory,
		resourceConfigFactory,
		dbUserFactory,

		buildserver.NewEventHandler,

		workerPool,

		reconfigurableSink,

		cmd.isTLSEnabled(),

		cmd.CLIArtifactsDir.Path(),
		concourse.Version,
		concourse.WorkerVersion,
		concourse.JetBridgeVersion,
		concourse.ConcourseVersion,
		secretManager,
		cmd.varSourcePool,
		credsManagers,
		containerserver.NewInterceptTimeoutFactory(cmd.InterceptIdleTimeout),
		time.Minute,
		dbWall,
		clock.NewClock(),
		dbSigningKeyFactory,
		dbConn,
		db.NewAgentFeedbackFactory(dbConn),
		db.NewAgentReviewsFactory(dbConn),
		db.NewAgentRunMetricsFactory(dbConn),
		dispatchGraph.tickets,
		workflowRunStore,
		ticketjournalapi.TrustedTeam{ID: dispatchGraph.teamID},
		db.NewAgentUserCredentialsFactory(dbConn),
		db.NewAgentCostLedgerFactory(dbConn),
		cmd.AgentDailyBudgetUSD,
		db.NewAgentRunTranscriptFactory(dbConn),
		workflowStore,
		nodeStore,
		// The SAME Deps the dispatcher component runs on.
		dispatch.NewHTTPHandler(dispatchGraph.deps, func(r *http.Request) (string, error) {
			return workflowRunCreatorIdentity(accessor.GetAccessor(r).UserInfo())
		}),
		db.NewAgentSettingsFactory(dbConn),
		snapshotHandlers,
		resourceCapturer,
		workflowRunHandlers,
		workflowOverviewHandlers,
		nodeRunHandlers,
		nodeUpgradeHandlers,
		workflowWaitHandlers,
		workflowOutcomeHandlers,
		experimentHandlers,
		agentChildHandlerOptions...,
	)
}

func (cmd *RunCommand) agentChildExecutionHandlers(connection db.DbConn) (*api.AgentChildExecutionHandlers, error) {
	signer, verifier, _, err := cmd.composeAgentChildAuthority()
	if err != nil {
		return nil, err
	}
	cmd.agentSnapshotMu.Lock()
	creator := cmd.agentSnapshotCreator
	metadata := cmd.agentSnapshotMetadataStore
	cmd.agentSnapshotMu.Unlock()
	sealer, err := agentchildexecutions.NewOrdinaryResultSealer(creator, metadata)
	if err != nil {
		return nil, err
	}
	store := db.NewAgentChildExecutionsFactory(connection)
	authority, err := agentchildexecutions.NewHandler(agentchildexecutions.HandlerConfig{
		Signer: signer, Verifier: verifier, Store: store, Sealer: sealer,
		ExecutionCapabilityTTL: cmd.AgentChildExecutions.CapabilityTTL,
	})
	if err != nil {
		return nil, err
	}
	return &api.AgentChildExecutionHandlers{Authority: authority, Store: store}, nil
}

// composeAgentChildAuthority builds the signing pair and strict runtime once.
// Both the HTTP handler and AgentStep factory receive these exact command-
// scoped instances; only already-signed bootstrap bytes enter a pod.
func (cmd *RunCommand) composeAgentChildAuthority() (*agentchildexecutions.CapabilitySigner, *agentchildexecutions.CapabilityVerifier, agentBrokerRuntimeConfig, error) {
	cmd.agentChildAuthorityMu.Lock()
	defer cmd.agentChildAuthorityMu.Unlock()
	if cmd.agentChildSigner != nil && cmd.agentChildVerifier != nil && cmd.agentBrokerRuntime != nil {
		return cmd.agentChildSigner, cmd.agentChildVerifier, *cmd.agentBrokerRuntime, nil
	}
	key, err := os.ReadFile(cmd.AgentChildExecutions.CapabilityKey.Path())
	if err != nil {
		return nil, nil, agentBrokerRuntimeConfig{}, fmt.Errorf("read agent child execution capability key: %w", err)
	}
	signer, err := agentchildexecutions.NewCapabilitySigner(cmd.AgentChildExecutions.CapabilityKeyID, key)
	if err != nil {
		return nil, nil, agentBrokerRuntimeConfig{}, err
	}
	verifier, err := agentchildexecutions.NewCapabilityVerifier(cmd.AgentChildExecutions.CapabilityKeyID, key)
	if err != nil {
		return nil, nil, agentBrokerRuntimeConfig{}, err
	}
	runtimeConfig, err := loadAgentBrokerRuntime(cmd.AgentChildExecutions.BrokerRuntime.Path())
	if err != nil {
		return nil, nil, agentBrokerRuntimeConfig{}, err
	}
	cmd.agentChildSigner = signer
	cmd.agentChildVerifier = verifier
	cmd.agentBrokerRuntime = &runtimeConfig
	return signer, verifier, runtimeConfig, nil
}

func workflowRunCreatorIdentity(info atc.UserInfo) (string, error) {
	if strings.TrimSpace(info.DisplayUserId) == "" {
		return "", errors.New("authenticated workflow-run creator is unavailable")
	}
	return info.DisplayUserId, nil
}

type tlsRedirectHandler struct {
	matchHostname string
	externalHost  string
	baseHandler   http.Handler
}

func (h tlsRedirectHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.Host, h.matchHostname) && (r.Method == "GET" || r.Method == "HEAD") {
		u := url.URL{
			Scheme:   "https",
			Host:     h.externalHost,
			Path:     r.URL.Path,
			RawQuery: r.URL.RawQuery,
		}

		http.Redirect(w, r, u.String(), http.StatusMovedPermanently)
	} else {
		h.baseHandler.ServeHTTP(w, r)
	}
}

func (cmd *RunCommand) isTLSEnabled() bool {
	return cmd.TLSBindPort != 0
}

type drainRunner struct {
	logger  lager.Logger
	drainer component.Drainable
}

func (runner drainRunner) Run(signals <-chan os.Signal, ready chan<- struct{}) error {
	close(ready)
	<-signals
	runner.drainer.Drain(lagerctx.NewContext(context.Background(), runner.logger))
	return nil
}

type RunnableComponent struct {
	atc.Component
	component.Runnable
	Interval time.Duration
}

func (cmd *RunCommand) isMTLSEnabled() bool {
	return string(cmd.TLSCaCert) != ""
}
