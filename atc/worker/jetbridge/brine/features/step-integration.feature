@RC-02 @RC-03 @RC-05 @CO-09 @LR-03 @PN-07 @PE-01 @CF-05
Feature: A step from end to end — its rows, its pod, its artifacts

  Everything in this file drives a real jetbridge worker against a real
  PostgreSQL database and a fake Kubernetes cluster, the way the ATC does when
  it places a step: it writes a container row, it asks Kubernetes for a pod
  named after the step rather than after a UUID, it hands the step its working
  set, it moves an artifact from one step to the next, and it speaks the
  resource protocol over stdin and stdout.

  Source: migrated from the jetbridge suite's integration files —
  behavioral_worker_test.go (RC/CO/LR), podname_integration_test.go (PN-07),
  artifact_integration_test.go, resource_test.go, secret_env_test.go and
  node_ip_resolver_test.go. Cases that are not scenarios here carry a
  disposition in steps/integration.go.

  Two things this file deliberately does not say, per coverage_matrix.md
  Addendum 2. It never asserts which command, pod, namespace or container name
  was handed to the executor: the executor here really runs the command, and
  the assertion is on what came back. And it never asserts that a volume is of
  a particular Go type — worker.feature's rule, kept.

  # ==========================================================================
  # Volumes a previous step left in the database
  # ==========================================================================

  # RC-02. A get step that finds its output already persisted has to be told
  # which worker holds it, because that is what the next step streams from.
  @RC-02
  Scenario: A persisted volume comes back naming the worker that holds it
    Given a jetbridge cluster in namespace "test-namespace"
    And a volume "cached-vol-1" recorded against this worker
    When the volume "cached-vol-1" is looked up twice
    Then the lookup finds it
    And the volume that came back is handle "cached-vol-1"
    And it names "k8s-worker-1" as the worker holding it
    And it carries the database row for handle "cached-vol-1" on worker "k8s-worker-1"

  # The two ginkgo Contexts this replaces ("with ArtifactLocator populated" and
  # "without ArtifactLocator") asserted the SAME thing — `vol.Source()` equals
  # the worker name — and they were right to: DaemonSetVolume.Source() returns
  # the worker name unconditionally, so the locator cannot change it. Stating
  # that as one scenario with a locator deliberately pointing somewhere else is
  # the only version of the pair that can fail.
  @RC-02
  Scenario: A locator pointing at another node does not change who holds the volume
    Given a jetbridge cluster in namespace "test-namespace"
    And a volume "cached-vol-1" recorded against this worker
    And the worker remembers the artifact "cached-vol-1" on node "node-42"
    When the volume "cached-vol-1" is looked up twice
    Then the lookup finds it
    And it names "k8s-worker-1" as the worker holding it

  # RC-03. The whole value of a cache hit is that the get step never happens.
  # A lookup that scheduled a pod would cost exactly what it was meant to save.
  @RC-03
  Scenario: A cache hit is served from the database without scheduling anything
    Given a jetbridge cluster in namespace "test-namespace"
    And a volume "cache-hit-vol" recorded against this worker
    When the volume "cache-hit-vol" is looked up twice
    Then the lookup finds it
    And looking it up scheduled nothing

  # RC-03's database half. The volume is only a cache hit for the NEXT build if
  # the association is written down; without these two rows the next build
  # re-downloads and this one's work is invisible.
  @RC-03
  Scenario: A cache hit records the association the next build will look for
    Given a jetbridge cluster in namespace "test-namespace"
    And a volume "cache-hit-vol" recorded against this worker
    When the volume "cache-hit-vol" is initialised as the resource cache for type "some-type" version "v1"
    Then the volume row points at the worker resource cache the caller was handed

  # LR-03. The ATC is restarted mid-build. Nothing is carried over in memory —
  # a new worker object over a new repository — and the step's output still has
  # to be findable, or every in-flight build loses its inputs.
  @LR-03
  Scenario: A restarted ATC finds a step's output again from persisted state
    Given a jetbridge cluster in namespace "test-namespace"
    And a volume "resilient-vol" recorded against this worker
    When a restarted ATC looks the volume "resilient-vol" up
    Then the lookup finds it
    And the volume that came back is handle "resilient-vol"

  # ==========================================================================
  # What a step is handed before anything is scheduled
  # ==========================================================================

  # The mounts are the step's working set, and they are handed back before any
  # pod exists — exec/build's repository registers them, so a missing one is an
  # input the step cannot read or an output the next step never sees.
  #
  # Note the fourth mount. The ginkgo case asserted `HaveLen(4)` and then named
  # only three paths, so the cache mount was counted and never checked. It is
  # "/workdir/my-cache": a relative cache path is resolved against the step's
  # working directory.
  Scenario: A step is handed a mount for its directory, its input, its output and its cache
    Given a jetbridge cluster in namespace "test-namespace"
    And a task step with handle "mount-test-handle" and no pipeline or job
    And the step works in "/workdir"
    And the step takes an input at "/workdir/input-a"
    And the step produces an output "out-b" at "/workdir/out-b"
    And the step caches "my-cache"
    When the step's container is created
    Then the step is handed 4 volume mounts
    And the step is handed a mount at "/workdir"
    And the step is handed a mount at "/workdir/input-a"
    And the step is handed a mount at "/workdir/out-b"
    And the step is handed a mount at "/workdir/my-cache"

  # ==========================================================================
  # Naming the pod after the step
  # ==========================================================================
  #
  # pod-naming.feature covers GeneratePodName as a pure function. These are the
  # integration half: the name the function produces is the name the cluster
  # actually holds, and everything that looks a pod up has to agree with it.

  Scenario: The pod an operator sees is named after the step, not after the handle
    Given a jetbridge cluster in namespace "test-namespace"
    And a "task" step in pipeline "my-pipeline" job "unit-test" build "42" named "run-tests" with handle "550e8400-e29b-41d4-a716-446655440000"
    When the step's container is created
    And the step's container runs
    Then the pod in the cluster is named to match "^my-pipeline-unit-test-b42-task-[a-f0-9]{8}$"
    And the pod is not named after the handle

  # `fly execute` has no pipeline and no job, so there is nothing to name the
  # pod after but the handle. Falling back is correct; inventing a name is not.
  Scenario: A step with no pipeline or job keeps its handle as its pod name
    Given a jetbridge cluster in namespace "test-namespace"
    And a task step with handle "550e8400-e29b-41d4-a716-446655440000" and no pipeline or job
    When the step's container is created
    And the step's container runs
    Then the pod in the cluster is named exactly "550e8400-e29b-41d4-a716-446655440000"

  # Exec mode creates a pause pod rather than baking the command in. The name
  # is generated the same way; a mismatch here would mean hijack and reap
  # disagree with the runtime about which pod is which.
  Scenario: A get step in exec mode is named the same way
    Given a jetbridge cluster in namespace "test-namespace"
    And the worker execs commands in pods
    And a "get" step in pipeline "ci" job "build" build "7" named "source-code" with handle "aabbccdd-1122-3344-5566-778899aabbcc"
    When the step's container is created
    And the step's container runs
    Then the pod in the cluster is named to match "^ci-build-b7-get-[a-f0-9]{8}$"

  # PE-12. A web that restarted mid-build re-attaches to a step it already has
  # a row for. The step had finished, so the recorded status is the answer and
  # the build resumes instead of running twice.
  Scenario: A restarted web resumes a step that had already finished
    Given a jetbridge cluster in namespace "test-namespace"
    And a "task" step in pipeline "my-pipeline" job "unit-test" build "42" named "" with handle "550e8400-e29b-41d4-a716-446655440000"
    And the step's container is created
    And the step finished with exit status "0" and left its pod behind
    When the web restarts and attaches to the step
    Then the step resumes reporting exit status 0

  # The failure message is the whole point. A worker that resolved the handle
  # straight to a pod name would name the UUID, and an operator would go
  # looking for a pod that never existed under that name.
  Scenario: Attaching to a step whose pod is gone names the pod, not the handle
    Given a jetbridge cluster in namespace "test-namespace"
    And a "task" step in pipeline "my-pipeline" job "unit-test" build "42" named "" with handle "550e8400-e29b-41d4-a716-446655440000"
    And the step's container is created
    When the web restarts and attaches to the step
    Then attaching fails naming the pod the step would have created
    And the failure does not name the handle

  # PN-07. The labels are what `kubectl get pods -l` and the reaper select on.
  @PN-07
  Scenario: A pod carries the step's pipeline, job, build, step and handle
    Given a jetbridge cluster in namespace "test-namespace"
    And a "task" step in pipeline "my-pipeline" job "unit-test" build "42" named "run-tests" with handle "550e8400-e29b-41d4-a716-446655440000"
    When the step's container is created
    And the step's container runs
    Then the pod is labelled "concourse.ci/pipeline" as "my-pipeline"
    And the pod is labelled "concourse.ci/job" as "unit-test"
    And the pod is labelled "concourse.ci/build" as "42"
    And the pod is labelled "concourse.ci/step" as "run-tests"
    And the pod is labelled "concourse.ci/handle" as "550e8400-e29b-41d4-a716-446655440000"

  # An empty label value is not the same as an absent one: Kubernetes accepts
  # it, and a selector for it then matches every pipeline-less pod at once.
  @PN-07
  Scenario: A pod carries no label for metadata the step does not have
    Given a jetbridge cluster in namespace "test-namespace"
    And a task step with handle "test-handle" and no pipeline or job
    When the step's container is created
    And the step's container runs
    Then the pod carries no "concourse.ci/pipeline" label
    And the pod carries no "concourse.ci/job" label
    And the pod is labelled "concourse.ci/handle" as "test-handle"

  # A label value over 63 characters is rejected by the API server, so the pod
  # is not created at all and the step fails for a reason that looks nothing
  # like "your pipeline name is long".
  @PN-07
  Scenario: A pipeline name too long to be a label value is cut to fit
    Given a jetbridge cluster in namespace "test-namespace"
    And a "task" step in pipeline "extremely-long-pipeline-name-that-exceeds-the-sixty-three-character-k8s-label-value-limit" job "j" build "1" named "" with handle "label-trunc-handle"
    When the step's container is created
    And the step's container runs
    Then every pod label value fits in a Kubernetes label

  # The volume a step is handed cannot read anything until it knows which pod
  # to read from, and the pod does not exist until the step runs. Binding it to
  # the handle instead of the pod name is the bug this catches: the read would
  # go to a pod that is not there.
  Scenario: A step's input volume starts unbound and ends up reading from its pod
    Given a jetbridge cluster in namespace "test-namespace"
    And the worker execs commands in pods
    And a "task" step in pipeline "my-pipeline" job "unit-test" build "42" named "" with handle "550e8400-e29b-41d4-a716-446655440000"
    And the step works in "/tmp/build/workdir"
    And the step takes an input at "/tmp/build/workdir/my-input"
    When the step's container is created
    And the step's container runs
    Then the mount at "/tmp/build/workdir/my-input" read from no pod before the step ran
    And the mount at "/tmp/build/workdir/my-input" reads from the pod the step created

  # ==========================================================================
  # Artifacts moving between steps
  # ==========================================================================

  # CO-09 plus the consumer's half: an artifact the previous step persisted is
  # looked up by handle, mounted where the task asked for it, and the task runs
  # and says so.
  @CO-09
  Scenario: An artifact a previous step persisted becomes this step's input
    Given a jetbridge cluster in namespace "ci-namespace"
    And the worker execs commands in pods
    And an artifact volume "upload" persisted for this team
    And a task step with handle "task-consume-artifact" and no pipeline or job
    And the step works in "/tmp/build/workdir"
    And the step takes the artifact "upload" as an input at "/tmp/build/workdir/my-input"
    When the step's container is created
    And the step's container runs the command "echo artifact data received"
    Then the step's pod mounts "/tmp/build/workdir/my-input"
    And the step's output is "artifact data received"
    And the step reports exit status 0
    And the step's container row is a created "task" container on worker "k8s-worker-1"

  # The chain a pipeline actually is: a get step's output feeds a task, the
  # task's output feeds a put, and the put speaks the resource protocol.
  Scenario: A task's output reaches the put step that publishes it
    Given a jetbridge cluster in namespace "ci-namespace"
    And the worker runs resource scripts that exit 0
    And an artifact volume "get-output" persisted for this team
    And a task step with handle "task-build-step" and no pipeline or job
    And the step works in "/tmp/build/workdir"
    And the step takes the artifact "get-output" as an input at "/tmp/build/workdir/repo"
    And the step produces an output "binary" at "/tmp/build/workdir/binary"
    And the step's container is created
    And the step's container runs the command "echo built"
    When next, a "put" step "put-upload-step" for resource type "s3"
    And the step takes an input at "/tmp/build/put/binary"
    And the step's container is created
    And the resource is asked for "ref=v1.0.0" into "/tmp/build/put"
    Then the resource answers "script=out dir=/tmp/build/put request=ref=v1.0.0"
    And the step reports exit status 0
    And the step's pod mounts "/tmp/build/put/binary"
    And the step's container row is a created "put" container on worker "k8s-worker-1"

  # The artifact key is what a daemon is asked for. If it moved between
  # lookups, the second step would ask for something no daemon has.
  Scenario: An artifact answers to the same key however often it is looked up
    Given a jetbridge cluster in namespace "ci-namespace"
    And a volume "artifact-key-vol" recorded against this worker
    When the volume "artifact-key-vol" is looked up twice
    Then the lookup finds it
    And both lookups named the same artifact key
    And the artifact key is the volume's handle

  # LR-03's artifact half: the row survives the wrap, so a looked-up artifact
  # can still be destroyed, re-initialised, or reported on.
  Scenario: A looked-up artifact still carries its database row
    Given a jetbridge cluster in namespace "ci-namespace"
    And an artifact volume "persisted" persisted for this team
    And a volume "artifact-row-vol" recorded against this worker
    When the volume "artifact-row-vol" is looked up twice
    Then the lookup finds it
    And it carries the database row for handle "artifact-row-vol" on worker "k8s-worker-1"

  # The reaper deletes the row, not the object the ATC is holding. The next
  # lookup has to miss, or a step would be handed a reference to bytes that
  # have been collected.
  Scenario: An artifact the reaper destroyed stops resolving
    Given a jetbridge cluster in namespace "ci-namespace"
    And an artifact volume "orphan" persisted for this team
    When the reaper destroys the artifact volume "orphan"
    Then the lookup finds nothing

  # Cross-team artifact visibility is a data leak, not a convenience.
  Scenario: An artifact is reachable by its own team and by nobody else
    Given a jetbridge cluster in namespace "ci-namespace"
    And an artifact volume "team-one-output" persisted for this team
    And an artifact volume "team-two-output" persisted for a second team
    When the artifact "team-one-output" is asked for by its own team and by the other team
    Then only its own team can reach it

  # ==========================================================================
  # The resource protocol
  # ==========================================================================
  #
  # A resource is a program: it reads a request on stdin, writes its answer on
  # stdout, and its exit code is the step's. The scripts behind these scenarios
  # are real — they echo the directory they were given and the request they
  # read — so the path, the argument, the stdin and the stdout are all proved
  # by the one answer, and none of them is asserted against a recorded call.

  Scenario: A get step's container row is a created get container
    Given a jetbridge cluster in namespace "test-namespace"
    And the worker runs resource scripts that exit 0
    And a "get" step "get-resource-handle" for resource type "git"
    And the step works in "/tmp/build/get"
    When the step's container is created
    Then the container row for "get-resource-handle" is a created "get" container on worker "k8s-worker-1"

  # PE-01. The pod runs a pause command, not the resource script: baking the
  # script in would run it once at pod start, with no stdin to read the request
  # from and nowhere to send the answer.
  @PE-01
  Scenario: A get step's request reaches its resource and the answer comes back
    Given a jetbridge cluster in namespace "test-namespace"
    And the worker runs resource scripts that exit 0
    And a "get" step "get-resource-handle" for resource type "git"
    And the step works in "/tmp/build/get"
    And the step's container is created
    When the resource is asked for "ref=abc123" into "/tmp/build/get"
    Then the resource answers "script=in dir=/tmp/build/get request=ref=abc123"
    And the step reports exit status 0
    And the step's pod runs the image "concourse/git-resource"
    And the pod's own command is not the resource script

  # A resource that fails has to fail the step with its own code. Swallowing it
  # would turn a broken git URL into a green build.
  Scenario: A resource script that fails hands its exit code back
    Given a jetbridge cluster in namespace "test-namespace"
    And the worker runs resource scripts that exit 1
    And a "get" step "get-resource-handle" for resource type "git"
    And the step works in "/tmp/build/get"
    And the step's container is created
    When the resource is asked for "ref=abc123" into "/tmp/build/get"
    Then the step reports exit status 1

  Scenario: A put step's inputs are mounted where its resource expects them
    Given a jetbridge cluster in namespace "test-namespace"
    And the worker runs resource scripts that exit 0
    And a "put" step "put-resource-handle" for resource type "git"
    And the step takes an input at "/tmp/build/put/my-repo"
    And the step's container is created
    When the resource is asked for "repo=my-repo" into "/tmp/build/put"
    Then the resource answers "script=out dir=/tmp/build/put request=repo=my-repo"
    And the step reports exit status 0
    And the step's pod mounts "/tmp/build/put/my-repo"

  Scenario: A check step's request reaches its resource and the versions come back
    Given a jetbridge cluster in namespace "test-namespace"
    And the worker runs resource scripts that exit 0
    And a "check" step "check-resource-handle" for resource type "git"
    And the step's container is created
    When the resource is checked with "uri=example.invalid"
    Then the resource answers "script=check dir= request=uri=example.invalid"
    And the step reports exit status 0

  # A check has no step-scoped process id, so the container handle is what
  # identifies the running process. Anything else and a re-exec after a web
  # restart would not find it.
  Scenario: A check step's process is identified by its container handle
    Given a jetbridge cluster in namespace "test-namespace"
    And the worker runs resource scripts that exit 0
    And a "check" step "check-resource-handle" for resource type "git"
    And the step's container is created
    When the resource is checked with "uri=example.invalid"
    Then the running process is identified as "check-resource-handle"

  # ==========================================================================
  # Secrets in a step's environment
  # ==========================================================================

  # CF-05. A credential that reaches the pod as a literal is a credential in
  # `kubectl get pod -o yaml`, in every audit log, and in the etcd snapshot.
  @CF-05
  Scenario: A secret env var reaches the pod as a reference, never as a literal
    Given a jetbridge cluster in namespace "test-namespace"
    And a task step with handle "secret-env-handle" and no pipeline or job
    And the step sets the environment "DB_PASS=s3cret"
    And the step sets the environment "STATIC=hello"
    And the step reads "DB_PASS" from the secret "db-password" key "value" in namespace "concourse-main"
    When the step's container is created
    And the step's container runs
    Then the pod reads "DB_PASS" from the secret "db-password" key "value"
    And the pod sets "STATIC" to the literal "hello"

  @CF-05
  Scenario: With no secrets declared every env var stays a literal
    Given a jetbridge cluster in namespace "test-namespace"
    And a task step with handle "no-secret-env-handle" and no pipeline or job
    And the step sets the environment "FOO=bar"
    When the step's container is created
    And the step's container runs
    Then the pod sets "FOO" to the literal "bar"

  # ==========================================================================
  # Resolving a node to an address
  # ==========================================================================
  #
  # The artifact daemon lives on a node, and a consumer reaches it by IP. This
  # is the only translation between the two.

  Scenario: A node resolves to its internal address, and keeps resolving to it
    Given a cluster whose node "node-1" has internal address "10.0.0.5" and external address "34.1.2.3"
    When a caller resolves "node-1" twice
    Then every answer is "10.0.0.5"

  Scenario: A node that is not in the cluster cannot be resolved
    Given a cluster with no nodes
    When a caller resolves "nonexistent" twice
    Then resolving fails

  # A node with only an external address is not reachable from inside the
  # cluster, so answering with the external one would send every artifact fetch
  # out through the internet gateway — or nowhere.
  Scenario: A node with no internal address is an error, not an external address
    Given a cluster whose node "node-1" has internal address "" and external address "34.1.2.3"
    When a caller resolves "node-1" twice
    Then resolving fails

  # The regression: a caller conflating a pod IP with a node name. The Nodes
  # API is keyed by name, so the lookup could only ever 404 with a misleading
  # `nodes "10.0.0.5" not found`. On a cluster with no nodes at all, the
  # sentinel is the ONLY thing that distinguishes the refusal from that 404.
  Scenario Outline: An IP passed as a node name is refused, not looked up — <case>
    Given a cluster with no nodes
    When a caller resolves "<name>" twice
    Then it is refused as an IP address rather than reported as a missing node

    Examples:
      | case            | name            |
      | private IPv4    | 10.0.0.5        |
      | loopback IPv4   | 127.0.0.1       |
      | CGNAT IPv4      | 100.68.228.107  |
      | loopback IPv6   | ::1             |
      | documentation   | 2001:db8::1     |

  # The container a step's command is exec'd into. Every PodExecutor double in
  # this package declared that parameter as `_`, so nothing observed it — and
  # resource_test.go covered it by inspecting the recorded call.
  #
  # This pod has only "main". An exec naming anything else is refused the way
  # the API server refuses it, so a runtime that targeted a sidecar would run
  # the resource script in the wrong image, against the wrong filesystem.
  Scenario: A step's command is exec'd into the pod's main container
    When a resource step runs on a pod whose only container is "main"
    Then the step ran, so it was exec'd into the container that exists
