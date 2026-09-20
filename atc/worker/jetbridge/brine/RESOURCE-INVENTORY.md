# Resource acquisition inventory

Baseline: `045da99b74`, `steps/resources.go` and the definitions it appends.
All listed consumer paths are under `steps/`. Consumers include both `res.Get`
and `resources.Get`; dependency-map lookups are called out separately.

| Resource | Scope | Factory cost before change | Consumers and use | Decision |
| --- | --- | --- | --- | --- |
| postgres | Suite | High: initdb and postmaster | No step Get; jetbridge-db dependency asserts `*postmaster`, reads runner, opens connections | Leave eager, explicitly excluded |
| jetbridge-db | Scenario | High: database clone and connections | 22 files below assert `JetbridgeDB`, use factories/repositories or forward it | Leave eager: isolation, explicitly excluded |
| task-workspace | Scenario | Low: one attributed mkdir | config.go asserts `TaskWorkspace`, reads `Dir` to write kubeconfig | Leave cheap factory and ownership-based disposer unchanged |
| real-cluster | Suite | High: envtest apiserver and etcd | 18 files / 20 Get boundaries below assert `*realCluster`; fields or forwarding | Lazy suite factory; shared `getRealCluster` accessor |
| auth-binaries | Suite | High: source discovery, RSA key, fly build, temp directory | No step Get; auth-server dependency asserts `authBinaries`; fixture reads Key, fly helpers read Fly | Lazy value with shared state; auth-server calls `ready` |
| auth-server | Scenario | High: Dex, HTTP, Postgres storage, encrypted DB connection, temp home | auth_sessions.go asserts `*AuthFixture`, reads URL and forwards fixture in AuthScenario | One `ready` call at the authentication Given, before downstream field access |
| span-capture | Scenario | High: collector subprocess, OTLP exporter, globals, temp directory | live_observability.go and volume_streaming.go assert `SpanCapture`, forward it; assertions later call spans/EventNames | `ready` at both entry points, before any span production; unused disposal leaves globals alone |

Envtest exceeds the requested 15-field-access-site threshold. No downstream
field accesses were rewritten; only the 20 resource acquisition boundaries use
the common accessor, to fulfill the explicit requirement that the live tier
must not start unused envtest. AuthFixture also has many downstream field uses,
but all flow through its single authentication Given, so they stay unchanged.
Concrete resource types, names, scopes, dependencies and disposer signatures
are retained. Shared lazy state uses sync.Once, caches startup errors, serializes
startup against disposal, and never starts an unused resource during disposal.

## jetbridge-db consumers

Every entry asserts `JetbridgeDB`.

| File | Use after assertion |
| --- | --- |
| artifact_recording.go | PersistNamedWorker; forwards DB into worker state |
| build_scheduling.go | Builder, Conn, LockFactory, TeamFactory; stores DB in scheduling state |
| gc_caches.go | Forwards into cache fixture; TeamFactory, Conn, LockFactory, BuildFactory |
| gc_containers.go | TeamFactory, WorkerFactory, ContainerRepository, Conn, LockFactory, VolumeRepository; PersistNamedWorker |
| gc_pipelines.go | TeamFactory, Conn, LockFactory, BuildFactory; stores DB in collector state |
| gc_reclamation.go | TeamFactory, ContainerRepository, VolumeRepository, Conn; PersistNamedWorker |
| hangar_settle.go | Returns DB from lookup helper; Conn transactions and queries |
| job_admission.go | Forwards to scheduling fixture; Conn and LockFactory |
| live_artifact_mirroring.go | Forwards to newLiveArtifactRecording |
| live_artifact_recording.go | Forwards to newLiveArtifactRecording; PersistNamedWorker |
| live_exec.go | Stores in LiveTaskPlan.Database |
| live_observability.go | Forwards to traced startup preparation |
| pipeline_retention.go | TeamFactory, Conn, LockFactory, Builder; stores in fixture state |
| real_peer.go | PersistNamedWorker; forwards into worker state |
| reaper.go | PersistNamedWorker, ContainerRepository, VolumeRepository, BuildFactory |
| registrar.go | WorkerFactory; stores DB in RegistrarReady |
| resource_checking.go | Conn, LockFactory; constructs check/config factories |
| sidecar_logs.go | Stores in SidecarPlan.Database |
| step_execution.go | TeamFactory, Conn, LockFactory; forwards to execution fixture |
| task_command.go | Forwards to newLiveRuntimeWorker |
| volume_streaming.go | PersistNamedWorker, TeamFactory, VolumeRepository |
| worker.go | PersistNamedWorker, TeamFactory, VolumeRepository; stores DB in WorkerReady |

## real-cluster consumers

Every baseline entry asserts `*realCluster`; every resulting entry obtains that
same type from `getRealCluster`, which starts the suite resource once.

| File | Existing fields / forwarding retained |
| --- | --- |
| artifact_recording.go | Clientset API calls/cleanup and worker state; RESTConfig executor |
| closing.go | Stores pointer in closingCacheRuntime.api |
| daemon.go | Clientset for namespace/discovery calls |
| daemon_cross_node.go | env.Config for kubeconfig; Clientset for nodes and endpoint slices |
| daemon_mirroring.go | env.Config for kubeconfig; Clientset for nodes and endpoint slices |
| exec_artifacts.go | Clientset for namespace creation/cleanup |
| exec_retry.go | RESTConfig.Host and rest.CopyConfig |
| integration.go | RESTConfig for instrumented client; Clientset in node state |
| mirror_client.go | Clientset for daemon resolver |
| pod_failure.go | Forwards pointer to observeProcessDeletion |
| podwatch_real.go | Forwards to newRealWatchOn; Clientset API calls, RESTConfig in watch state |
| process.go | Two lookups: forwards to observeDiagnosticDeletion; RESTConfig for initial-read failure observation |
| real_peer.go | Clientset API calls and state; RESTConfig for executor and daemon kubeconfig |
| real_warm.go | Clientset in WarmRollPlan |
| reaper.go | Clientset for namespace/reaper setup |
| registrar.go | Clientset for namespace/registrar setup |
| volume_streaming.go | Two lookups: Clientset API calls; Clientset/RESTConfig for executors |
| worker.go | Clientset API calls, cleanup and worker state; RESTConfig executor |
