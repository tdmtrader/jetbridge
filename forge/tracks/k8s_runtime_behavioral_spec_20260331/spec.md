# K8s Runtime Behavioral Specification

> Track: `k8s_runtime_behavioral_spec_20260331`
> Type: docs
> Status: Active

## Overview

This specification defines the observable behavioral contract for Concourse's JetBridge Kubernetes runtime — the layer responsible for executing pipeline steps as Kubernetes pods, managing their lifecycle, and providing resilience and observability guarantees.

This spec covers everything EXCEPT artifact storage and volume streaming (covered separately in `jetbridge_storage_behavioral_spec_20260330`). Together, the two specs form the complete JetBridge runtime contract.

## Scope

- Pod execution lifecycle (exec mode and direct mode)
- Pod naming and labeling
- Sidecar container support
- Failure detection and diagnostics
- Pod cleanup and garbage collection
- Worker registration and heartbeat
- Pod watch and status monitoring
- Configuration and defaults

## Out of Scope

- Artifact daemon HTTP API (DA-* requirements)
- Artifact registry and alias store (AR-* requirements)
- Peer discovery and cross-node resolution (PD-* requirements)
- Volume types and streaming (VT-* requirements)
- Container artifact orchestration (CO-* requirements)
- Resource caching flow (RC-* requirements)

These are covered in the Storage & Artifact Behavioral Specification.

---

## Section 1: Pod Execution Lifecycle

### PE-01: Exec mode pod creation
When Run() is called on a container with an executor configured, the system MUST:
- Check if a pod with the container's pod name already exists
- If the pod exists and is Running, reuse it without deletion
- If the pod exists and is in terminal state (Succeeded/Failed), delete it and create a fresh pause pod
- If no pod exists, create a new pause pod with command `trap 'exit 0' TERM; sleep 86400 & wait`
- Set termination grace period to 10 seconds
- Increment the ContainersCreated metric

### PE-02: Direct mode pod creation
When Run() is called on a container without an executor, the system MUST:
- Embed the actual command directly into the pod spec (not exec via SPDY)
- Create and return a Process that streams logs and polls for completion
- Increment the ContainersCreated metric

### PE-03: Pod spec invariants
When building any pod spec, the system MUST:
- Set RestartPolicy to Never
- Set ImagePullPolicy to PullIfNotPresent
- Apply image pull secrets from configuration
- Apply service account from configuration (if specified)
- Create pods in the configured namespace

### PE-04: Security context
When building container security contexts, the system MUST:
- Set `Privileged=true` for privileged containers
- Set `AllowPrivilegeEscalation=false` for non-privileged containers
- Leave pod-level SecurityContext empty (no RunAsNonRoot enforcement)

### PE-05: Image reference resolution
When resolving image references, the system MUST:
- Strip `docker:///` prefix
- Strip `docker://` prefix
- Strip `raw:///` prefix
- Map resource type names to their configured image URIs

### PE-06: Environment variable merging
When building container environment, the system MUST merge environment variables from both the container spec and the process spec, with process spec values taking precedence on conflict.

### PE-07: Resource requirements
When container limits or requests are specified, the system MUST:
- Map CPU values to millicores (int64)
- Map Memory and EphemeralStorage values to bytes (binary SI)
- Create Guaranteed QoS (requests equal limits) when only limits are specified
- Create Burstable QoS (independent requests) when both limits and requests are specified
- Create BestEffort QoS (empty requirements) when neither is specified

### PE-08: Exec mode command execution
When executing commands in exec mode, the system MUST:
- Wait for the pause pod to reach Running state (enforcing PodStartupTimeout)
- Execute the command via Kubernetes SPDY exec API inside the "main" container
- Support stdin, stdout, and stderr streams (any may be nil)
- Support TTY mode (combines stdout/stderr into single stream)
- Extract exit code from ExecExitError on command failure

### PE-09: Direct mode process completion
When Wait() is called on a direct-mode process, the system MUST:
- Stream pod logs to ProcessIO.Stdout in a background goroutine
- Poll pod status until the main container terminates
- Extract exit code from container's Terminated state
- Default to exit code 0 for PodSucceeded, 1 for PodFailed (when no container status found)
- Delete the pod after process exits

### PE-10: Context cancellation behavior
When the context is cancelled:
- In direct mode: delete the pod with 5-second timeout and return context error
- In exec mode: do NOT delete the pod (preserved for fly hijack; GC cleans up later)

### PE-11: Exit status persistence
When a process completes in exec mode, the system MUST:
- Store exit status in container's in-memory properties map
- Persist exit status as pod annotation `concourse.ci/exit-status` for crash recovery
- Persist resource-result as pod annotation when applicable

### PE-12: Attach and reattachment
When Attach() is called on an existing container, the system MUST:
- Check in-memory properties first for cached exit status
- If found, return an exitedProcess with stored exit status immediately
- If not found, query K8s API for the pod
- For exec-mode pods, check pod annotations for persisted exit status
- If pod has completion annotation, return exitedProcess
- If pod has no completion annotation, return error (engine will re-exec via Run())
- If pod doesn't exist, return error (engine will call Run())

---

## Section 2: Pod Naming

### PN-01: Build step pod names
When generating pod names for build step containers (pipeline + job context), the system MUST use format:
- `<pipeline>-<job>-b<build>-<type>-<suffix>` (with build number)
- `<pipeline>-<job>-<type>-<suffix>` (without build number)
- Where suffix is first 8 hex characters of handle (hyphens stripped)

### PN-02: Check container pod names
When generating pod names for check containers, the system MUST use format:
- `chk-<resource-name>-<suffix>`

### PN-03: Resource type pod names
When generating pod names for resource type get/put steps without full job context, the system MUST use format:
- `rt-<step-name>-<type>-<suffix>`

### PN-04: Fallback pod names
When metadata is insufficient to generate a readable name, the system MUST fall back to the raw handle (UUID) as the pod name.

### PN-05: Name sanitization
When sanitizing name segments, the system MUST:
- Convert all characters to lowercase
- Replace underscores, dots, and spaces with hyphens
- Remove non-alphanumeric characters (except hyphens)
- Collapse consecutive hyphens into single hyphen
- Trim leading and trailing hyphens

### PN-06: Length constraints
When generating pod names, the system MUST:
- Cap total pod name at 63 characters (Kubernetes DNS label limit)
- Allocate available space evenly between pipeline and job segments
- Cap each segment at 20 characters for readability
- Trim trailing hyphen after truncation

### PN-07: Label storage for handle correlation
When creating pod labels, the system MUST:
- Include `concourse.ci/handle` label with the DB container handle (UUID)
- Include `concourse.ci/type` label with container type (task, get, put, check)
- Include `concourse.ci/worker` label with worker name
- Include metadata labels for pipeline, job, build, step, build-id when available
- Truncate all label values to 63 characters maximum

---

## Section 3: Sidecar Lifecycle

### SC-01: Sidecar pod spec building
When a container spec includes sidecars, the system MUST:
- Add each sidecar as a separate container in the pod spec (after the main container)
- Use each sidecar's specified image, command, args, and environment
- Set ImagePullPolicy to PullIfNotPresent for each sidecar

### SC-02: Sidecar volume sharing
When building sidecar containers, the system MUST give each sidecar the same volume mounts as the main container (inputs, outputs, caches, scratch paths).

### SC-03: Sidecar working directory
When building sidecar containers, the system MUST:
- Use the sidecar's specified WorkingDir if provided
- Inherit the main container's working directory if the sidecar does not specify one

### SC-04: Sidecar security context
When building sidecar security contexts, the system MUST set `AllowPrivilegeEscalation=false` for all sidecars (unprivileged regardless of main container privilege mode).

### SC-05: Sidecar port exposure
When sidecar ports are specified, the system MUST:
- Convert port specs to Kubernetes ContainerPort objects
- Default to TCP protocol if protocol is not specified

### SC-06: Sidecar resource requirements
When sidecar resource requirements are specified, the system MUST apply CPU and memory limits/requests independently from the main container.

### SC-07: Sidecar log streaming
When streaming logs from a pod with sidecars, the system MUST:
- Stream sidecar logs in background goroutines parallel to main container logs
- Write to dedicated per-sidecar event writers (from ProcessIO.SidecarWriters) if available
- Fall back to prefixing sidecar output as `[sidecar-name] <line>` on shared stdout
- Retry log stream attachment with 500ms delay if container is not ready

### SC-08: Sidecar failure before main starts
When any container (including sidecars) enters a terminal waiting state (ImagePullBackOff, ErrImagePull, CrashLoopBackOff, InvalidImageName, CreateContainerConfigError) BEFORE the main container has terminated, the system MUST fail the entire pod immediately.

### SC-09: Sidecar failure after main exits
When the main container has already terminated, sidecar failures in terminal waiting states MUST be ignored — the exit code from the main container determines the result.

### SC-10: Pod stays running with sidecars
When the main container exits while sidecars are still running, the pod phase remains Running. The system MUST check for main container termination even when pod phase is Running (not just Succeeded/Failed).

### SC-11: Sidecar log stream completion
When waiting for sidecar log streams to complete, the system MUST bound the wait to 5 seconds. If exceeded, proceed without waiting (sidecar streams do not block process completion).

---

## Section 4: Resilience & Failure Handling

### RF-01: OOM kill detection
When any container is killed due to memory exhaustion, the system MUST detect it by checking `ContainerStatus.State.Terminated.Reason == "OOMKilled"` OR `ContainerStatus.LastTerminationState.Terminated.Reason == "OOMKilled"` (to catch OOM kills from previous restart cycles). The system MUST:
- Record a K8s pod failure metric tagged as "OOMKilled"
- Write pod diagnostics to stderr
- Return error: `"pod failed: OOMKilled: container %q exceeded memory limit"`

### RF-02: Image pull failure detection
When any container's Waiting.Reason is "ImagePullBackOff" or "ErrImagePull", the system MUST:
- Increment the K8s image pull failure metric counter
- Record a K8s pod failure metric tagged with the reason
- Write pod diagnostics to stderr
- Return error: `"pod failed: %s: %s"`

### RF-03: CrashLoopBackOff detection
When any container's Waiting.Reason is "CrashLoopBackOff", the system MUST:
- Record a K8s pod failure metric tagged as "CrashLoopBackOff"
- Write pod diagnostics to stderr
- Return error: `"pod failed: CrashLoopBackOff: %s"`

### RF-04: Additional terminal waiting states
When any container's Waiting.Reason is "InvalidImageName" or "CreateContainerConfigError", the system MUST fail the pod immediately with diagnostics.

### RF-05: Pod eviction detection
When pod phase is "Failed" AND pod reason is "Evicted", the system MUST:
- Record a K8s pod failure metric tagged as "Evicted"
- Write pod diagnostics to stderr
- Write node diagnostics to stderr (pressures, spot/preemptible labels, cordoned status)
- Return error: `"pod failed: Evicted: %s"`

### RF-06: External pod deletion detection
When the watch API receives a `watch.Deleted` event for the pod, the system MUST:
- Write pod diagnostics to stderr
- Write node diagnostics to stderr
- Return error: `"pod deleted externally: %s"`

### RF-07: Unschedulable pod detection
When pod condition PodScheduled has status=False and reason=Unschedulable, the system MUST:
- Write pod diagnostics to stderr
- Return error: `"pod failed: Unschedulable: %s"`

### RF-08: Startup timeout enforcement
When waiting for a pod to reach Running state, the system MUST enforce a startup timeout (default: 5 minutes, configurable via Config.PodStartupTimeout). On timeout, the system MUST:
- Write pod diagnostics to stderr
- Return error: `"timed out waiting for pod to start (timeout: %s, phase: %s)"`

### RF-09: Failure detection priority order
When checking for terminal failures, the system MUST check in this order:
1. OOMKilled (most actionable)
2. Terminal waiting states (ImagePullBackOff, ErrImagePull, CrashLoopBackOff, InvalidImageName, CreateContainerConfigError)
3. Evicted
4. Unschedulable
5. Exit code (container terminated)

This ensures OOM kills are not masked by generic CrashLoopBackOff.

### RF-10: Pod diagnostics output format
When writing pod diagnostics to stderr, the system MUST include:
- Pod namespace and name
- Node name (if assigned)
- Pod phase, reason, and message
- Pod conditions (status=False or non-empty reason)
- Container waiting states with reason and message
- Container terminated states with reason and exit code
- Container termination messages
- Container restart count and last termination info (if restart count > 0)

### RF-11: Node diagnostics output
When writing node diagnostics, the system MUST (best-effort with 3-second timeout):
- Report node pressure conditions (MemoryPressure, DiskPressure, PIDPressure when status=True)
- Report node not-ready condition
- Report GKE spot/preemptible instance labels
- Report Azure spot instance label
- Report AWS spot instance label (eks.amazonaws.com/capacityType=SPOT)
- Report cordoned/unschedulable node status

### RF-12: Transient error classification
The system MUST classify these K8s API errors as transient (retryable):
- Server timeout (500/503)
- Service unavailable (503)
- Too many requests (429)
- Internal error (500)
- Network-level errors (net.Error interface)
- URL errors (url.Error interface)

Non-transient errors MUST be returned unchanged.

### RF-13: Transient error wrapping
When a transient K8s API error is detected, the system MUST wrap it as a TransientError implementing `runtime.RetryableError.IsRetryable()` returning true, allowing the step engine to retry.

### RF-14: Init container failure reporting
When an init container fails (terminated with non-zero exit code) during exec-mode startup, the system MUST:
- Fetch init container logs (best-effort)
- Return error with init container name, state, and logs

### RF-15: Exec mode failure context
When exec-mode operations (exec, upload, stream) fail, the system MUST call fetchPodFailureContext() to:
- Fetch pod state with 3-second timeout (best-effort)
- Write pod diagnostics and node diagnostics to stderr
- If pod no longer exists, report that it was likely deleted by kubelet or GC

---

## Section 5: Pod Cleanup & Garbage Collection

### GC-01: Reaper pod discovery
When the reaper runs, the system MUST list all pods in the configured namespace with label selector `concourse.ci/worker=k8s-<namespace>`.

### GC-02: Fast cleanup path
When a pod has the annotation `concourse.ci/exit-status`, the system MUST:
- Delete the pod immediately (without waiting for DB GC cycle)
- Skip adding it to the active container list for DB reporting
- Handle NotFound errors silently (pod already gone)
- Continue processing other pods if deletion fails (non-blocking)

### GC-03: Handle extraction from pods
When extracting container handles from pods, the system MUST:
- Use the `concourse.ci/handle` label value if present and non-empty
- Fall back to using pod.Name as the handle if label is absent or empty
- Build a handle-to-podName mapping for later pod deletion lookup

### GC-04: Active container reporting
When the reaper has extracted handles from remaining (non-completed) pods, the system MUST call UpdateContainersMissingSince() to mark any DB containers for this worker NOT in the handles list as "missing since now".

### GC-05: DB container destruction
When active containers have been reported, the system MUST call DestroyContainers() to remove DB records for containers whose pods no longer exist.

### GC-06: Orphan pod detection
When DB destruction completes, the system MUST call DestroyUnknownContainers() to insert "destroying" records for pods that exist in K8s but have no matching DB container record.

### GC-07: Destroying pod deletion
When processing containers in "destroying" state, the system MUST:
- Look up pod name from handle-to-podName mapping
- Fall back to using handle as pod name if not in mapping (backward compatibility)
- Delete the pod via K8s API
- Handle NotFound silently (pod already gone)
- Log and return error for other deletion failures

### GC-08: Artifact store cleanup
When pod deletion completes, the system MUST clean up artifact store entries (DaemonSet mode):
- Validate handle strings (reject empty, path-traversal attempts: starting with "/", containing "..")
- Resolve artifact node via ArtifactLocator
- Resolve node IP via NodeIPResolver
- Send HTTP DELETE to `http://<nodeIP>:<port>/artifacts/steps/<handle>`
- Use 10-second timeout on HTTP requests
- Remove artifact locator entry after cleanup (best-effort)
- Continue on all errors (non-blocking, best-effort cleanup)

### GC-09: Default artifact daemon port
When ArtifactDaemonPort is 0 (unconfigured), the system MUST use port 7780 as default.

---

## Section 6: Worker Registration

### WR-01: Worker name derivation
The worker name MUST be deterministically derived as `k8s-<namespace>` (e.g., `k8s-concourse`).

### WR-02: Registration metadata
When Register() is called, the system MUST save a worker to the DB with:
- Name: `k8s-<namespace>`
- Platform: `"linux"`
- State: `"running"`
- Version: Concourse worker version constant
- ActiveContainers: count of pods with the worker label in the namespace
- ResourceTypes: mapped from configured or default resource type images

### WR-03: Heartbeat TTL
When saving the worker, the system MUST use a heartbeat TTL of 30 seconds. The same Register() call is used for both initial registration and heartbeat refresh (idempotent).

### WR-04: Active container counting
When counting active containers, the system MUST list pods with label selector `concourse.ci/worker=k8s-<namespace>` and return the count.

### WR-05: Default resource type images
The system MUST provide default image mappings for: time, registry-image, git, s3, docker-image, pool, semver, mock.

### WR-06: Resource type overrides
When resource type image overrides are configured (format: `name=image`), the system MUST:
- Start from default mappings
- Apply overrides (replacing defaults)
- Silently skip malformed entries (no "=", empty name, or empty image)

---

## Section 7: Pod Watch & Monitoring

### PW-01: Lazy watch establishment
When PodWatcher is created, the system MUST NOT establish a K8s watch. The watch MUST be established lazily on the first call to Next().

### PW-02: Initial sync
On the first call to Next(), the system MUST perform a Get() call to retrieve the current pod state BEFORE establishing a watch. This prevents missing state changes that occurred before the watch was established.

### PW-03: Field selector
When establishing a watch, the system MUST use field selector `metadata.name=<podName>` to receive events only for the specific pod.

### PW-04: Resource version tracking
The system MUST track the last observed pod ResourceVersion from every event and use it when reconnecting the watch after a channel close. This ensures no events are missed across reconnections.

### PW-05: Watch reconnection
When the watch channel closes, the system MUST:
- Set the watcher to nil
- Re-establish the watch using the last observed ResourceVersion
- Track consecutive watch errors

### PW-06: Fallback to Get()
When watch establishment fails 3 consecutive times (maxConsecutiveAPIErrors), the system MUST fall back to a single Get() call instead of retrying watch establishment. The next call to Next() will attempt to re-establish the watch.

### PW-07: Pod deletion event
When a `watch.Deleted` event is received, the system MUST return the pod with ErrPodDeleted error, signaling external deletion (eviction, node failure, manual deletion, spot preemption).

### PW-08: Non-pod event filtering
When a non-pod event is received (e.g., Status objects on error), the system MUST skip it and continue reading from the channel.

### PW-09: Stop and cleanup
When Stop() is called, the system MUST close the watcher and prevent further Next() calls.

### PW-10: Initial sync retry
When the initial Get() fails, the system MUST retry up to 3 consecutive times. If all retries fail, return error with count of consecutive failures.

---

## Section 8: Observability Events

### OE-01: Pod scheduled event
When pod condition PodScheduled becomes True, the system MUST emit span event `pod.scheduled` with `node.name` attribute (one-time).

### OE-02: Pod initialized event
When pod condition Initialized becomes True, the system MUST emit span event `pod.initialized` (one-time).

### OE-03: Image pulling event
When a container enters ContainerCreating waiting state, the system MUST emit span event `image.pulling` with container name and image attributes.

### OE-04: Image pulled event
When a container transitions out of ContainerCreating, the system MUST emit span event `image.pulled` with container name and image attributes.

### OE-05: Init container completion event
When an init container terminates with exit code 0, the system MUST emit span event `init.container.completed`.

### OE-06: Init container failure event
When an init container terminates with non-zero exit code, the system MUST emit span event `init.container.failed` with exit code and reason attributes.

### OE-07: Sidecar started event
When a non-main container reaches Running state, the system MUST emit span event `sidecar.started` with container name attribute.

### OE-08: Pod phase change events
When pod phase changes, the system MUST emit span event `pod.phase.<phase>` (e.g., `pod.phase.pending`, `pod.phase.running`).

### OE-09: Event deduplication
Each observability event MUST be emitted at most once per pod lifecycle (tracked by podEventTracker).

### OE-10: Metrics recording
The system MUST record:
- `K8sPodFailure` metric with reason (OOMKilled, ImagePullBackOff, ErrImagePull, CrashLoopBackOff, Evicted, Unschedulable)
- `K8sImagePullFailures` counter (incremented on ImagePullBackOff or ErrImagePull)
- `K8sPodStartupDuration` with successful startup time in milliseconds

---

## Section 9: Configuration

### CF-01: Namespace default
When namespace is empty, the system MUST default to `"default"`.

### CF-02: Pod startup timeout default
When PodStartupTimeout is 0, the system MUST use the default of 5 minutes.

### CF-03: Kubeconfig resolution
When KubeconfigPath is set, the system MUST use file-based kubeconfig. When empty, the system MUST use in-cluster authentication (ServiceAccount mount).

### CF-04: Cache store backends
The system MUST support two cache store backends:
- `"hostpath"`: Node-local directories that survive pod restarts on the same node
- `"emptydir"`: Ephemeral volumes lost on pod termination

When CacheStore is empty, auto-select based on configuration (prefer hostpath if DaemonSet backend available).

### CF-05: Image registry
When ImageRegistry is configured, the system MUST:
- Use the Prefix as registry path prefix for custom resource type images
- Auto-add SecretName to imagePullSecrets on every pod (if non-empty)

### CF-06: Artifact helper image default
When ArtifactHelperImage is empty, the system MUST use `alpine:latest`.

### CF-07: Mount path constants
The system MUST use:
- Cache base path: `/concourse/cache`
- Artifact mount path: `/artifacts`
- Worker label key: `concourse.ci/worker`
- Type label key: `concourse.ci/type`
- Handle label key: `concourse.ci/handle`

---

## Acceptance Criteria

1. Every requirement has a unique, traceable ID
2. Each requirement describes observable behavior, not implementation details
3. A coverage matrix maps each requirement to existing test code
4. Gaps are identified with priority (P1: must-have, P2: should-have, P3: nice-to-have)
5. All existing tests continue to pass
