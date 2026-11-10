// The JetBridge runtime: its own components (the registrar and the Reaper), the
// shared runtime configuration and clientset every JetBridge consumer reads,
// the artifact locator, the worker pool and worker factory, and the artifact
// daemon's resolve-capability key.

package atccmd

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/concourse/concourse/artifactcap"

	"code.cloudfoundry.org/lager/v3"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/gc"
	"github.com/concourse/concourse/atc/imageresolver"
	"github.com/concourse/concourse/atc/runs"
	"github.com/concourse/concourse/atc/worker"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

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

		// Once at startup: which step pods may hold privilege, and the
		// grants that allow it, so an operator can see the gate's state.
		var privilegedGrants []string
		for _, grant := range k8sCfg.StepPodGrants {
			if grant.Privileged {
				privilegedGrants = append(privilegedGrants, grant.Name+"="+grant.Owner.String())
			}
		}
		logger.Info("step-privilege-mode", lager.Data{"mode": k8sCfg.StepPrivilege, "privileged-grants": privilegedGrants})

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

func (cmd *RunCommand) streamer() worker.Streamer {
	return worker.NewStreamer(compression.NewGzipCompression())
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
	if cfg.StepRuntime == jetbridge.StepRuntimeJob && cfg.LauncherImage == "" {
		// The launcher is copied out of the web's own image, so it is always
		// the web's own build: a chart value naming a tag would lag the image
		// the pipeline's self-upgrade sets on the running pod.
		namespace := cmd.Kubernetes.WebPodNamespace
		if namespace == "" {
			namespace = cfg.Namespace
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		image, err := jetbridge.WebImage(ctx, clientset, namespace, cmd.Kubernetes.WebPodName)
		if err != nil {
			return jetbridgeRuntime{}, fmt.Errorf("--kubernetes-step-runtime=job: finding the launcher image from this web's pod: %w", err)
		}
		cfg.LauncherImage = image
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
	k8sCfg.StepRuntime = cmd.Kubernetes.StepRuntime
	if err := jetbridge.ValidateStepPrivilege(cmd.Kubernetes.StepPrivilege); err != nil {
		return jetbridge.Config{}, fmt.Errorf("--kubernetes-step-privilege: %w", err)
	}
	k8sCfg.StepPrivilege = cmd.Kubernetes.StepPrivilege
	k8sCfg.LauncherImage = cmd.Kubernetes.LauncherImage
	k8sCfg.StepEnvelopeURL = cmd.Kubernetes.StepEnvelopeURL
	k8sCfg.ArtifactDaemonPort = cmd.Kubernetes.ArtifactDaemonPort
	k8sCfg.ArtifactDaemonHostPath = cmd.Kubernetes.ArtifactDaemonHostPath
	k8sCfg.ArtifactDaemonResolveCapabilityKey = key
	k8sCfg.ArtifactDaemonService = cmd.Kubernetes.ArtifactDaemonService
	k8sCfg.ArtifactDaemonNamespace = cmd.Kubernetes.ArtifactDaemonNamespace
	k8sCfg.ArtifactDaemonTLSCert = cmd.Kubernetes.ArtifactDaemonTLSCert
	k8sCfg.ArtifactDaemonTLSKey = cmd.Kubernetes.ArtifactDaemonTLSKey
	k8sCfg.ArtifactDaemonTLSCACert = cmd.Kubernetes.ArtifactDaemonTLSCACert
	k8sCfg.ArtifactDaemonTLSEnabled = jetbridge.DaemonTLSConfigured(
		cmd.Kubernetes.ArtifactDaemonTLSCert,
		cmd.Kubernetes.ArtifactDaemonTLSKey,
		cmd.Kubernetes.ArtifactDaemonTLSCACert,
	)
	k8sCfg.OutputPlaneEnabled = cmd.Kubernetes.OutputPlaneEnabled
	k8sCfg.OutputOperationTimeout = cmd.Kubernetes.OutputOperationTimeout
	if cmd.Kubernetes.ImageRegistrySecret != "" {
		k8sCfg.ImageRegistry = &jetbridge.ImageRegistryConfig{
			SecretName: cmd.Kubernetes.ImageRegistrySecret,
		}
	}
	if len(cmd.Kubernetes.BaseResourceTypes) > 0 {
		k8sCfg.ResourceTypeImages = imageresolver.MergeResourceTypeImages(cmd.Kubernetes.BaseResourceTypes)
	}
	return k8sCfg, nil
}

// stepEnvelopes is the one step envelope store of this web: every worker's
// job runtime registers into it and the step envelope handler serves from it.
func (cmd *RunCommand) stepEnvelopes() *jetbridge.StepEnvelopes {
	if cmd.k8sStepEnvelopes == nil {
		cmd.k8sStepEnvelopes = jetbridge.NewStepEnvelopes()
	}
	return cmd.k8sStepEnvelopes
}

// stepEnvelopeHandler serves step envelopes to step pods under the job step
// runtime, or nil when the runtime is off.
func (cmd *RunCommand) stepEnvelopeHandler(logger lager.Logger) (http.Handler, error) {
	if cmd.Kubernetes.Namespace == "" || cmd.Kubernetes.StepRuntime != jetbridge.StepRuntimeJob {
		return nil, nil
	}
	rt, err := cmd.jetbridgeConfig()
	if err != nil {
		return nil, err
	}
	return &jetbridge.StepEnvelopeHandler{
		Logger:    logger.Session("step-envelope"),
		Clientset: rt.clientset,
		Namespace: rt.config.Namespace,
		Envelopes: cmd.stepEnvelopes(),
	}, nil
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

func (cmd *RunCommand) constructPool(logger lager.Logger, dbConn db.DbConn, lockFactory lock.LockFactory, workerCache *db.WorkerCache) (worker.Pool, error) {
	factory, workerDB, err := cmd.workerFactory(logger, dbConn, lockFactory, workerCache)
	if err != nil {
		return worker.Pool{}, err
	}
	return worker.NewPool(factory, workerDB), nil
}

// workerFactory builds the worker factory a pool hands its workers, and the
// worker DB beside it. It is split from constructPool so the collaborators the
// factory carries (the artifact locator above all) can be checked against the
// ones the Reaper is given.
func (cmd *RunCommand) workerFactory(logger lager.Logger, dbConn db.DbConn, lockFactory lock.LockFactory, workerCache *db.WorkerCache) (worker.DefaultFactory, worker.DB, error) {
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
		factory.K8sStepEnvelopes = cmd.stepEnvelopes()
		if k8sCfg.OutputPlaneEnabled && cmd.hangarSigner != nil {
			// Both dispatch and recovery use the same node plane.
			factory.K8sOutputControls = jetbridge.NewOutputControls(k8sCfg,
				jetbridge.NewNodeIPResolver(k8sClientset),
				cmd.hangarSigner)
			source := jetbridge.NewOutputSource(k8sClientset, k8sCfg, cmd.hangarSigner)
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
			if cmd.readWarrantMinter != nil {
				executionStarter.SetInputReadMinter(cmd.readWarrantMinter)
			}
			cmd.runTaskStarter = executionStarter
			cmd.hangarOutputSource = source
		}

		if k8sCfg.ArtifactDaemonService != "" {
			// The Config picks the daemon's namespace, not this call site:
			// the daemon may live apart from the step-pod namespace.
			factory.K8sDaemonClient = jetbridge.NewDaemonClientFromConfig(logger.Session("daemon-client"), k8sClientset, k8sCfg)
		}
	}

	return factory, db, nil
}

// loadArtifactResolveCapabilityKey reads the shared signing key once and caches
// it. validateK8sRuntime has already refused a missing path.
func (cmd *RunCommand) loadArtifactResolveCapabilityKey() ([]byte, error) {
	cmd.artifactResolveCapabilityMu.Lock()
	defer cmd.artifactResolveCapabilityMu.Unlock()
	if len(cmd.artifactResolveCapabilityKey) != 0 {
		return append([]byte(nil), cmd.artifactResolveCapabilityKey...), nil
	}
	key, err := artifactcap.LoadKeyFile(cmd.Kubernetes.ArtifactDaemonResolveCapabilityKey)
	if err != nil {
		return nil, fmt.Errorf("--kubernetes-artifact-daemon-resolve-capability-key: %w", err)
	}
	cmd.artifactResolveCapabilityKey = append([]byte(nil), key...)
	return append([]byte(nil), key...), nil
}
