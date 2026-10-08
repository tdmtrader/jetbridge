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
	"github.com/concourse/concourse/artifactcap"
	"net"
	"net/http"
	_ "net/http/pprof"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"code.cloudfoundry.org/clock"
	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/concourse/concourse"
	"github.com/google/uuid"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api"
	"github.com/concourse/concourse/atc/api/accessor"
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
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/gc"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/atc/imageresolver"
	"github.com/concourse/concourse/atc/lidar"
	"github.com/concourse/concourse/atc/mcp"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/pauser"
	"github.com/concourse/concourse/atc/policy"
	"github.com/concourse/concourse/atc/runinput"
	"github.com/concourse/concourse/atc/runs"
	"github.com/concourse/concourse/atc/scheduler"
	"github.com/concourse/concourse/atc/scheduler/algorithm"
	"github.com/concourse/concourse/atc/syslog"
	"github.com/concourse/concourse/atc/util"
	"github.com/concourse/concourse/atc/worker"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/atc/wrappa"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/objectstore"
	"github.com/concourse/concourse/hangar/output"
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
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

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

const algorithmLimitRows = 100

var schedulerCache = gocache.New(10*time.Second, 10*time.Second)

var defaultDriverName = "pgx"
var retryingDriverName = "retrying"

var flyClientID = "fly"
var flyClientSecret = "Zmx5"

type ATCCommand struct {
	RunCommand RunCommand `command:"run"`
	Migration  Migration  `command:"migrate"`
}

type RunCommand struct {
	mcpHandler                   http.Handler
	mcpCleanup                   ifrit.Runner
	artifactResolveCapabilityMu  sync.Mutex
	artifactResolveCapabilityKey []byte

	Logger flag.Lager

	varSourcePool creds.VarSourcePool

	// customRoles is loaded during validation and reused by the API so the
	// security check and enforcement always operate on the same mapping. It is
	// keyed on the path it was parsed from, so a path set after an earlier
	// load (as the integration suite does) is never shadowed by a stale entry.
	customRoles     map[string]string
	customRolesPath string

	// k8sArtifactLocator is shared between the Reaper and every pool's worker
	// factory. Only artifactLocator() reads or creates it.
	k8sArtifactLocator *jetbridge.ArtifactLocator
	// k8sRuntime is the runtime configuration and clientset every JetBridge
	// consumer shares. Only jetbridgeConfig() reads or assembles it.
	k8sRuntimeOnce sync.Once
	k8sRuntime     jetbridgeRuntime
	k8sRuntimeErr  error
	// k8sHangarWarrantSigner is constructed once during startup validation and
	// carried by the assembled JetBridge Config.
	k8sHangarWarrantSigner *hangar.WarrantSigner

	hangarOutputSource     *jetbridge.OutputSource
	hangarOutputStoreClose func() error

	// hangarOutputCapabilityMinter mints the capability every control call on
	// the artifact daemon's output plane presents. Built once during startup validation, from
	// the key the flag names, and handed to the worker factory.
	hangarOutputCapabilityMinter *executioncontrol.CapabilityMinter
	runOutputStarter             *runs.OutputStarter
	runTaskStarter               *runs.ExecutionStarter
	runResultFinalizer           *runs.ResultFinalizer
	runResultReader              *runs.ResultReader
	runAdmitter                  runs.Admitter
	runInputAuthority            *runinput.Authority
	outputReadSigner             *output.ReadWarrantSigner
	runCancellationSource        runs.CancellationSourcePlane

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
		PreferredStepNode                  string        `long:"kubernetes-preferred-step-node"    description:"Node label key=value every step pod prefers but does not require, e.g. kubernetes.io/hostname=big-node. Pods that do not fit there schedule elsewhere. Step pods tolerate the taint KEY=VALUE:NoSchedule, so tainting the node reserves it for builds." value-name:"KEY=VALUE"`
		RequiredStepNode                   string        `long:"kubernetes-required-step-node"     description:"Node label key=value every step pod must land on, e.g. a dedicated CI pool. Pods with no such node free wait Pending (--kubernetes-pod-scheduling-timeout). Step pods tolerate the taint KEY=VALUE:NoSchedule." value-name:"KEY=VALUE"`
		ImagePullSecrets                   []string      `long:"kubernetes-image-pull-secret"      description:"Kubernetes Secret name to use as imagePullSecrets on task Pods. Can be specified multiple times."`
		ServiceAccount                     string        `long:"kubernetes-service-account"        description:"Kubernetes ServiceAccount name to set on task Pods. Defaults to the namespace default SA."`
		StepPodGrants                      []string      `long:"kubernetes-step-pod-grant"         description:"Map a build's owner to the ServiceAccount its step pods run under: name=<grant>,owner=<team>/<pipeline>/<job>|main/one-off,service-account=<sa>. Can be specified multiple times. Every other build's pods run as --kubernetes-service-account." value-name:"GRANT"`
		CacheStore                         string        `long:"kubernetes-cache-store"            description:"Task cache backend: hostpath (node-local dirs) or emptydir (ephemeral). Empty = auto-detect."`
		CacheHostPath                      string        `long:"kubernetes-cache-host-path"        description:"Base directory on host node for persistent task caches. Caches are node-local and survive pod restarts."`
		ArtifactHelperImage                string        `long:"kubernetes-artifact-helper-image"     description:"Container image for artifact init containers. Defaults to alpine:latest."`
		ArtifactDaemonPort                 int           `long:"kubernetes-artifact-daemon-port"      default:"7780" description:"HTTP port for the DaemonSet artifact server (hostPort)."`
		ArtifactDaemonHostPath             string        `long:"kubernetes-artifact-daemon-host-path" description:"Host path for artifact storage on each node. When set, build pods require concourse.dev/artifact-cache=ready node label."`
		ArtifactDaemonResolveCapabilityKey string        `long:"kubernetes-artifact-daemon-resolve-capability-key" description:"Path to the raw 32-byte key used to authorize artifact resolve operations. The artifact daemon must be started with the same key."`
		ArtifactDaemonResolveCapabilityTTL time.Duration `long:"kubernetes-artifact-daemon-resolve-capability-ttl" default:"2h" description:"Lifetime of operation-bound resolve capabilities; must exceed pod scheduling plus startup plus the init retry budget."`
		ArtifactDaemonService              string        `long:"kubernetes-artifact-daemon-service"   default:"artifact-daemon" description:"Headless Service name for DaemonSet per-pod DNS."`
		ArtifactDaemonNamespace            string        `long:"kubernetes-artifact-daemon-namespace" description:"Namespace the artifact daemon's Service, EndpointSlices and server certificate live in. Empty means --kubernetes-namespace."`
		ArtifactDaemonWarmTimeout          time.Duration `long:"kubernetes-artifact-daemon-warm-timeout" default:"90s" description:"How long to wait for a daemon to restore a resource cache from durable storage. A miss or timeout costs a re-download, never a failed build."`
		ArtifactDaemonTLSCert              string        `long:"kubernetes-artifact-daemon-tls-cert"    description:"Path to client certificate for mTLS with the artifact daemon."`
		ArtifactDaemonTLSKey               string        `long:"kubernetes-artifact-daemon-tls-key"     description:"Path to client private key for mTLS with the artifact daemon."`
		ArtifactDaemonTLSCACert            string        `long:"kubernetes-artifact-daemon-tls-ca-cert" description:"Path to CA certificate for verifying the artifact daemon's server certificate."`
		OutputPlaneEnabled                 bool          `long:"kubernetes-hangar-output-enabled"           description:"Enable the durable output-capture extension: the capture control init, the ledger-checked stale-workspace cleanup, and the ATC's exact-execution control calls. Off, every one of those is absent and an ordinary pod is byte-identical to the one built without it."`
		OutputCaptureEnabled               bool          `long:"kubernetes-hangar-output-capture-enabled"   description:"Enable web-side durable output SELECTION. It is a second switch on top of --kubernetes-hangar-output-enabled: the base one wires the exact-execution control calls, this one is what lets an admitted task carry a capture at all. A worker without capture enabled builds no capture pod, and the refusal is at admission rather than an omission in the Pod."`
		OutputWarrantKey                   string        `long:"kubernetes-hangar-output-warrant-key"    description:"Path to the raw 32-byte key the control plane mints Hangar output CONTROL capabilities with. The artifact daemon's output plane verifies with the same key; nothing else holds it."`
		OutputWarrantKeyLegacy             string        `long:"kubernetes-hangar-output-capability-key" hidden:"true" description:"Deprecated alias for --kubernetes-hangar-output-warrant-key."`
		OutputMaterializationKey           string        `long:"kubernetes-hangar-output-materialization-key" description:"Path to the exact 32-byte key output READ WARRANTS are minted with, under the hangar-output-materialize-v1 domain. It is never the control capability key and never the foundation's strict-input materialization key."`
		OutputBucket                       string        `long:"kubernetes-hangar-output-bucket"            description:"The dedicated output bucket. The control plane derives the bucket, scope and key prefix from authenticated deployment context alone; it is here so the status surface can key a cursor by the same bucket the sweep does, and never so a caller can choose one."`
		CacheBucket                        string        `long:"kubernetes-artifact-daemon-cache-bucket"   description:"The artifact daemons' fail-open resource-cache bucket or disk namespace, if they have one. Named here only so startup can refuse a cache, input and output namespace that are not three different places (ADR-0002); web never reaches the cache."`
		InputBucket                        string        `long:"kubernetes-hangar-input-bucket"            description:"The strict-input bucket or disk namespace the artifact daemons publish trees into. Named here only so startup can refuse a cache, input and output namespace that are not three different places (ADR-0002)."`
		OutputTenant                       string        `long:"kubernetes-hangar-output-tenant"            description:"Authenticated deployment/tenant identity the opaque output scope is derived from. It is never rendered into an object key."`
		OutputStore                        string        `long:"kubernetes-hangar-output-store" default:"gcs" choice:"gcs" choice:"disk" description:"The output namespace's store profile: gcs or disk. The web's reclaim pass and orphan sweep reach it; node daemons publish and never delete."`
		OutputPrefix                       string        `long:"kubernetes-hangar-output-prefix"           description:"Deployment object-key prefix the output namespace hangs off. The orphan sweep lists under it; no caller chooses one."`
		OutputEndpoint                     string        `long:"kubernetes-hangar-output-endpoint"         description:"Output store endpoint. Empty with the gcs store is real GCS with the web's ambient credential."`
		OutputStoreID                      string        `long:"kubernetes-hangar-output-store-id"         description:"Expected disk store identity (disk store only)."`
		OutputStoreCACert                  string        `long:"kubernetes-hangar-output-store-ca-cert"    description:"Disk store CA certificate (disk store only)."`
		OutputListTokenFile                string        `long:"kubernetes-hangar-output-list-token-file"  description:"Disk store list-and-stat role credential the orphan sweep lists with (disk store only)."`
		OutputDeleteTokenFile              string        `long:"kubernetes-hangar-output-delete-token-file" description:"Disk store stat-and-delete role credential the reclaim pass and orphan sweep delete with (disk store only). Only the web holds it."`
		OutputPublicationGrace             time.Duration `long:"kubernetes-hangar-output-publication-grace" default:"192h" description:"A generation is reclaimable only once this has elapsed since its registration. Must exceed the maximum capture deadline by an hour."`
		OutputReclaimInterval              time.Duration `long:"kubernetes-hangar-output-reclaim-interval" default:"1m" description:"How often the web's reclaim pass deletes unclaimed generations."`
		OutputReclaimBatch                 int           `long:"kubernetes-hangar-output-reclaim-batch" default:"10" description:"Generations one reclaim pass reclaims."`
		OutputDeleteTimeout                time.Duration `long:"kubernetes-hangar-output-delete-timeout" default:"2m" description:"How long one conditional delete may take."`
		OutputOrphanSweepInterval          time.Duration `long:"kubernetes-hangar-output-orphan-sweep-interval" default:"1h" description:"How often the orphan sweep lists the output namespace."`
		OutputOperationTimeout             time.Duration `long:"kubernetes-hangar-output-operation-timeout" default:"1m" description:"Managed-read operation timeout. Must match the artifact daemon's output-plane operation timeout; a reader's claim and the transport cover this budget."`
		OutputCaptureDeadline              time.Duration `long:"kubernetes-hangar-output-capture-deadline"  default:"24h" description:"Maximum capture deadline offered to a daemon. Configurable from 1h to 168h."`
		HangarEnabled                      bool          `long:"kubernetes-hangar-enabled"                  description:"Enable exact immutable Hangar tree inputs for Kubernetes task Pods."`
		HangarWarrantKey                   string        `long:"kubernetes-hangar-warrant-key"           description:"Path to the raw 32-byte Hangar materialization warrant key."`
		HangarWarrantTTL                   time.Duration `long:"kubernetes-hangar-warrant-ttl"           default:"15m" description:"Lifetime of exact Hangar materialization warrants (maximum 15m)."`
		HangarWarrantKeyLegacy             string        `long:"kubernetes-hangar-capability-key" hidden:"true" description:"Deprecated alias for --kubernetes-hangar-warrant-key."`
		HangarWarrantTTLLegacy             time.Duration `long:"kubernetes-hangar-capability-ttl" hidden:"true" description:"Deprecated alias for --kubernetes-hangar-warrant-ttl."`
		ImageRegistryPrefix                string        `long:"kubernetes-image-registry-prefix"     description:"Registry path prefix for custom resource type images (e.g. gcr.io/my-project/concourse). Images are resolved as <prefix>/<type-name>."`
		ImageRegistrySecret                string        `long:"kubernetes-image-registry-secret"     description:"Kubernetes Secret name (type kubernetes.io/dockerconfigjson) for registry auth. Auto-added to imagePullSecrets on every pod."`
		BaseResourceTypes                  []string      `long:"kubernetes-base-resource-type"        description:"Override or add a base resource type image. Format: name=image (e.g. git=my-registry/git-resource:v2). Can be specified multiple times. Merges with built-in defaults." value-name:"NAME=IMAGE"`
	} `group:"Kubernetes Runtime"`

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

	LogDBQueries   bool `long:"log-db-queries" description:"Log database queries."`
	LogClusterName bool `long:"log-cluster-name" description:"Log cluster name."`

	GC struct {
		OneOffBuildGracePeriod     time.Duration `long:"one-off-grace-period" default:"5m" description:"Period after which one-off build containers will be garbage-collected."`
		MissingGracePeriod         time.Duration `long:"missing-grace-period" default:"5m" description:"Period after which to reap containers and volumes that were created but went missing from the worker."`
		HijackGracePeriod          time.Duration `long:"hijack-grace-period" default:"5m" description:"Period after which hijacked containers will be garbage collected"`
		FailedGracePeriod          time.Duration `long:"failed-grace-period" default:"120h" description:"Period after which failed containers will be garbage collected"`
		VarSourceRecyclePeriod     time.Duration `long:"var-source-recycle-period" default:"5m" description:"Period after which to reap var_sources that are not used."`
		DeprecatedScopeGracePeriod time.Duration `long:"deprecated-scope-grace-period" default:"720h" description:"Period after which deprecated resource config scopes (from resource type/source changes) will be garbage collected. Default 30 days."`
	} `group:"Garbage Collection" namespace:"gc"`

	TelemetryOptIn bool `long:"telemetry-opt-in" hidden:"true" description:"Enable anonymous concourse version reporting."`

	DefaultBuildLogsToRetain uint64 `long:"default-build-logs-to-retain" description:"Default build logs to retain, 0 means all"`
	MaxBuildLogsToRetain     uint64 `long:"max-build-logs-to-retain" description:"Maximum build logs to retain, 0 means not specified. Will override values configured in jobs"`

	DefaultDaysToRetainBuildLogs uint64 `long:"default-days-to-retain-build-logs" description:"Default days to retain build logs. 0 means unlimited"`
	MaxDaysToRetainBuildLogs     uint64 `long:"max-days-to-retain-build-logs" description:"Maximum days to retain build logs, 0 means not specified. Will override values configured in jobs"`

	JobSchedulingMaxInFlight uint64 `long:"job-scheduling-max-in-flight" default:"32" description:"Maximum number of jobs to be scheduling at the same time"`

	PipelineRunReclaimBatch int `long:"pipeline-run-reclaim-batch" default:"20" description:"Maximum number of reclaimable pipeline runs to destroy per reclaimer pass."`

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

	ConfigRBAC          flag.File `long:"config-rbac" description:"Customize RBAC role-action mapping."`
	EnableMCP           bool      `long:"enable-mcp" description:"Enable the authenticated MCP endpoint and OAuth consent flow."`
	MCPClientConfig     flag.File `long:"mcp-client-config" description:"JSON array of registered public MCP clients (client_id, client_name, redirect_uris). Required with --enable-mcp."`
	MCPDisableOperation []string  `long:"mcp-disable-operation" description:"MCP operation id to refuse regardless of the caller's scopes. Repeatable. A deployment restriction, never an authority grant."`

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

	// Deliberately not a member of the "Feature Flags" group above: the
	// three fields in that group are exactly the keys of atc.FeatureFlags(),
	// which atc/api/infoserver serves anonymously on atc.GetInfo. Whether
	// this server admits durable runs is not an anonymous fact.
	// DisableRedactSecrets is the existing precedent for a process-wide
	// setting that is deliberately outside the group and outside the map.
	PipelineRunActivationEpoch int64    `long:"pipeline-run-activation-epoch" description:"The Run contract activation epoch this web node admits pipeline runs under (the v2 create route and the run_pipeline step). Zero admits none. At startup it is written into the durable Run activation marker, which only moves forward. It is independent of the Hangar control-key generation: rotating that leaves running Runs, their finalization and invocation-key replay alone."`
	RunInputSigningKey         string   `long:"run-input-signing-key" description:"Path to a distinct raw 32-byte web-only key for temporary Run input grants. Never mount this service key in workers or node daemons."`
	RunResultScratchDir        string   `long:"run-result-scratch-dir" description:"Absolute, existing directory for spooling Run result downloads. Each read holds about twice the archive size until its response is written. A private child is created in it at startup. Empty uses the process temporary directory."`
	RunResultReadConcurrency   int      `long:"run-result-read-concurrency" default:"2" description:"Run result downloads in flight at once. Readers beyond this are refused with 503 and Retry-After rather than queued."`
	RunCredentialWorkerImages  []string `long:"run-credential-worker-image" description:"Digest-qualified image (repository@sha256:...) a Run result producer must run for session credentials to be delivered into it. Repeatable. Unset refuses every credential handoff."`
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
	atc.PipelineRunActivationEpoch = cmd.PipelineRunActivationEpoch

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

	if err := cmd.reconcileHangarEnabled(logger, backendConn); err != nil {
		for _, conn := range []Closer{apiConn, backendConn, gcConn, workerConn} {
			_ = conn.Close()
		}
		return nil, err
	}

	if err := cmd.reconcilePipelineRunActivation(logger, backendConn); err != nil {
		for _, conn := range []Closer{apiConn, backendConn, gcConn, workerConn} {
			_ = conn.Close()
		}
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
		if cmd.hangarOutputStoreClose != nil {
			_ = cmd.hangarOutputStoreClose()
		}
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

	pool, err := cmd.constructPool(dbConn, lockFactory, workerCache)
	if err != nil {
		return nil, err
	}

	// The worker factory has its own connection pool (for worker registration)
	dbWorkerFactory := db.NewWorkerFactory(workerConn, workerCache)

	credsManagers := cmd.CredentialManagers
	dbPipelineFactory := db.NewPipelineFactory(dbConn, lockFactory)
	dbPipelineRunFactory := db.NewPipelineRunFactory(dbConn, lockFactory)
	dbJobFactory := db.NewJobFactory(dbConn, lockFactory)
	dbResourceFactory := db.NewResourceFactory(dbConn, lockFactory)
	dbContainerRepository := db.NewContainerRepository(dbConn)
	dbVolumeRepository := db.NewVolumeRepository(dbConn)
	gcContainerDestroyer := gc.NewDestroyer(logger, dbContainerRepository, dbVolumeRepository)
	dbBuildFactory := db.NewBuildFactory(dbConn, lockFactory, cmd.GC.OneOffBuildGracePeriod, cmd.GC.FailedGracePeriod)
	dbCheckFactory := db.NewCheckFactory(dbConn, lockFactory, checkBuildsChan, nil)
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

	middleware := token.NewMiddleware(cmd.secureCookies())

	apiHandler, err := cmd.constructAPIHandler(
		logger,
		reconfigurableSink,
		teamFactory,
		workerTeamFactory,
		dbPipelineFactory,
		dbPipelineRunFactory,
		dbJobFactory,
		dbResourceFactory,
		dbWorkerFactory,
		dbVolumeRepository,
		dbContainerRepository,
		gcContainerDestroyer,
		dbBuildFactory,
		dbCheckFactory,
		dbResourceConfigFactory,
		userFactory,
		pool,
		secretManager,
		credsManagers,
		accessFactory,
		dbWall,
		policyChecker,
		dbSigningKeyFactory,
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
		storage,
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

	if err := cmd.constructMCPHandler(logger, dbConn, httpClient, apiHandler, accessFactory); err != nil {
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

	if cmd.mcpCleanup != nil {
		members = append(members, grouper.Member{Name: "mcp-auth-cleanup", Runner: cmd.mcpCleanup})
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
		return nil, errors.New("syslog Drainer is misconfigured, cannot configure a drainer without a transport")
	}

	syslogDrainConfigured := true
	if cmd.Syslog.Address == "" {
		syslogDrainConfigured = false
	}

	teamFactory := db.NewTeamFactory(dbConn, lockFactory)

	dbResourceCacheFactory := db.NewResourceCacheFactory(dbConn, lockFactory)
	dbResourceConfigFactory := db.NewResourceConfigFactory(dbConn, lockFactory)

	dbBuildFactory := db.NewBuildFactory(dbConn, lockFactory, cmd.GC.OneOffBuildGracePeriod, cmd.GC.FailedGracePeriod)
	dbCheckFactory := db.NewCheckFactory(dbConn, lockFactory, checkBuildsChan, util.NewSequenceGenerator(1))
	dbPipelineFactory := db.NewPipelineFactory(dbConn, lockFactory)
	dbJobFactory := db.NewJobFactory(dbConn, lockFactory)
	dbPipelineLifecycle := db.NewPipelineLifecycle(dbConn, lockFactory)
	dbPipelinePauser := db.NewPipelinePauser(dbConn, lockFactory)
	dbSigningKeyFactory := db.NewSigningKeyFactory(dbConn)

	dbWorkerFactory := db.NewWorkerFactory(dbConn, workerCache)

	alg := algorithm.New(db.NewVersionsDB(dbConn, algorithmLimitRows, schedulerCache))

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

	childRunAdmitter, err := cmd.constructChildRunAdmitter(dbConn, lockFactory, teamFactory)
	if err != nil {
		return nil, err
	}

	engine := cmd.constructEngine(
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
		childRunAdmitter,
	)

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
	}

	idtoken.UpdateGlobalManagerFactory(func(f *idtoken.ManagerFactory) {
		f.SetSigningKeyFactory(dbSigningKeyFactory)
	})

	k8sComponents, err := cmd.jetbridgeComponents(logger, dbConn, dbWorkerFactory, dbBuildFactory)
	if err != nil {
		return nil, err
	}
	components = append(components, k8sComponents...)

	components = append(components, cmd.hangarOutputComponents(dbConn)...)
	deletes, err := cmd.hangarOutputDeleteComponents(dbConn, lockFactory)
	if err != nil {
		return nil, err
	}
	components = append(components, deletes...)
	components = append(components, cmd.runComponents(dbConn)...)

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

	return components, err
}

// jetbridgeComponents builds the JetBridge runtime's own components: the
// registrar that heartbeats the synthetic worker and the Reaper. None when the
// runtime is off.
func (cmd *RunCommand) jetbridgeComponents(logger lager.Logger, dbConn db.DbConn, dbWorkerFactory db.WorkerFactory, dbBuildFactory db.BuildFactory) ([]RunnableComponent, error) {
	var components []RunnableComponent
	if cmd.Kubernetes.Namespace != "" {
		rt, err := cmd.jetbridgeConfig()
		if err != nil {
			return nil, err
		}
		k8sCfg, k8sClientset := rt.config, rt.clientset

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
		// The workers' locator, never a fresh one: the Reaper can only drop,
		// and forget, the keys the workers recorded.
		k8sReaper.SetArtifactLocator(cmd.artifactLocator())
		// The reaper reaps a completed step's pod only once its build is no
		// longer running: until then the pod's exit-status annotation is the
		// only thing that lets a restarted web resume the plan instead of
		// re-executing the step.
		k8sReaper.SetBuildLookup(dbBuildFactory)
		components = append(components, RunnableComponent{
			Component: atc.Component{
				Name: atc.ComponentK8sWorkerReaper,
			},
			Runnable: k8sReaper,
		})
	}

	return components, nil
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

// The shared clientset's client-side rate limit: three times client-go's
// default (5 QPS, burst 10), one share for each clientset it replaced.
const (
	jetbridgeClientQPS   = 15
	jetbridgeClientBurst = 30
)

// jetbridgeRuntime is the JetBridge runtime configuration, assembled once from
// the --kubernetes-* flags, with the one clientset built from it.
type jetbridgeRuntime struct {
	config     jetbridge.Config
	clientset  kubernetes.Interface
	restConfig *rest.Config
}

// jetbridgeConfig returns the runtime configuration every JetBridge consumer
// shares: both pools' worker factories, the registrar and the Reaper. It is
// assembled, checked and given its clientset on the first call; later calls
// get the same result, a failure included. Each caller gets its own copy of
// the Config value.
func (cmd *RunCommand) jetbridgeConfig() (jetbridgeRuntime, error) {
	cmd.k8sRuntimeOnce.Do(func() {
		cmd.k8sRuntime, cmd.k8sRuntimeErr = cmd.assembleJetbridgeRuntime()
	})
	return cmd.k8sRuntime, cmd.k8sRuntimeErr
}

func (cmd *RunCommand) assembleJetbridgeRuntime() (jetbridgeRuntime, error) {
	cfg, err := cmd.assembleJetbridgeConfig()
	if err != nil {
		return jetbridgeRuntime{}, err
	}
	restConfig, err := jetbridge.RestConfig(cfg)
	if err != nil {
		return jetbridgeRuntime{}, fmt.Errorf("creating k8s rest config: %w", err)
	}
	// The ATC used to build three clientsets (one per pool, one for the
	// registrar and Reaper), each with client-go's default rate limit of 5 QPS
	// and burst 10. They are now one, so it carries their combined budget
	// rather than a third of it.
	restConfig.QPS = jetbridgeClientQPS
	restConfig.Burst = jetbridgeClientBurst
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return jetbridgeRuntime{}, fmt.Errorf("creating k8s clientset: %w", err)
	}
	return jetbridgeRuntime{config: cfg, clientset: clientset, restConfig: restConfig}, nil
}

// assembleJetbridgeConfig maps the flags onto a jetbridge.Config and runs the
// startup guards that need the assembled value.
func (cmd *RunCommand) assembleJetbridgeConfig() (jetbridge.Config, error) {
	if cmd.Kubernetes.CacheStore != "" && !jetbridge.ValidCacheStores[cmd.Kubernetes.CacheStore] {
		return jetbridge.Config{}, fmt.Errorf("invalid --kubernetes-cache-store value %q (valid: hostpath, emptydir)", cmd.Kubernetes.CacheStore)
	}
	key, err := cmd.loadArtifactResolveCapabilityKey()
	if err != nil {
		return jetbridge.Config{}, err
	}

	k8sCfg := jetbridge.NewConfig(cmd.Kubernetes.Namespace, cmd.Kubernetes.Kubeconfig)
	k8sCfg.PodStartupTimeout = cmd.Kubernetes.PodStartupTimeout
	k8sCfg.PodSchedulingTimeout = cmd.Kubernetes.PodSchedulingTimeout
	if cmd.Kubernetes.PreferredStepNode != "" {
		preferred, err := jetbridge.ParseStepNodeLabel(cmd.Kubernetes.PreferredStepNode)
		if err != nil {
			return jetbridge.Config{}, fmt.Errorf("--kubernetes-preferred-step-node: %w", err)
		}
		k8sCfg.PreferredStepNode = &preferred
	}
	if cmd.Kubernetes.RequiredStepNode != "" {
		required, err := jetbridge.ParseStepNodeLabel(cmd.Kubernetes.RequiredStepNode)
		if err != nil {
			return jetbridge.Config{}, fmt.Errorf("--kubernetes-required-step-node: %w", err)
		}
		k8sCfg.RequiredStepNode = &required
	}
	k8sCfg.ImagePullSecrets = cmd.Kubernetes.ImagePullSecrets
	k8sCfg.ServiceAccount = cmd.Kubernetes.ServiceAccount
	for _, value := range cmd.Kubernetes.StepPodGrants {
		grant, err := jetbridge.ParseStepPodGrant(value)
		if err != nil {
			return jetbridge.Config{}, fmt.Errorf("--kubernetes-step-pod-grant: %w", err)
		}
		k8sCfg.StepPodGrants = append(k8sCfg.StepPodGrants, grant)
	}
	if err := jetbridge.ValidateStepPodGrants(k8sCfg.StepPodGrants); err != nil {
		return jetbridge.Config{}, fmt.Errorf("--kubernetes-step-pod-grant: %w", err)
	}
	k8sCfg.CacheStore = cmd.Kubernetes.CacheStore
	k8sCfg.CacheHostPath = cmd.Kubernetes.CacheHostPath
	k8sCfg.ArtifactHelperImage = cmd.Kubernetes.ArtifactHelperImage
	k8sCfg.ArtifactDaemonPort = cmd.Kubernetes.ArtifactDaemonPort
	k8sCfg.ArtifactDaemonHostPath = cmd.Kubernetes.ArtifactDaemonHostPath
	k8sCfg.ArtifactDaemonResolveCapabilityKey = key
	k8sCfg.ArtifactDaemonResolveCapabilityTTL = cmd.Kubernetes.ArtifactDaemonResolveCapabilityTTL
	k8sCfg.ArtifactDaemonService = cmd.Kubernetes.ArtifactDaemonService
	k8sCfg.ArtifactDaemonNamespace = cmd.Kubernetes.ArtifactDaemonNamespace
	k8sCfg.ArtifactDaemonWarmTimeout = cmd.Kubernetes.ArtifactDaemonWarmTimeout
	k8sCfg.ArtifactDaemonTLSCert = cmd.Kubernetes.ArtifactDaemonTLSCert
	k8sCfg.ArtifactDaemonTLSKey = cmd.Kubernetes.ArtifactDaemonTLSKey
	k8sCfg.ArtifactDaemonTLSCACert = cmd.Kubernetes.ArtifactDaemonTLSCACert
	k8sCfg.ArtifactDaemonTLSEnabled = jetbridge.DaemonTLSConfigured(
		cmd.Kubernetes.ArtifactDaemonTLSCert,
		cmd.Kubernetes.ArtifactDaemonTLSKey,
		cmd.Kubernetes.ArtifactDaemonTLSCACert,
	)
	k8sCfg.HangarEnabled = cmd.Kubernetes.HangarEnabled
	k8sCfg.HangarWarrantSigner = cmd.k8sHangarWarrantSigner
	k8sCfg.OutputPlaneEnabled = cmd.Kubernetes.OutputPlaneEnabled
	k8sCfg.OutputOperationTimeout = cmd.Kubernetes.OutputOperationTimeout
	if cmd.Kubernetes.ImageRegistryPrefix != "" || cmd.Kubernetes.ImageRegistrySecret != "" {
		k8sCfg.ImageRegistry = &jetbridge.ImageRegistryConfig{
			Prefix:     cmd.Kubernetes.ImageRegistryPrefix,
			SecretName: cmd.Kubernetes.ImageRegistrySecret,
		}
	}
	if len(cmd.Kubernetes.BaseResourceTypes) > 0 {
		k8sCfg.ResourceTypeImages = jetbridge.MergeResourceTypeImages(cmd.Kubernetes.BaseResourceTypes)
	}

	if err := jetbridge.ValidateResolveCapabilityConfig(k8sCfg); err != nil {
		return jetbridge.Config{}, err
	}
	return k8sCfg, nil
}

// artifactLocator is the one artifact locator of this web: every pool's worker
// factory records into it and the Reaper drops from it. It is the only place a
// locator is created. Without one, workers skip recording output locations.
func (cmd *RunCommand) artifactLocator() *jetbridge.ArtifactLocator {
	if cmd.k8sArtifactLocator == nil {
		cmd.k8sArtifactLocator = jetbridge.NewArtifactLocator()
	}
	return cmd.k8sArtifactLocator
}

func (cmd *RunCommand) constructPool(dbConn db.DbConn, lockFactory lock.LockFactory, workerCache *db.WorkerCache) (worker.Pool, error) {
	factory, workerDB, err := cmd.workerFactory(dbConn, lockFactory, workerCache)
	if err != nil {
		return worker.Pool{}, err
	}
	return worker.NewPool(factory, workerDB), nil
}

// workerFactory builds the worker factory a pool hands its workers, and the
// worker DB beside it. It is split from constructPool so the collaborators the
// factory carries (the artifact locator above all) can be checked against the
// ones the Reaper is given.
func (cmd *RunCommand) workerFactory(dbConn db.DbConn, lockFactory lock.LockFactory, workerCache *db.WorkerCache) (worker.DefaultFactory, worker.DB, error) {
	dbResourceCacheFactory := db.NewResourceCacheFactory(dbConn, lockFactory)
	dbWorkerBaseResourceTypeFactory := db.NewWorkerBaseResourceTypeFactory(dbConn)
	dbTaskCacheFactory := db.NewTaskCacheFactory(dbConn)
	dbWorkerTaskCacheFactory := db.NewWorkerTaskCacheFactory(dbConn)
	dbVolumeRepository := db.NewVolumeRepository(dbConn)
	dbWorkerFactory := db.NewWorkerFactory(dbConn, workerCache)
	dbTeamFactory := db.NewTeamFactory(dbConn, lockFactory)
	runFactory := db.NewPipelineRunFactory(dbConn, lockFactory)
	cmd.runResultFinalizer = &runs.ResultFinalizer{Conn: dbConn, Factory: runFactory}
	stepPodBuilds := db.NewBuildFactory(dbConn, lockFactory, cmd.GC.OneOffBuildGracePeriod, cmd.GC.FailedGracePeriod)

	db := worker.NewDB(
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
		DB:       db,
		Streamer: cmd.streamer(),
	}
	executionStarter := &runs.ExecutionStarter{Conn: dbConn, Factory: runFactory}
	factory.K8sExecutionPreparer = executionStarter

	if cmd.Kubernetes.Namespace != "" {
		rt, err := cmd.jetbridgeConfig()
		if err != nil {
			return worker.DefaultFactory{}, worker.DB{}, err
		}
		k8sCfg, k8sClientset, k8sRestConfig := rt.config, rt.clientset, rt.restConfig

		factory.K8sClientset = k8sClientset
		factory.K8sConfig = &k8sCfg
		factory.K8sExecutor = jetbridge.NewSPDYExecutor(k8sClientset, k8sRestConfig)
		factory.K8sArtifactLocator = cmd.artifactLocator()
		factory.K8sStepPodBuilds = stepPodBuilds
		if k8sCfg.OutputPlaneEnabled && cmd.hangarOutputCapabilityMinter != nil {
			// Both dispatch and recovery use the same node plane.
			factory.K8sOutputControls = jetbridge.NewOutputControls(k8sCfg,
				jetbridge.NewNodeIPResolver(k8sClientset),
				cmd.hangarOutputCapabilityMinter)
			source := jetbridge.NewOutputSource(k8sClientset, k8sCfg, cmd.hangarOutputCapabilityMinter)
			source.SetExecutor(factory.K8sExecutor)
			cmd.runCancellationSource = source
			if err := cmd.configureOutputReads(dbConn, source); err != nil {
				return worker.DefaultFactory{}, worker.DB{}, err
			}
			if err := cmd.configureRunInputUploads(dbConn, runFactory, dbTeamFactory, source); err != nil {
				return worker.DefaultFactory{}, worker.DB{}, err
			}
			executionStarter.Source = source
			cmd.runOutputStarter = runs.NewOutputStarter(dbConn, runFactory, source, cmd.Kubernetes.OutputCaptureDeadline)
			executionStarter.Output = cmd.runOutputStarter
			executionStarter.SetInputReadMinter(cmd.outputReadSigner)
			cmd.runTaskStarter = executionStarter
			cmd.hangarOutputSource = source
		}

		if k8sCfg.ArtifactDaemonService != "" {
			dcLogger := lager.NewLogger("daemon-client")
			dcLogger.RegisterSink(lager.NewWriterSink(os.Stderr, lager.INFO))

			// The Config picks the daemon's namespace, not this call site:
			// the daemon may live apart from the step-pod namespace.
			factory.K8sDaemonClient = jetbridge.NewDaemonClientFromConfig(dcLogger, k8sClientset, k8sCfg)
		}
	}

	return factory, db, nil
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
	dbPipelineRunReclaimLifecycle := db.NewPipelineRunReclaimLifecycle(gcConn)
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
	components = append(components, newPipelineRunReclaimerComponent(dbPipelineRunReclaimLifecycle, time.Now, cmd.PipelineRunReclaimBatch))

	return components, nil
}

// hangarOutputComponents is everything the output plane contributes to the
// component table, and the one place the plane's own flag decides whether any
// of it runs.
//
// THE CAPTURE ADVANCER ONCE RAN ON EVERY DEPLOYMENT. It was appended outside
// both the Kubernetes block and any output-plane check, so an upgraded
// deployment that never opted in -- a non-Kubernetes one included -- grew a
// `components` row, an advisory lock and a query a minute, for a plane it does
// not have. Each pass is one bounded indexed SELECT rolled back immediately,
// so the cost was small; a deployment with capture disabled must behave as it
// did before, and "small" is not that.
//
// The status component keeps its own additional condition, and it is a
// different question: the flag says this deployment HAS an output plane, and
// a configured bucket says it is configured enough to describe. A status
// surface reporting "0 live generations, not at risk" about a plane nobody
// has configured is an alert rule that will never fire looking exactly like
// coverage.
func (cmd *RunCommand) hangarOutputComponents(dbConn db.DbConn) []RunnableComponent {
	if !cmd.Kubernetes.OutputPlaneEnabled {
		return nil
	}

	components := []RunnableComponent{
		cmd.hangarOutputCaptureComponent(dbConn),
	}
	if status := cmd.hangarOutputStatusComponent(dbConn); status != nil {
		components = append(components, *status)
	}

	return components
}

// hangarOutputCoordinator is the capture coordinator: the capture rows in
// PostgreSQL and the node daemons the rows name.
func (cmd *RunCommand) hangarOutputCoordinator(dbConn db.DbConn) *hangaroutput.Coordinator {
	coordinator := &hangaroutput.Coordinator{
		Transactor: hangarOutputTransactor{conn: dbConn},
		Rows:       db.NewHangarOutputRepository(db.HangarConsumerPrefixForComponent()),
	}
	// With no runtime source the coordinator's Dial stays nil, and it refuses
	// every node-side step with a typed error.
	if source := cmd.hangarOutputSource; source != nil {
		coordinator.Dial = func(ctx context.Context, node string, uid executioncontrol.NodeUID) (hangaroutput.SourceControl, error) {
			dial, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			return source.CaptureControl(dial, node, uid)
		}
	}

	return coordinator
}

// hangarOutputCaptureComponent runs the capture sequence and its recovery:
// every pending capture through its seal and publish, every publishing one
// through recovery, every pending one past its deadline to failed, and every
// terminal one's marker released.
//
// It is a component and not a goroutine beside the step because the process
// that started a capture is exactly the process that may be gone: what has to
// survive is the row, and the ability to read it and take the next step. The
// component runner's lease makes one ATC own one pass; the CAS on the row
// makes a second one benign.
//
// A ten-second interval: a seal waits for its node's Pod to stop, and the
// pass is what asks again.
func (cmd *RunCommand) hangarOutputCaptureComponent(dbConn db.DbConn) RunnableComponent {
	return RunnableComponent{
		Component: atc.Component{Name: atc.ComponentHangarOutputCapture},
		Runnable:  cmd.hangarOutputCoordinator(dbConn),
		Interval:  10 * time.Second,
	}
}

// runComponents is what every web node registers for Runs, with or without an
// output plane and whatever its Run activation epoch.
//
// The cancellation worker is here and not among the output plane's components
// because the cancel route answers on every node, and a Run activation epoch
// of zero stops admission while leaving running Runs running: a fence accepted
// where the worker is absent would never converge. Its own operations (build
// abort, candidate settlement, terminalization) need no output plane. Without
// one the node admits no exact execution, so no capture is ever discarded.
//
// The web fleet must agree on the output plane. The worker's lease is one
// row that a live owner renews on every pass, so a node without the plane
// that holds it keeps it; an execution admitted by a node with the plane is
// then retried, its Run staying running, until the lease holder stops. The
// chart renders every web node alike.
func (cmd *RunCommand) runComponents(dbConn db.DbConn) []RunnableComponent {
	return []RunnableComponent{
		cmd.runResultsComponent(),
		cmd.runCancellationComponent(dbConn),
	}
}

// runResultsComponent makes each settled Run's one terminal publication. It
// is registered on every web node, with or without an output plane; a Run whose
// captures are still unsettled is simply left for a later pass. Build
// completion wakes it; the interval is the net under a lost wake-up.
func (cmd *RunCommand) runResultsComponent() RunnableComponent {
	return RunnableComponent{
		Component: atc.Component{Name: atc.ComponentRunResults},
		Runnable: component.RunFunc(func(ctx context.Context) error {
			if cmd.runResultFinalizer == nil {
				return nil
			}
			return cmd.runResultFinalizer.Run(ctx)
		}),
		Interval: 10 * time.Second,
	}
}

// Cancellation has its own bounded pass and nonzero fallback. It shares the
// source coordinator and the Run's immutable identities with normal completion.
func (cmd *RunCommand) runCancellationComponent(dbConn db.DbConn) RunnableComponent {
	factory := db.NewPipelineRunFactory(dbConn, nil)
	if cmd.runResultFinalizer != nil {
		factory = cmd.runResultFinalizer.Factory
	}
	executions := &runs.CancellationExecutions{Conn: dbConn, Factory: factory, Source: cmd.runCancellationSource}
	return RunnableComponent{
		Component: atc.Component{Name: atc.ComponentRunCancellation},
		Interval:  runs.CancellationPollInterval,
		Runnable: &runs.CancellationWorker{Conn: dbConn, Factory: factory, OwnerID: uuid.NewString(),
			Actions: runs.CancellationActionSet{factory, executions, runs.CancellationActionFunc(factory.ExecuteCancellationFinality)}},
	}
}

// hangarOutputTransactor adapts the connection to the coordinator's
// Transactor.
//
// It lives here rather than in atc/db because the interface belongs to the
// coordinator and atc/db has no business importing it: an interface is a
// statement of what a consumer uses, and the package that satisfies one should
// not have to know it exists.
type hangarOutputTransactor struct{ conn db.DbConn }

func (transactor hangarOutputTransactor) Begin() (hangaroutput.Transaction, error) {
	tx, err := transactor.conn.Begin()
	if err != nil {
		return nil, err
	}

	// The COMMIT answers in the output leaf's vocabulary. Two of this plane's
	// constraint triggers are DEFERRED, so their refusals arrive here and
	// nowhere earlier, and a coordinator handed an unclassified commit failure
	// would read a denial as an ambiguous commit and retry it forever.
	return db.HangarOutputTx{Tx: tx}, nil
}

func newPipelineRunReclaimerComponent(lifecycle db.PipelineRunReclaimLifecycle, now func() time.Time, batchSize int) RunnableComponent {
	return RunnableComponent{
		Component: atc.Component{Name: atc.ComponentReclaimerPipelineRuns},
		Runnable:  gc.NewPipelineRunReclaimer(lifecycle, now, batchSize),
		Interval:  time.Minute,
	}
}

func (cmd *RunCommand) validateCustomRoles() error {
	_, err := cmd.loadCustomRoles()
	return err
}

func (cmd *RunCommand) loadCustomRoles() (map[string]string, error) {
	path := cmd.ConfigRBAC.Path()
	if cmd.customRoles != nil && cmd.customRolesPath == path {
		return cmd.customRoles, nil
	}

	mapping, err := cmd.parseCustomRoles()
	if err != nil {
		return nil, err
	}

	if err = accessor.ValidateCustomRoles(mapping); err != nil {
		return nil, fmt.Errorf("failed to customize roles: %w", err)
	}

	cmd.customRoles = mapping
	cmd.customRolesPath = path
	return cmd.customRoles, nil
}

func (cmd *RunCommand) parseCustomRoles() (map[string]string, error) {
	mapping := map[string]string{}

	path := cmd.ConfigRBAC.Path()
	if path == "" {
		return mapping, nil
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open RBAC config file (%s): %w", cmd.ConfigRBAC, err)
	}

	var data map[string][]string
	if err = yaml.Unmarshal(content, &data); err != nil {
		return nil, fmt.Errorf("failed to parse RBAC config file (%s): %w", cmd.ConfigRBAC, err)
	}

	allKnownRoles := map[string]bool{}
	for _, roleName := range accessor.DefaultRoles {
		allKnownRoles[roleName] = true
	}

	for role, actions := range data {
		if _, ok := allKnownRoles[role]; !ok {
			return nil, fmt.Errorf("failed to customize roles: %w", fmt.Errorf("unknown role %s", role))
		}

		for _, action := range actions {
			if _, ok := accessor.DefaultRoles[action]; !ok {
				return nil, fmt.Errorf("failed to customize roles: %w", fmt.Errorf("unknown action %s", action))
			}
			if assignedRole, assigned := mapping[action]; assigned {
				return nil, fmt.Errorf(
					"failed to customize roles: action %s is assigned more than once (roles %s and %s)",
					action,
					assignedRole,
					role,
				)
			}
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

// secureCookies says whether web marks its cookies Secure: always when it is
// reached over https, which is the one right answer for a cookie served over
// TLS, and otherwise only when --cookie-secure forces it (a TLS-terminating
// proxy in front of an http external URL). MCP's authorization cookies
// already followed the issuer's scheme; session cookies now do too.
func (cmd *RunCommand) secureCookies() bool {
	return cmd.Auth.AuthFlags.SecureCookies ||
		(cmd.ExternalURL.URL != nil && cmd.ExternalURL.URL.Scheme == "https")
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
	if err := cmd.validateRunInputSigningKey(); err != nil {
		errs = multierror.Append(errs, err)
	}
	if err := cmd.validateRunResultReads(); err != nil {
		errs = multierror.Append(errs, err)
	}
	if err := cmd.validateRunCredentialWorkerImages(); err != nil {
		errs = multierror.Append(errs, err)
	}

	if err := cmd.validateMCPDisabledOperations(); err != nil {
		errs = multierror.Append(errs, err)
	}

	return errs.ErrorOrNil()
}

// validateMCPDisabledOperations refuses an id no operation answers to. A typo
// would otherwise leave the operation enabled while the operator believes it is
// off -- the one failure mode a deployment restriction must not have.
func (cmd *RunCommand) validateMCPDisabledOperations() error {
	known := map[string]bool{}
	for _, op := range mcp.Operations() {
		known[op.ID] = true
	}
	var errs error
	for _, id := range cmd.MCPDisableOperation {
		if !known[id] {
			errs = multierror.Append(errs, fmt.Errorf("--mcp-disable-operation: no such MCP operation: %s", id))
		}
	}
	return errs
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
// applyDeprecatedHangarWarrantFlags folds the pre-rename `-capability-` flag
// spellings into the `-warrant-` fields they now alias. A deprecated key flag
// fills an empty new one and is refused when both name different files; a
// deprecated TTL, when given, replaces the new flag's value outright, because a
// duration flag cannot tell "left at the default" from "set to the default".
func (cmd *RunCommand) applyDeprecatedHangarWarrantFlags() error {
	aliases := []struct {
		current, legacy         *string
		currentName, legacyName string
	}{
		{&cmd.Kubernetes.HangarWarrantKey, &cmd.Kubernetes.HangarWarrantKeyLegacy,
			"--kubernetes-hangar-warrant-key", "--kubernetes-hangar-capability-key"},
		{&cmd.Kubernetes.OutputWarrantKey, &cmd.Kubernetes.OutputWarrantKeyLegacy,
			"--kubernetes-hangar-output-warrant-key", "--kubernetes-hangar-output-capability-key"},
	}
	for _, alias := range aliases {
		if *alias.legacy == "" {
			continue
		}
		if *alias.current != "" && *alias.current != *alias.legacy {
			return fmt.Errorf("%s and its deprecated alias %s name different files; set only %s",
				alias.currentName, alias.legacyName, alias.currentName)
		}
		*alias.current = *alias.legacy
	}
	if cmd.Kubernetes.HangarWarrantTTLLegacy != 0 {
		cmd.Kubernetes.HangarWarrantTTL = cmd.Kubernetes.HangarWarrantTTLLegacy
	}

	return nil
}

func (cmd *RunCommand) validateK8sRuntime() error {
	if err := cmd.applyDeprecatedHangarWarrantFlags(); err != nil {
		return err
	}
	if cmd.Kubernetes.HangarEnabled && cmd.Kubernetes.Namespace == "" {
		return errors.New("--kubernetes-namespace is required when --kubernetes-hangar-enabled is set")
	}
	if err := (objectstore.Namespaces{
		Cache:  cmd.Kubernetes.CacheBucket,
		Input:  cmd.Kubernetes.InputBucket,
		Output: cmd.Kubernetes.OutputBucket,
	}).Validate(); err != nil {
		return fmt.Errorf("--kubernetes-artifact-daemon-cache-bucket, --kubernetes-hangar-input-bucket and --kubernetes-hangar-output-bucket: %w", err)
	}
	if cmd.Kubernetes.Namespace == "" {
		return nil
	}
	if cmd.Kubernetes.ArtifactDaemonHostPath == "" {
		return errors.New("--kubernetes-artifact-daemon-host-path is required when --kubernetes-namespace is set: " +
			"the DaemonSet artifact cache is mandatory for the K8s runtime, because downstream artifact reads " +
			"must not exec into the producing pod (which is reaped as soon as the step finishes)")
	}
	if err := jetbridge.ValidateDaemonTLSFlags(
		cmd.Kubernetes.ArtifactDaemonTLSCert,
		cmd.Kubernetes.ArtifactDaemonTLSKey,
		cmd.Kubernetes.ArtifactDaemonTLSCACert,
	); err != nil {
		return err
	}
	if err := cmd.validateHangarOutputPlane(); err != nil {
		return err
	}
	if !cmd.Kubernetes.HangarEnabled {
		return nil
	}
	if cmd.Kubernetes.HangarWarrantTTL <= 0 || cmd.Kubernetes.HangarWarrantTTL > hangar.MaxWarrantTTL {
		return fmt.Errorf("--kubernetes-hangar-warrant-ttl must be positive and no greater than %s", hangar.MaxWarrantTTL)
	}
	if cmd.Kubernetes.ArtifactDaemonTLSCert == "" || cmd.Kubernetes.ArtifactDaemonTLSKey == "" || cmd.Kubernetes.ArtifactDaemonTLSCACert == "" {
		return errors.New("--kubernetes-hangar-enabled requires complete artifact daemon TLS: " +
			"--kubernetes-artifact-daemon-tls-cert, --kubernetes-artifact-daemon-tls-key, and --kubernetes-artifact-daemon-tls-ca-cert")
	}
	if cmd.Kubernetes.HangarWarrantKey == "" {
		return errors.New("--kubernetes-hangar-warrant-key is required when --kubernetes-hangar-enabled is set")
	}
	if cmd.k8sHangarWarrantSigner == nil {
		key, err := os.ReadFile(cmd.Kubernetes.HangarWarrantKey)
		if err != nil {
			return fmt.Errorf("read --kubernetes-hangar-warrant-key: %w", err)
		}
		if len(key) != sha256.Size {
			return fmt.Errorf("--kubernetes-hangar-warrant-key must contain exactly %d raw bytes", sha256.Size)
		}
		signer, err := hangar.NewWarrantSigner(key, cmd.Kubernetes.HangarWarrantTTL, nil)
		if err != nil {
			return fmt.Errorf("construct Hangar materialization warrant signer: %w", err)
		}
		cmd.k8sHangarWarrantSigner = signer
	}
	return nil
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

	return team.UpdateProviderAuth(auth)
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
	childRunAdmitter exec.ChildRunAdmitter,
) engine.Engine {
	coreOptions := []engine.CoreStepFactoryOption{
		engine.WithCoreImageResolver(resolver),
		engine.WithChildRunAdmitter(childRunAdmitter),
	}
	if cmd.runTaskStarter != nil {
		coreOptions = append(coreOptions, engine.WithCoreRunTaskPreparer(cmd.runTaskStarter))
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
				coreOptions...,
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
	)
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
	webMux.Handle("/api/v2/", csrfHandler)
	webMux.Handle("/sky/issuer/", authHandler)
	webMux.Handle("/sky/", skyHandler)
	webMux.Handle("/auth/", legacyHandler)
	webMux.Handle("/login", legacyHandler)
	webMux.Handle("/logout", legacyHandler)
	webMux.Handle("/.well-known/", apiHandler)
	webMux.Handle("/", webHandler)

	// MCP uses its own bearer credentials and browser consent cookies. In
	// particular an MCP 401 must not clear an unrelated web login's cookies.
	routes := http.NewServeMux()
	routes.Handle("/", auth.WebAuthHandler{Handler: webMux, Middleware: middleware})

	if cmd.mcpHandler != nil {
		for _, path := range []string{"/api/v1/mcp", "/mcp/oauth/", "/.well-known/oauth-authorization-server/mcp/oauth", "/.well-known/oauth-protected-resource/api/v1/mcp"} {
			routes.Handle(path, cmd.mcpHandler)
		}
	}

	return wrappa.LoggerHandler{
		Logger: logger,

		Handler: wrappa.SecurityHandler{
			XFrameOptions:           cmd.Server.XFrameOptions,
			ContentSecurityPolicy:   cmd.Server.ContentSecurityPolicy,
			StrictTransportSecurity: cmd.Server.StrictTransportSecurity,

			Handler: routes,
		},
	}
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
		Logger:                      logger.Session("dex"),
		PasswordConnector:           cmd.Auth.AuthFlags.PasswordConnector,
		Users:                       cmd.Auth.AuthFlags.LocalUsers,
		Clients:                     cmd.Auth.AuthFlags.Clients,
		Expiration:                  cmd.Auth.AuthFlags.Expiration,
		IssuerURL:                   issuerURL.String(),
		RedirectURL:                 redirectURL.String(),
		SigningKey:                  cmd.Auth.AuthFlags.SigningKey.PrivateKey,
		Storage:                     storage,
		RefreshTokenIdleTimeout:     cmd.Auth.AuthFlags.RefreshTokenIdleTimeout,
		RefreshTokenAbsoluteTimeout: cmd.Auth.AuthFlags.RefreshTokenAbsoluteTimeout,
		RefreshTokenReuseInterval:   cmd.Auth.AuthFlags.RefreshTokenReuseInterval,
		ExtraClients:                cmd.mcpIdentityClients(),
	})
	if err != nil {
		return nil, err
	}

	// Dex serves /sky/issuer/* endpoints directly — no token interception.
	// JWT validation happens in the JWKS verifier on API requests.
	return token.EnsureUser(
		logger.Session("dex-server"),
		dexserver.RequireDesktopPKCE(dexServer),
		token.NewClaimsParser(),
		userFactory,
		displayUserIdGenerator,
	), nil
}

func (cmd *RunCommand) constructSkyHandler(
	logger lager.Logger,
	httpClient *http.Client,
	middleware token.Middleware,
	store storage.Storage,
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
		Logger:               logger.Session("sky"),
		TokenMiddleware:      middleware,
		OAuthConfig:          oauth2Config,
		HTTPClient:           httpClient,
		RevokeRefresh:        newRefreshRevoker(store, logger.Session("revoke-refresh")),
		RefreshTokenLifetime: cmd.Auth.AuthFlags.RefreshTokenIdleTimeout,
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
	validClients := []string{flyClientID, "fly-browser"}
	for clientId := range cmd.Auth.AuthFlags.Clients {
		validClients = append(validClients, clientId)
	}

	issuerPath, _ := url.Parse("/sky/issuer/keys")
	jwksURL := cmd.ExternalURL.URL.ResolveReference(issuerPath)

	return accessor.NewTrustedTokenVerifier(accessor.NewJWKSVerifier(jwksURL.String(), validClients))
}

func (cmd *RunCommand) constructAPIHandler(
	logger lager.Logger,
	reconfigurableSink *lager.ReconfigurableSink,
	teamFactory db.TeamFactory,
	workerTeamFactory db.TeamFactory,
	dbPipelineFactory db.PipelineFactory,
	dbPipelineRunFactory db.PipelineRunFactory,
	dbJobFactory db.JobFactory,
	dbResourceFactory db.ResourceFactory,
	dbWorkerFactory db.WorkerFactory,
	dbVolumeRepository db.VolumeRepository,
	dbContainerRepository db.ContainerRepository,
	gcContainerDestroyer gc.Destroyer,
	dbBuildFactory db.BuildFactory,
	dbCheckFactory db.CheckFactory,
	resourceConfigFactory db.ResourceConfigFactory,
	dbUserFactory db.UserFactory,
	workerPool worker.Pool,
	secretManager creds.Secrets,
	credsManagers creds.Managers,
	accessFactory accessor.AccessFactory,
	dbWall db.Wall,
	policyChecker policy.Checker,
	dbSigningKeyFactory db.SigningKeyFactory,
	dbConn db.DbConn,
) (http.Handler, error) {

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

	customRoles, err := cmd.loadCustomRoles()
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

	return api.NewHandler(
		logger,
		cmd.ExternalURL.String(),
		cmd.OIDCIssuerURL.String(),
		cmd.Server.ClusterName,
		apiWrapper,

		teamFactory,
		dbPipelineFactory,
		dbPipelineRunFactory,
		dbJobFactory,
		dbResourceFactory,
		dbWorkerFactory,
		workerTeamFactory,
		dbVolumeRepository,
		dbBuildFactory,
		dbCheckFactory,
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
		cmd.hangarStatusSource(dbConn),
		cmd.pipelineRunServices(dbConn, dbPipelineRunFactory, teamFactory),
	)
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

// loadArtifactResolveCapabilityKey reads the shared signing key once and caches
// it. Empty path means no signing, which the daemon accepts only if it too was
// started without a key.
func (cmd *RunCommand) loadArtifactResolveCapabilityKey() ([]byte, error) {
	if cmd.Kubernetes.ArtifactDaemonResolveCapabilityKey == "" {
		return nil, nil
	}
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

// validateHangarOutputPlane refuses a half-configured control plane.
//
// Two switches, checked in the order they depend on each other. The BASE
// switch wires this node's exact-execution control calls and needs the
// capability key the daemon verifies with; the CAPTURE switch needs the
// read-warrant key it mints warrants with, and it can never be on while the
// base switch is off.
//
// Every refusal here is one the chart also refuses at render time. Both, and
// deliberately: the chart is what an operator reviews, and this is what catches
// a deployment that did not come from the chart.
func (cmd *RunCommand) validateHangarOutputPlane() error {
	if err := cmd.applyDeprecatedHangarWarrantFlags(); err != nil {
		return err
	}
	if cmd.Kubernetes.OutputCaptureEnabled && !cmd.Kubernetes.OutputPlaneEnabled {
		return errors.New("--kubernetes-hangar-output-capture-enabled requires " +
			"--kubernetes-hangar-output-enabled: durable output capture is an extension of " +
			"exact execution control, and output admission can never be true while base " +
			"admission is false")
	}
	if !cmd.Kubernetes.OutputPlaneEnabled {
		return nil
	}
	if cmd.Kubernetes.OutputWarrantKey == "" {
		return errors.New("--kubernetes-hangar-output-warrant-key is required when " +
			"--kubernetes-hangar-output-enabled is set: every control operation on the output " +
			"daemon presents a capability minted with it, and a control plane that cannot mint " +
			"one can make no call at all")
	}
	// READ HERE rather than at the first call: a control plane that cannot
	// mint is one that will make no call at all, and "minted nothing" and
	// "minted successfully" are the same observable outcome on any path that
	// discovers the problem late. Until
	// this, the flag was required, compared with two other flags for
	// distinctness, and never opened -- a validated secret whose validation
	// was a statement about a file nobody had looked at.
	//
	// The consumer is the worker factory, which hands every jetbridge Worker
	// the resolver built from this minter. NOTHING SELECTS A CAPTURE YET, so
	// no production path spends a capability; what this removes is the gap
	// between a startup refusal and what it was refusing about.
	capabilityKey, err := os.ReadFile(cmd.Kubernetes.OutputWarrantKey)
	if err != nil {
		return fmt.Errorf("--kubernetes-hangar-output-warrant-key: %w", err)
	}
	minter, err := executioncontrol.NewCapabilityMinter(capabilityKey,
		executioncontrol.MaxCapabilityTTL, nil)
	if err != nil {
		return fmt.Errorf("--kubernetes-hangar-output-warrant-key: %w", err)
	}
	cmd.hangarOutputCapabilityMinter = minter
	// The output plane's transport is TLS, and only TLS: it is served by the
	// artifact daemon, whose off-node output routes refuse an operation whose
	// request carries no VERIFIED peer certificate. A partially-configured or
	// absent client credential is therefore not a weaker deployment, it is one
	// that fails at the first capture instead of at startup.
	if err := jetbridge.ValidateOutputPlaneTLS(
		cmd.Kubernetes.ArtifactDaemonTLSCert,
		cmd.Kubernetes.ArtifactDaemonTLSKey,
		cmd.Kubernetes.ArtifactDaemonTLSCACert,
	); err != nil {
		return err
	}
	if cmd.Kubernetes.OutputOperationTimeout <= 0 {
		return errors.New("--kubernetes-hangar-output-operation-timeout must be positive; a " +
			"reader's claim term is derived from it")
	}
	if err := output.ValidateCaptureDeadline(cmd.Kubernetes.OutputCaptureDeadline); err != nil {
		return fmt.Errorf("--kubernetes-hangar-output-capture-deadline: %w", err)
	}
	if !cmd.Kubernetes.OutputCaptureEnabled {
		return nil
	}
	if cmd.Kubernetes.OutputMaterializationKey == "" {
		return errors.New("--kubernetes-hangar-output-materialization-key is required when " +
			"--kubernetes-hangar-output-capture-enabled is set: a managed read is delivered " +
			"as a lease-bound warrant, and there is nothing to sign one with")
	}
	if cmd.Kubernetes.OutputMaterializationKey == cmd.Kubernetes.OutputWarrantKey {
		return errors.New("--kubernetes-hangar-output-materialization-key and " +
			"--kubernetes-hangar-output-warrant-key name the same file. A read warrant " +
			"authorizes one staged read of one object; a control capability authorizes one " +
			"operation on a daemon. One key for both means either can be spent as the other")
	}
	if cmd.Kubernetes.OutputMaterializationKey == cmd.Kubernetes.HangarWarrantKey {
		return errors.New("--kubernetes-hangar-output-materialization-key and " +
			"--kubernetes-hangar-warrant-key name the same file. The output read warrant " +
			"uses its own key and its own domain; reusing the foundation's strict-input key " +
			"would make a strict-input warrant spendable against a managed output")
	}

	key, err := os.ReadFile(cmd.Kubernetes.OutputMaterializationKey)
	if err != nil {
		return fmt.Errorf("read output materialization key: %w", err)
	}
	cmd.outputReadSigner, err = output.NewReadWarrantSigner(key)
	if err != nil {
		return err
	}
	return nil
}

// hangarOutputStatusComponent publishes the output plane's operational state:
// the in-service flag, the residue a drain waits on, and the open integrity
// findings.
//
// Nil when no output plane is configured, and that is the honest answer rather
// than a component emitting zeroes: a status surface reporting "0 residue, not
// at risk" about a plane that does not exist is an alert rule that will never
// fire looking exactly like coverage.
func (cmd *RunCommand) hangarOutputStatusComponent(dbConn db.DbConn) *RunnableComponent {
	// The same condition the admin status API answers on.
	if !cmd.Kubernetes.OutputPlaneEnabled {
		return nil
	}

	return &RunnableComponent{
		Component: atc.Component{Name: atc.ComponentHangarOutputStatus},
		Runnable: &hangaroutput.StatusPublisher{
			Reader: cmd.hangarOutputStatusReader(dbConn),
		},
		Interval: time.Minute,
	}
}
