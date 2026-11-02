// RunCommand's flags and the checks on them: the ATCCommand and RunCommand
// structs whose tags go-flags parses, WireDynamicFlags (which hangs the
// credential managers and metric emitters off their flag groups), validate and
// every validate* it runs, the custom-role mapping it loads, and the small
// accessors that turn flag values into what the wiring consumes (default
// container limits, bind addresses, the default external URL).

package atccmd

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/creds"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/atc/mcp"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/runinput"
	"github.com/concourse/concourse/atc/runs"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/output"
	"github.com/concourse/concourse/skymarshal/skycmd"
	"github.com/concourse/concourse/tracing"
	"github.com/concourse/flag/v2"
	"github.com/hashicorp/go-multierror"
	"github.com/jessevdk/go-flags"
	"github.com/tedsuo/ifrit"
	"go.yaml.in/yaml/v3"
)

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
	// hangarSigner holds the one Hangar key and mints every warrant the web
	// issues: read, execution control and output capture. Built once during
	// startup validation from the key --kubernetes-hangar-key names, and
	// carried by the assembled JetBridge Config and the output-plane clients.
	hangarSigner *hangar.Signer

	hangarOutputSource     *jetbridge.OutputSource
	hangarOutputStoreClose func() error

	// readWarrantMinter mints the read warrant a managed read carries. It is
	// a nil interface unless output capture is enabled, never a typed nil.
	readWarrantMinter     hangaroutput.WarrantMinter
	runOutputStarter      *runs.OutputStarter
	runTaskStarter        *runs.ExecutionStarter
	runResultFinalizer    *runs.ResultFinalizer
	runResultReader       *runs.ResultReader
	runAdmitter           runs.Admitter
	runInputAuthority     *runinput.Authority
	runCancellationSource runs.CancellationSourcePlane

	BindIP   flag.IP `long:"bind-ip"   default:"0.0.0.0" description:"IP address on which to listen for web traffic."`
	BindPort uint16  `long:"bind-port" default:"8080"    description:"Port on which to listen for HTTP traffic."`

	TLSBindPort uint16    `long:"tls-bind-port" description:"Port on which to listen for HTTPS traffic."`
	TLSCert     flag.File `long:"tls-cert"      description:"File containing an SSL certificate."`
	TLSKey      flag.File `long:"tls-key"       description:"File containing an RSA private key, used to encrypt HTTPS traffic."`
	TLSCaCert   flag.File `long:"tls-ca-cert"   description:"File containing the client CA certificate, enables mTLS"`

	ExternalURL   flag.URL `long:"external-url" description:"URL used to reach any ATC from the outside world."`
	OIDCIssuerURL flag.URL `long:"oidc-issuer-url" description:"URL to use as the OIDC issuer for IDToken generation. If not set, defaults to external-url. Must be publicly accessible for cloud provider OIDC verification."`

	Postgres flag.PostgresConfig `group:"PostgreSQL Configuration" namespace:"postgres"`

	APIMaxOpenConnections     int `long:"api-max-conns" description:"The maximum number of open connections for the api connection pool." default:"10"`
	BackendMaxOpenConnections int `long:"backend-max-conns" description:"The maximum number of open connections for the backend connection pool." default:"50"`

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
		// accepted and ignored; remove after the next release — the chart stops rendering it in this change
		ArtifactDaemonResolveCapabilityTTL time.Duration `long:"kubernetes-artifact-daemon-resolve-capability-ttl" default:"2h" description:"Accepted and ignored; capability expiry is derived from pod timeouts."`
		ArtifactDaemonService              string        `long:"kubernetes-artifact-daemon-service"   default:"artifact-daemon" description:"Headless Service name for DaemonSet per-pod DNS."`
		ArtifactDaemonNamespace            string        `long:"kubernetes-artifact-daemon-namespace" description:"Namespace the artifact daemon's Service, EndpointSlices and server certificate live in. Empty means --kubernetes-namespace."`
		ArtifactDaemonTLSCert              string        `long:"kubernetes-artifact-daemon-tls-cert"    description:"Path to client certificate for mTLS with the artifact daemon."`
		ArtifactDaemonTLSKey               string        `long:"kubernetes-artifact-daemon-tls-key"     description:"Path to client private key for mTLS with the artifact daemon."`
		ArtifactDaemonTLSCACert            string        `long:"kubernetes-artifact-daemon-tls-ca-cert" description:"Path to CA certificate for verifying the artifact daemon's server certificate."`
		OutputPlaneEnabled                 bool          `long:"kubernetes-hangar-output-enabled"           description:"Enable the durable output-capture extension: the capture control init, the ledger-checked stale-workspace cleanup, and the ATC's exact-execution control calls. Off, every one of those is absent and an ordinary pod is byte-identical to the one built without it."`
		OutputCaptureEnabled               bool          `long:"kubernetes-hangar-output-capture-enabled"   description:"Enable web-side durable output SELECTION. It is a second switch on top of --kubernetes-hangar-output-enabled: the base one wires the exact-execution control calls, this one is what lets an admitted task carry a capture at all. A worker without capture enabled builds no capture pod, and the refusal is at admission rather than an omission in the Pod."`
		OutputBucket                       string        `long:"kubernetes-hangar-output-bucket"            description:"The dedicated output bucket. The control plane derives the bucket, scope and key prefix from authenticated deployment context alone; it is here so the status surface can key a cursor by the same bucket the sweep does, and never so a caller can choose one."`
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
		HangarKey                          string        `long:"kubernetes-hangar-key"                   description:"Path to the raw 32-byte Hangar key the web signs every warrant with: read, execution control and output capture. Required with --kubernetes-hangar-output-enabled. Never present in a task pod."`
		HangarWarrantTTL                   time.Duration `long:"kubernetes-hangar-warrant-ttl"           default:"15m" description:"Lifetime of the warrants the web mints (maximum 15m)."`
		ImageRegistrySecret                string        `long:"kubernetes-image-registry-secret"     description:"Kubernetes Secret name (type kubernetes.io/dockerconfigjson) for registry auth. Auto-added to imagePullSecrets on every pod."`
		BaseResourceTypes                  []string      `long:"kubernetes-base-resource-type"        description:"Override or add a base resource type image. Format: name=image (e.g. git=my-registry/git-resource:v2). Can be specified multiple times. Merges with built-in defaults." value-name:"NAME=IMAGE"`
	} `group:"Kubernetes Runtime"`

	CLIArtifactsDir flag.Dir `long:"cli-artifacts-dir" description:"Directory containing downloadable CLI binaries."`
	WebPublicDir    flag.Dir `long:"web-public-dir" description:"Web public/ directory to serve live for local development."`

	Metrics struct {
		Attributes          map[string]string `long:"metrics-attribute" description:"A key-value attribute to attach to emitted metrics. Can be specified multiple times." value-name:"NAME:VALUE"`
		CaptureErrorMetrics bool              `long:"capture-error-metrics" description:"Enable capturing of error log metrics"`

		Prometheus metric.PrometheusConfig `group:"Metric Emitter (Prometheus)"`
	} `group:"Metrics & Diagnostics"`

	Tracing tracing.Config `group:"Tracing" namespace:"tracing"`

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

	DefaultBuildLogsToRetain uint64 `long:"default-build-logs-to-retain" description:"Default build logs to retain, 0 means all"`
	MaxBuildLogsToRetain     uint64 `long:"max-build-logs-to-retain" description:"Maximum build logs to retain, 0 means not specified. Will override values configured in jobs"`

	DefaultDaysToRetainBuildLogs uint64 `long:"default-days-to-retain-build-logs" description:"Default days to retain build logs. 0 means unlimited"`
	MaxDaysToRetainBuildLogs     uint64 `long:"max-days-to-retain-build-logs" description:"Maximum days to retain build logs, 0 means not specified. Will override values configured in jobs"`

	JobSchedulingMaxInFlight uint64 `long:"job-scheduling-max-in-flight" default:"32" description:"Maximum number of jobs to be scheduling at the same time"`

	DefaultCpuLimit    *int    `long:"default-task-cpu-limit" description:"Default max number of cpu shares per task, 0 means unlimited"`
	DefaultMemoryLimit *string `long:"default-task-memory-limit" description:"Default maximum memory per task, 0 means unlimited"`

	DefaultCpuRequest    *int    `long:"default-task-cpu-request" description:"Default CPU request (shares) per task for Burstable QoS"`
	DefaultMemoryRequest *string `long:"default-task-memory-request" description:"Default memory request per task for Burstable QoS"`

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

	BaseResourceTypeDefaults flag.File `long:"base-resource-type-defaults" description:"Base resource type defaults"`

	DisplayUserIdPerConnector map[string]string `long:"display-user-id-per-connector" description:"Define how to display user ID for each authentication connector. Format is <connector>:<fieldname>. Valid field names are user_id, name, username and email, where name maps to claims field username, and username maps to claims field preferred username"`

	DefaultGetTimeout  time.Duration `long:"default-get-timeout" description:"Default timeout of get steps"`
	DefaultPutTimeout  time.Duration `long:"default-put-timeout" description:"Default timeout of put steps"`
	DefaultTaskTimeout time.Duration `long:"default-task-timeout" description:"Default timeout of task steps"`

	DisableRedactSecrets bool `long:"disable-redact-secrets" description:"Disables secret redaction in build logs."`

	RunInputSigningKey        string   `long:"run-input-signing-key" description:"Path to a distinct raw 32-byte web-only key for temporary Run input grants. Never mount this service key in workers or node daemons."`
	RunCredentialWorkerImages []string `long:"run-credential-worker-image" description:"Digest-qualified image (repository@sha256:...) a Run result producer must run for session credentials to be delivered into it. Repeatable. Unset refuses every credential handoff."`
}

func (cmd *RunCommand) WireDynamicFlags(commandFlags *flags.Command) {
	var (
		credsGroup *flags.Group
		authGroup  *flags.Group
	)

	groups := commandFlags.Groups()
	for i := 0; i < len(groups) && (credsGroup == nil || authGroup == nil); i++ {
		group := groups[i]

		if credsGroup == nil && group.ShortDescription == "Credential Management" {
			credsGroup = group
		}

		if authGroup == nil && group.ShortDescription == "Authentication" {
			authGroup = group
		}

		groups = append(groups, group.Groups()...)
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

	skycmd.WireConnectors(authGroup)
	skycmd.WireTeamConnectors(authGroup.Find("Authentication (Main Team)"))
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

func (cmd *RunCommand) validate() error {
	var errs *multierror.Error

	switch {
	case cmd.TLSBindPort == 0:
		if cmd.TLSCert != "" || cmd.TLSKey != "" {
			errs = multierror.Append(
				errs,
				errors.New("must specify --tls-bind-port to use TLS"),
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
			errors.New("must specify --tls-cert and --tls-key to use TLS"),
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
	if err := cmd.validateRunCredentialWorkerImages(); err != nil {
		errs = multierror.Append(errs, err)
	}

	if err := cmd.validateMCPDisabledOperations(); err != nil {
		errs = multierror.Append(errs, err)
	}

	if err := cmd.validateJobSchedulingMaxInFlight(); err != nil {
		errs = multierror.Append(errs, err)
	}

	return errs.ErrorOrNil()
}

// validateJobSchedulingMaxInFlight refuses 0. The scheduler sizes its guard on
// jobs being scheduled at once from this value, so 0 leaves no slot: the first
// job blocks the scheduler forever while it holds the scheduler lock, no web
// schedules a job again, and the web cannot shut down.
func (cmd *RunCommand) validateJobSchedulingMaxInFlight() error {
	if cmd.JobSchedulingMaxInFlight == 0 {
		return errors.New("--job-scheduling-max-in-flight must be at least 1")
	}
	return nil
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
func (cmd *RunCommand) validateK8sRuntime() error {
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
	if err := cmd.validateHangarKey(); err != nil {
		return err
	}
	return cmd.validateHangarOutputPlane()
}

// validateHangarKey loads the one Hangar key and builds the one signer every
// warrant the web mints comes from. It is required the moment the output
// plane is on: every managed read and every control call it makes carries a
// warrant signed with it.
//
// Idempotent: a test may set cmd.hangarSigner before validation, and a signer
// already built is kept.
func (cmd *RunCommand) validateHangarKey() error {
	if !cmd.Kubernetes.OutputPlaneEnabled {
		return nil
	}
	if cmd.Kubernetes.HangarWarrantTTL <= 0 || cmd.Kubernetes.HangarWarrantTTL > hangar.MaxWarrantTTL {
		return fmt.Errorf("--kubernetes-hangar-warrant-ttl must be positive and no greater than %s", hangar.MaxWarrantTTL)
	}
	if cmd.hangarSigner != nil {
		return nil
	}
	if cmd.Kubernetes.HangarKey == "" {
		return errors.New("--kubernetes-hangar-key is required when --kubernetes-hangar-output-enabled " +
			"is set: every warrant the web mints is signed with it")
	}
	key, err := os.ReadFile(cmd.Kubernetes.HangarKey)
	if err != nil {
		return fmt.Errorf("read --kubernetes-hangar-key: %w", err)
	}
	if len(key) != hangar.WarrantKeyBytes {
		return fmt.Errorf("--kubernetes-hangar-key must contain exactly %d raw bytes", hangar.WarrantKeyBytes)
	}
	signer, err := hangar.NewSigner(key, cmd.Kubernetes.HangarWarrantTTL, nil)
	if err != nil {
		return fmt.Errorf("construct the Hangar signer: %w", err)
	}
	cmd.hangarSigner = signer
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

// validateHangarOutputPlane refuses a half-configured control plane.
//
// Two switches, checked in the order they depend on each other. The BASE
// switch wires this node's exact-execution control calls, signed with the
// Hangar key validateHangarKey already loaded; the CAPTURE switch mints the
// read warrant with that same key, and it can never be on while the base
// switch is off.
//
// Every refusal here is one the chart also refuses at render time. Both, and
// deliberately: the chart is what an operator reviews, and this is what catches
// a deployment that did not come from the chart.
func (cmd *RunCommand) validateHangarOutputPlane() error {
	if cmd.Kubernetes.OutputCaptureEnabled && !cmd.Kubernetes.OutputPlaneEnabled {
		return errors.New("--kubernetes-hangar-output-capture-enabled requires " +
			"--kubernetes-hangar-output-enabled: durable output capture is an extension of " +
			"exact execution control, and output admission can never be true while base " +
			"admission is false")
	}
	if !cmd.Kubernetes.OutputPlaneEnabled {
		return nil
	}
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
	cmd.readWarrantMinter = output.ReadWarrantMinter{Signer: cmd.hangarSigner}
	return nil
}
