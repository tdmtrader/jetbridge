@PE-03 @PE-04 @PE-07 @CO-04 @CO-05 @CO-06 @CO-07 @CO-08 @CF-04 @CF-05 @SC-01 @SC-02 @SC-03 @SC-04
Feature: What a step's pod actually looks like

  A container spec is a description of what a build step needs. This is what
  Kubernetes is asked for on its behalf: the directories the step can write to,
  which of them survive the pod, how much of the node it may use, what it is
  allowed to do, and what runs alongside it.

  Source: k8s_runtime_behavioral_spec_20260331 (PE-03, PE-04, PE-07, CF-04,
  CF-05) and jetbridge_storage_behavioral_spec_20260330 (CO-04 to CO-08).
  Migrated from container_test.go.

  # CO-04. A step gets its working directory plus one volume per input.
  @CO-04
  Scenario: A step sees its working directory and every input
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "input-vol-handle" built from image "docker:///busybox"
    And it works in "/tmp/build/workdir"
    And it takes an input at "/tmp/build/workdir/input-a"
    And it takes an input at "/tmp/build/workdir/input-b"
    When the container runs
    Then the pod has 3 volumes
    And the step sees a volume mounted at "/tmp/build/workdir"
    And the step sees a volume mounted at "/tmp/build/workdir/input-a"
    And the step sees a volume mounted at "/tmp/build/workdir/input-b"
    And every volume is ephemeral

  # CO-05. An output written into an input's directory must not get a second
  # volume, or the step would write into one and the next step would read the
  # other.
  @CO-05
  Scenario: An output that shares an input's path gets one volume, not two
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "overlap-handle" built from image "docker:///busybox"
    And it works in "/tmp/build/workdir"
    And it takes an input at "/tmp/build/workdir/shared"
    And it produces an output at "/tmp/build/workdir/shared"
    When the container runs
    Then the pod has 2 volumes
    And the step sees a volume mounted at "/tmp/build/workdir/shared"

  @CO-05
  Scenario: An output on its own path gets its own volume
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "distinct-handle" built from image "docker:///busybox"
    And it works in "/tmp/build/workdir"
    And it takes an input at "/tmp/build/workdir/in"
    And it produces an output at "/tmp/build/workdir/out"
    When the container runs
    Then the pod has 3 volumes
    And the step sees a volume mounted at "/tmp/build/workdir/in"
    And the step sees a volume mounted at "/tmp/build/workdir/out"

  # CO-08. Scratch space is ephemeral by design — persisting it across pods
  # would leak one build's temporary state into the next.
  @CO-08
  Scenario: Scratch space never outlives the pod
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "scratch-handle" built from image "docker:///busybox"
    And it works in "/tmp/build/workdir"
    And it uses scratch space at "/tmp/scratch"
    When the container runs
    Then the step sees a volume mounted at "/tmp/scratch"
    And the volume mounted at "/tmp/scratch" is lost with the pod

  @CO-07
  Scenario: A cache and a scratch path are different volumes
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "cache-scratch-handle" built from image "docker:///busybox"
    And it works in "/tmp/build/workdir"
    And it caches "/tmp/cache"
    And it uses scratch space at "/tmp/scratch"
    When the container runs
    Then the pod has 3 volumes
    And the step sees a volume mounted at "/tmp/cache"
    And the step sees a volume mounted at "/tmp/scratch"

  # PE-07. The QoS class is what the kubelet uses to decide which pods to evict
  # first when a node runs out of memory, so it is the observable consequence
  # of the resource envelope — not an implementation detail.
  @PE-07
  Scenario: Limits alone reserve exactly what they cap
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "limits-handle" built from image "docker:///busybox"
    And it is limited to 1024 CPU shares and 1073741824 bytes of memory
    When the container runs
    Then the step may use at most "1024m" CPU and "1Gi" memory
    And the step is reserved "1024m" CPU and "1Gi" memory
    And the pod is scheduled as "Guaranteed"

  @PE-07
  Scenario: Independent requests below the limits make the pod burstable
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "burstable-handle" built from image "docker:///busybox"
    And it is limited to 2048 CPU shares and 4294967296 bytes of memory
    And it requests 512 CPU shares and 1073741824 bytes of memory
    When the container runs
    Then the step may use at most "2048m" CPU and "4Gi" memory
    And the step is reserved "512m" CPU and "1Gi" memory
    And the pod is scheduled as "Burstable"

  @PE-07
  Scenario: A step that asks for nothing is evicted first
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "nolimits-handle" built from image "docker:///busybox"
    When the container runs
    Then the pod is scheduled as "BestEffort"

  # PE-04. Privilege is the difference between a task that can mount things and
  # one that cannot; getting it backwards is a security hole in one direction
  # and a broken pipeline in the other.
  @PE-04
  Scenario: An unprivileged step cannot gain privileges
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "unpriv-handle" built from image "docker:///busybox"
    When the container runs
    Then the step cannot escalate its privileges

  @PE-04
  Scenario: A privileged step is granted privilege
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "priv-handle" built from image "docker:///busybox"
    And it runs privileged
    When the container runs
    Then the step can escalate its privileges

  # PE-03
  @PE-03
  Scenario: A step's pod is never restarted behind the scheduler's back
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "restart-handle" built from image "docker:///busybox"
    When the container runs
    Then the pod is never restarted

  # SC-01 to SC-04. A sidecar exists to serve the step — a database for an
  # integration test, a log shipper. It must see what the step sees, and it must
  # not inherit the step's privilege.
  @SC-01
  Scenario: A step with no sidecars runs alone
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "nosidecar-handle" built from image "docker:///busybox"
    When the container runs
    Then the pod runs 1 containers

  @SC-01 @SC-02 @SC-04
  Scenario: A sidecar runs alongside the step and shares its working set
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "sidecar-handle" built from image "docker:///busybox"
    And it works in "/tmp/build/workdir"
    And it takes an input at "/tmp/build/workdir/input-a"
    And a sidecar "postgres" runs "postgres:15" alongside it
    When the container runs
    Then the pod runs 2 containers
    And the sidecar "postgres" runs image "postgres:15"
    And the sidecar "postgres" sees the same volumes as the step
    And the sidecar "postgres" cannot escalate its privileges

  @SC-01
  Scenario: Several sidecars all run
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "multisidecar-handle" built from image "docker:///busybox"
    And a sidecar "postgres" runs "postgres:15" alongside it
    And a sidecar "redis" runs "redis:7" alongside it
    When the container runs
    Then the pod runs 3 containers
    And the sidecar "postgres" runs image "postgres:15"
    And the sidecar "redis" runs image "redis:7"

  # SC-03: a sidecar inherits the step's working directory unless it names one.
  @SC-03
  Scenario: A sidecar inherits the step's working directory
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "sidecar-inherit-handle" built from image "docker:///busybox"
    And it works in "/tmp/build/workdir"
    And a sidecar "helper" runs "busybox" alongside it
    When the container runs
    Then the sidecar "helper" works in "/tmp/build/workdir"

  @SC-03
  Scenario: A sidecar that names a working directory keeps its own
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "sidecar-own-handle" built from image "docker:///busybox"
    And it works in "/tmp/build/workdir"
    And a sidecar "helper" runs "busybox" alongside it
    And the sidecar "helper" declares its working directory as "/opt/helper"
    When the container runs
    Then the sidecar "helper" works in "/opt/helper"

  # CF-05. Registry credentials are an operator setting that has to reach every
  # pod, or images fail to pull with an error that looks like a pipeline bug.
  @CF-05
  Scenario: Operator-configured pull secrets and service account reach the pod
    Given a jetbridge worker that pulls with the secrets "registry-creds,gcr-key" as the service account "ci-runner"
    And a task container "secrets-handle" built from image "docker:///busybox"
    When the container runs
    Then the pod pulls images using the secret "registry-creds"
    And the pod pulls images using the secret "gcr-key"
    And the pod runs as the service account "ci-runner"

  @CF-05
  Scenario: With nothing configured the pod names no credentials
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "nosecrets-handle" built from image "docker:///busybox"
    When the container runs
    Then the pod names no image pull secret and no service account

  @CF-05
  Scenario: A private registry's secret is added to the pod
    Given a jetbridge worker pulling from a private registry with secret "gcr-auth", already pulling with "existing-secret"
    And a task container "registry-handle" built from image "docker:///busybox"
    When the container runs
    Then the pod pulls images using the secret "gcr-auth"
    And the pod pulls images using the secret "existing-secret"

  # Adding it twice would be harmless to Kubernetes but is a sign the merge is
  # wrong, and the original suite guarded it explicitly.
  @CF-05
  Scenario: A registry secret the operator already listed is not duplicated
    Given a jetbridge worker pulling from a private registry with secret "gcr-auth", already pulling with "gcr-auth"
    And a task container "registry-dup-handle" built from image "docker:///busybox"
    When the container runs
    Then the pod names the secret "gcr-auth" exactly once

  # PE-07, the clause the QoS scenarios do not reach. Requests without limits
  # reserve capacity without capping it — a step that may burst but must be
  # guaranteed a floor.
  @PE-07
  Scenario: Requests without limits reserve a floor but set no ceiling
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "requests-only-handle" built from image "docker:///busybox"
    And it requests 512 CPU shares and 1073741824 bytes of memory
    When the container runs
    Then the step is reserved "512m" CPU and "1Gi" memory
    And the pod is scheduled as "Burstable"

  # A step that writes a large artifact to local disk is evicted without an
  # ephemeral-storage reservation, and that eviction reads as an unexplained
  # failure rather than a capacity problem.
  @PE-07
  Scenario: Local disk is reserved and capped like any other resource
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "ephemeral-handle" built from image "docker:///busybox"
    And it is limited to 2147483648 bytes of local disk, requesting 1073741824
    When the container runs
    Then the step may use at most "2Gi" of local disk, reserving "1Gi"

  # SC-07. A sidecar exists to serve the step — a database, a log shipper. If
  # its output never reaches the build, a user debugging a failing integration
  # test has no way to see why the database rejected the connection.
  @SC-07
  Scenario: A sidecar's output reaches its own stream when there is one
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "sc07-dedicated" built from image "docker:///busybox"
    And a sidecar "postgres" runs "postgres:15" alongside it
    When the step runs with a dedicated log stream for sidecar "postgres"
    Then the sidecar's output arrives on its own stream

  # And when there is no separate pane for it, the output still has to appear —
  # labelled, so it is distinguishable from the step's own.
  @SC-07
  Scenario: Without a separate stream the sidecar's output is labelled in the build log
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "sc07-fallback" built from image "docker:///busybox"
    And a sidecar "postgres" runs "postgres:15" alongside it
    When the step runs with nowhere separate to put sidecar output
    Then the sidecar's output is folded into the build log, labelled "[postgres]"

  # CO-07 / CF-04. A cache exists to survive between builds, and whether it
  # does depends on which storage backs it and what key it is filed under. A
  # key that varied per build would give a directory that is always empty —
  # indistinguishable from a working cache that never hits.
  @CO-07 @CF-04
  Scenario: A cache is kept on the node, under a key stable across builds
    Given a jetbridge worker keeping caches on the node under "/var/concourse/cache"
    And a task container "cache-hostpath-handle" built from image "docker:///busybox"
    And it works in "/tmp/build/workdir"
    And it belongs to job 7 step "compile"
    And it caches "/tmp/build/workdir/.cache"
    When the container runs
    Then the cache at "/tmp/build/workdir/.cache" is kept on the node under "/var/concourse/cache/job-7-compile-"

  # A one-off build (`fly execute`) has no job to key on, so there is nothing
  # stable to file a cache under and it falls back to ephemeral storage.
  @CO-07
  Scenario: A one-off build with no job gets an ephemeral cache
    Given a jetbridge worker keeping caches on the node under "/var/concourse/cache"
    And a task container "cache-oneoff-handle" built from image "docker:///busybox"
    And it works in "/tmp/build/workdir"
    And it caches "/tmp/build/workdir/.cache"
    When the container runs
    Then the volume mounted at "/tmp/build/workdir/.cache" is lost with the pod

  # CF-04. The operator's explicit choice overrides the artifact store's
  # default, in both directions.
  @CF-04
  Scenario Outline: An explicit cache store overrides the artifact store default
    Given a jetbridge worker with an artifact store, told to keep caches "<store>"
    And a task container "cache-<store>-handle" built from image "docker:///busybox"
    And it works in "/tmp/build/workdir"
    And it belongs to job 7 step "compile"
    And it caches "/tmp/build/workdir/.cache"
    When the container runs
    Then the volume mounted at "/tmp/build/workdir/.cache" <fate>

    Examples:
      | store    | fate                  |
      | hostpath | survives the pod      |
      | emptydir | is lost with the pod  |

  # A check container's working directory must be ephemeral EVEN WHEN the
  # worker keeps step data on the node. The same container handle is reused
  # for every check of a resource, so node-local storage would carry one
  # check's state into the next one — a check would see the previous check's
  # files and could report a version it never fetched.
  #
  # Every other pod scenario runs on a worker with no storage backend, where
  # every volume is ephemeral anyway and the distinction cannot be observed.
  # NOTE the Given. "keeping caches on the node" sets CacheHostPath, which
  # governs CACHE volumes only; the step's working directory comes from the
  # STORAGE BACKEND, installed by ArtifactDaemonHostPath. Written against the
  # cache worker this scenario passed the mutation unchanged, because that
  # worker still has no backend and every volume is ephemeral regardless.
  @CO-08
  Scenario: A check's workspace is ephemeral even on a node-local worker
    Given a jetbridge worker with an artifact store, told to keep caches "node"
    And a check container "check-ephemeral-handle" built from image "docker:///busybox"
    And it works in "/tmp/build/check"
    When the container runs
    Then the volume mounted at "/tmp/build/check" is lost with the pod

  # The contrast that gives the scenario above its teeth: the SAME worker
  # gives a task's working directory node-local storage. Without both, an
  # "ephemeral" assertion cannot distinguish a check being handled correctly
  # from a backend that was never configured.
  Scenario: A task's workspace on the same worker is kept on the node
    Given a jetbridge worker with an artifact store, told to keep caches "node"
    And a task container "task-hostpath-handle" built from image "docker:///busybox"
    And it works in "/tmp/build/workdir"
    When the container runs
    Then the volume mounted at "/tmp/build/workdir" survives the pod

  # A reused container starts on top of whatever the previous run left in its
  # node-local workspace, so the pod clears it first. Without that, a retried
  # step meets its own half-written outputs — the "destination path already
  # exists" failure.
  Scenario: A retried step clears the workspace its last attempt left behind
    Given a jetbridge worker with an artifact store, told to keep caches "node"
    And a task container "reused-handle" built from image "docker:///busybox"
    And it works in "/tmp/build/workdir"
    And the container has run before on this worker
    When the container runs
    Then the pod clears the workspace left by the previous run

  # The other direction: a fresh container has nothing to clean, and the
  # cleanup would only cost an image pull on every step.
  Scenario: A first attempt does not clear a workspace nothing has used
    Given a jetbridge worker with an artifact store, told to keep caches "node"
    And a task container "fresh-handle" built from image "docker:///busybox"
    And it works in "/tmp/build/workdir"
    When the container runs
    Then the pod does not clear the workspace

  # A step reads its inputs from the artifact daemon running on its own node,
  # so the pod must not be scheduled where that daemon is not. Without the
  # requirement the scheduler is free to place it on a node with no artifact
  # cache, and the step cannot read its inputs at all.
  Scenario: A pod is pinned to a node that can serve its artifacts
    Given a jetbridge worker with an artifact store, told to keep caches "node"
    And a task container "affinity-handle" built from image "docker:///busybox"
    And it works in "/tmp/build/workdir"
    When the container runs
    Then the pod is only scheduled where the artifact cache is ready

  # Inputs arrive in the workspace via init containers that run before the
  # step's own command. Without them the step starts against an empty
  # directory and fails on a file it was handed.
  Scenario: A step's inputs are fetched before its command runs
    Given a jetbridge worker with an artifact store, told to keep caches "node"
    And a task container "fetch-handle" built from image "docker:///busybox"
    And it works in "/tmp/build/workdir"
    And it takes an input at "/tmp/build/workdir/from-earlier" produced by an earlier step
    When the container runs
    Then the pod fetches its inputs before the step starts

  # ==========================================================================
  # Measured gaps: production was mutated, the Go suite in
  # behavioral_permutations_test.go went red, and every scenario above stayed
  # green. One scenario each.
  # ==========================================================================

  # CO-05 again, one layer down. The CO-05 scenario near the top of this file
  # pins that an output sharing an input's path gets ONE volume rather than
  # two. What it cannot see is where that volume lives, because it runs on a
  # worker with no storage backend, where the answer is "an emptyDir" for
  # every volume in the pod.
  #
  # The shared directory has to sit under the OUTPUT's name. When the step
  # finishes, the runtime records what it produced under the key
  # "<handle>/<output name>" and tells the node's daemon to serve that key
  # from <store>/steps/<handle>/<output name>. Name the directory after the
  # input instead and the two halves disagree: the step writes to one path,
  # the next step's fetch resolves a key naming another, and the consumer gets
  # an empty directory. It reads as a producing step that emitted nothing,
  # which is the hardest kind of pipeline failure to place — both steps
  # succeeded.
  @CO-05
  Scenario: An input sharing an output's path is filed under the output's name
    Given a jetbridge worker with an artifact store
    And a task container "overlap-store-handle" built from image "docker:///busybox"
    And it works in "/tmp/build/workdir"
    When the container runs with an input and the output "repo-modified" both at "/tmp/build/workdir/repo"
    Then the volume mounted at "/tmp/build/workdir/repo" is the node directory recorded for the output "repo-modified"

  # CO-08. A scratch path may be written relative to the step's working
  # directory, and it means a directory inside it. Resolved against the
  # filesystem root instead, the pod mounts the ephemeral volume at
  # "/tmp-work" while the task — which was told its scratch space is
  # "tmp-work" — keeps writing under its working directory. Nothing fails
  # outright, and that is the problem: the writes land in the WORKSPACE
  # volume, so on a worker that keeps step data on the node the temporary
  # state outlives the pod and greets the next attempt on the same handle,
  # while the emptyDir the pod really does carry stays empty for the life of
  # the build.
  @CO-08
  Scenario: A relative scratch path lands inside the working directory
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "rel-scratch-handle" built from image "docker:///busybox"
    And it works in "/tmp/build/workdir"
    And it uses scratch space at "tmp-work"
    When the container runs
    Then the step sees a volume mounted at "/tmp/build/workdir/tmp-work"

  # CF-04, the clause the outline above cannot reach: what happens when the
  # operator names NO cache store at all. The storage backend answers, and a
  # cache goes to the node beside the artifacts.
  #
  # Defaulting to ephemeral instead is silent. The cache directory is there,
  # it is writable, and it is empty on every build; a cache that never hits
  # looks exactly like a cache that is merely cold. A pipeline pays the full
  # dependency download every build, forever, and nothing anywhere reports a
  # problem. The key is asserted with it because an unstable key is the same
  # failure by another route.
  @CO-07 @CF-04
  Scenario: With no explicit choice, caches follow the artifact store onto the node
    Given a jetbridge worker with an artifact store
    And a task container "cache-default-handle" built from image "docker:///busybox"
    And it works in "/tmp/build/workdir"
    And it belongs to job 7 step "compile"
    And it caches "/tmp/build/workdir/.cache"
    When the container runs
    Then the cache at "/tmp/build/workdir/.cache" is kept on the node under "/var/concourse/artifacts/caches/job-7-compile-"

  # THE ORDER OF THE INIT CONTAINERS IS THE BEHAVIOUR. Kubernetes runs them in
  # the order the spec lists them, to completion, one after another. The
  # cleanup container runs `rm -rf` over this step's whole directory under the
  # store; the fetch container writes this step's inputs into exactly that
  # directory. Listed the other way round, the pod deletes the inputs it has
  # just fetched and the step begins against an empty workspace — a retried
  # step failing on a file its first attempt was handed, with nothing in the
  # build log to say why, and only on retries.
  #
  # The two scenarios above ask whether each container is present. Presence
  # survives the swap; only the order does not.
  Scenario: A retried step clears its workspace before fetching, not after
    Given a jetbridge worker with an artifact store
    And a task container "order-handle" built from image "docker:///busybox"
    And it works in "/tmp/build/workdir"
    And it takes an input at "/tmp/build/workdir/from-earlier" produced by an earlier step
    And the container has run before on this worker
    When the container runs
    Then the pod clears the workspace before it fetches its inputs

  # A check container's handle is reused for every check of the same resource,
  # so a check that finds its own row already there is not crash recovery — it
  # is every check after the first. It still gets no cleanup container: its
  # workspace is an emptyDir, which arrives empty with the pod, so there is
  # nothing stale for a cleanup to remove.
  #
  # Giving it one is not merely wasteful. The cleanup container mounts the
  # artifact store's hostPath, and a check's pod does not carry that volume at
  # all — the API server rejects a container mounting a volume the pod never
  # defines, so the pod is never created and checking stops for every resource
  # on the worker.
  #
  # NOTE the When. This rule is keyed on the type the CONTAINER SPEC declares,
  # which is what check_step.go sets; the general "the container runs"
  # sentence leaves that field empty, so a scenario written on it cannot tell
  # a check from a task here.
  @CO-08
  Scenario: A repeated check is not handed a workspace to clear
    Given a jetbridge worker with an artifact store
    And a check container "check-reused-handle" built from image "docker:///busybox"
    And it works in "/tmp/build/check"
    When the same check runs again
    Then the check's pod does not try to clear a workspace it never kept

  # ==========================================================================
  # Third wave: the on-disk layout of step volumes.
  #
  # Everything above asks WHICH STORAGE backs a volume — node or ephemeral —
  # and that question has now been asked from several angles. None of it asks
  # WHERE on the node the volume lands, and that is the half the rest of the
  # runtime has to agree with: when a step finishes, RecordOutputs registers
  # each thing it produced under the daemon key "<handle>/<name>" and tells
  # the node's daemon to serve that key from <store>/steps/<handle>/<name>.
  #
  # When the pod and RecordOutputs choose different names for the same
  # directory, nothing fails where the mistake is. The producing step writes
  # its files and exits 0. The daemon, asked for a key it has never seen,
  # creates the directory empty and serves it. The consumer starts against an
  # empty input, and the build breaks inside a task several steps away, on a
  # file the pipeline plainly handed it — with both steps green in the UI.
  # That is why these scenarios assert the WHOLE path rather than which kind
  # of storage it is.
  #
  # Measured: production was mutated, the Go suite in
  # behavioral_permutations_test.go went red, every scenario above stayed
  # green. One scenario each.
  # ==========================================================================

  # MUTATION: the `continue` that skips an artifact-less input in
  # BuildFetchInitContainers becomes `return nil`.
  #
  # Not every input a step declares has something to fetch. An input the web
  # could not locate, or one a preceding step chose not to produce, arrives
  # with no artifact behind it; it still gets its directory, there is simply
  # nothing to put in it, so the batch skips it and carries on with the rest.
  #
  # Abandoning the batch at that point is the quietest failure in this file.
  # The step keeps every mount it asked for, the pod is created, no error is
  # logged anywhere — and the inputs that DID have artifacts silently arrive
  # empty too, because one artifact-less sibling took the whole batch down
  # with it. The task then fails on a file it was handed, and nothing
  # distinguishes that from a producing step that emitted nothing.
  #
  # "A step's inputs are fetched before its command runs" above cannot see it:
  # that step has one input and it carries an artifact, so the skip is never
  # reached. The mixture is the whole scenario.
  Scenario: An input with nothing to fetch is skipped, not fatal to the rest
    Given a jetbridge worker with an artifact store
    And a task container "mixed-inputs-handle" built from image "docker:///busybox"
    And it works in "/tmp/build/workdir"
    And it takes an input at "/tmp/build/workdir/from-earlier" produced by an earlier step
    And it takes an input at "/tmp/build/workdir/no-artifact"
    When the container runs
    Then the pod fetches exactly 1 of the step's inputs
    And the pod fetches the input at "/tmp/build/workdir/from-earlier"
    And the pod does not fetch the input at "/tmp/build/workdir/no-artifact"

  # MUTATION: a get container is given no volume and no mount for its working
  # directory, so its pod carries zero step volumes instead of one.
  #
  # A get step has no outputs in its container spec. What it produces IS its
  # working directory, and the runtime knows that: RecordOutputs treats the
  # working directory of anything that is neither a task nor a check as an
  # output, files it under the daemon key "<handle>/dir", and points that key
  # at <store>/steps/<handle>/dir.
  #
  # So the working directory has to BE that node directory. Without a volume
  # for it, the resource writes its files into the container's own image
  # filesystem — which no daemon can read, and which is gone the moment the
  # pod ends. The get still exits 0 and the version is still recorded. Every
  # step downstream resolves "<handle>/dir", is handed a directory the daemon
  # creates empty on demand, and reads nothing from a resource that plainly
  # fetched something.
  #
  # Every scenario above describes a task or a check, and the working
  # directory of a task is not an output at all — so this rule is invisible
  # from any of them.
  Scenario: A get step's resource lands in the node directory the daemon will serve
    Given a jetbridge worker with an artifact store
    And a get container "get-store-handle" built from image "docker:///git-resource"
    And it works in "/tmp/resource"
    When the get container runs
    Then the step sees a volume mounted at "/tmp/resource"
    And the volume mounted at "/tmp/resource" is the node directory the daemon serves as "get-store-handle/dir"

  # MUTATION: an output volume is backed by steps/<handle>/output-N — the
  # volume's positional name — instead of steps/<handle>/<output name>.
  #
  # buildVolumeMounts numbers the volumes it creates: dir-0, input-1,
  # output-2. That number is the volume's NAME inside the pod spec, and it
  # means nothing outside it. The directory under the store has to carry the
  # name the PIPELINE gave the output, because that is the half RecordOutputs
  # registers.
  #
  # File it under the positional name instead and the two halves disagree by
  # one string. The step writes into <store>/steps/<handle>/output-1; the next
  # step resolves "<handle>/compiled" and is handed
  # <store>/steps/<handle>/compiled, which the daemon creates empty for it.
  # Both steps succeed. The consumer's input directory is empty.
  #
  # "An input sharing an output's path is filed under the output's name" above
  # pins the same rule for a volume SHARED with an input, where the name comes
  # from the overlap and a different line of code chooses it. This is the
  # ordinary output, where nothing forces the choice.
  Scenario: An output's directory on the node carries the name the pipeline gave it
    Given a jetbridge worker with an artifact store
    And a task container "compile-output-handle" built from image "docker:///busybox"
    And it works in "/tmp/build/workdir"
    When the container runs producing the output "compiled" at "/tmp/build/workdir/compiled"
    Then the step sees a volume mounted at "/tmp/build/workdir/compiled"
    And the volume mounted at "/tmp/build/workdir/compiled" is the node directory the daemon serves as "compile-output-handle/compiled"
