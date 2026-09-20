@PE-01 @PE-02 @PE-04 @CO-04 @CO-05 @VT-02 @VT-03
Feature: Running a step, and what the caller gets back

  Two things decide what a step's Run does: whether the worker has an exec
  transport, and what the caller declared it needed. With no transport the pod
  IS the step. With one, the pod is a placeholder the step is exec'd into and
  which has to outlive it, because the step's outputs are still inside it.
  Either way the caller was handed a set of volumes before anything was
  scheduled, and those volumes are the only route back to the step's results.

  Source: k8s_runtime_behavioral_spec_20260331 (PE-01, PE-02, PE-04) and
  jetbridge_storage_behavioral_spec_20260330 (CO-04, CO-05, VT-02, VT-03).
  Migrated from container_test.go.

  # --------------------------------------------------------------------------
  # Direct mode — the pod is the step
  # --------------------------------------------------------------------------

  # Direct compatibility construction is distinct from production exec mode.
  # Shared rows preserve pod fields, attachment identity and the creation counter.
  # This is real API construction, not kubelet execution of the command.
  @PE-02 @PE-04
  Scenario Outline: Without an exec transport the pod runs the command itself
    Given a Kubernetes worker "run-worker" with a database behind it
    And the worker prepares task "run-test-handle" from image "docker:///busybox"
    And it works in "/workdir"
    And the container environment sets "FOO=bar"
    And the container environment sets "BAZ=qux"
    When the step runs command "<command>" with arguments "<arguments>" directly
    Then the pod is named "run-test-handle"
    And the pod runs these containers in order
      | name | image |
      | main | busybox |
    And the container "main" runs command "<command>" with arguments "<arguments>"
    And the container "main" works in "/workdir"
    And the container "main" has environment "FOO" set to "bar"
    And the container "main" has environment "BAZ" set to "qux"
    And the pod is never restarted
    And the step uses the "unprivileged" security policy
    And the step has an identity a restarted web could attach to

    Examples:
      | command          | arguments     |
      | /bin/sh          | -c,echo hello |
      | /opt/resource/in | /tmp/build/get |

  # A step that declares no workspace must not be given one. An unasked-for
  # emptyDir at a path the image already populates silently shadows it, and the
  # step fails looking for files that are right there in the image.
  @CO-04
  Scenario: A step that declares no working directory is given no workspace
    Given a Kubernetes worker "run-worker" with a database behind it
    And the worker prepares task "no-dir-handle" from image "docker:///busybox"
    And it declares no working directory
    When the container runs
    Then the pod has 0 volumes
    And the step has nothing mounted at all

  # DISPOSITION — "creates a Pod with an emptyDir volume for spec.Dir when Dir
  # is set" is now covered by container-pod.feature's "A step's working
  # directory is an ephemeral volume". Its volume/mount counts, destination
  # and emptyDir checks are mutation-paired with the original Go test; see
  # V5-MIGRATION.md. Not duplicated here.
  #
  # The direct-mode scenario above now observes the full pod contract together.

  # --------------------------------------------------------------------------
  # Exec mode — the pod is a placeholder the step outlives
  # --------------------------------------------------------------------------

  # Successful pause-pod and retention assertions are shared with the first
  # startup/log scenario in live/task-command.feature. Failed tasks stay distinct.

  # A pod deleted the moment the command exits cannot have its outputs streamed
  # out and cannot be intercepted. Cleanup is the collector's job, and it waits
  # for the build.
  # Failed-task retention moved to live/task-command.feature.


  # --------------------------------------------------------------------------
  # What FindOrCreateContainer hands back before anything is scheduled
  # --------------------------------------------------------------------------

  # CO-04/CO-05. These mounts are how the build step finds its own inputs,
  # outputs and caches; a missing one means the step's artifact is never
  # registered and the next step reads nothing. Two sharing a handle is worse —
  # the registry then points two paths at one blob.
  @CO-04 @CO-05
  Scenario: Every path the step declared comes back as its own volume
    Given a Kubernetes worker "mount-worker" with a database behind it
    And the worker prepares task "vm-handle" from image "docker:///busybox"
    And it works in "/tmp/build/workdir"
    And it takes an input at "/tmp/build/workdir/my-input"
    And it takes an input at "/tmp/build/workdir/other-input"
    And it produces output "result" at "/tmp/build/workdir/result"
    And it produces output "metadata" at "/tmp/build/workdir/metadata"
    And it caches "/tmp/build/workdir/.cache"
    When the container is created but not yet run
    Then the caller is handed these deferred volumes
      | mount path                       |
      | /tmp/build/workdir                |
      | /tmp/build/workdir/my-input       |
      | /tmp/build/workdir/other-input    |
      | /tmp/build/workdir/result         |
      | /tmp/build/workdir/metadata       |
      | /tmp/build/workdir/.cache         |
    When the step then runs
    Then every volume the caller was handed now names the pod "vm-handle"

  # The same production-returned volumes are checked before and after Run.
  # Executor wiring is a precondition, not a claim that unbound volumes can
  # already move bytes; live streaming scenarios exercise actual I/O.

  # The output round trip is consolidated into volume-streaming.feature's
  # artifact-handoff outline: a direct gzip read before collection, followed
  # by the daemon-backed producer-to-consumer transfer after pod deletion.
  # JB-container-041/-042 retain their named Go tests for exact command,
  # metadata and pod-retention contracts; see DISPOSITION-jetbridge.md.

  # DISPOSITION — "Input streaming is a no-op (handled by init containers)"
  # has no seam-level equivalent and is not migrated. It asserts
  # `len(execExecutor.execCalls) == 1`, i.e. that the runtime did NOT do
  # something; the only observation is the double's call count, which is
  # Addendum 2's "routing" class. Worse, the block's own title is not what it
  # tests: this container is built with no storage backend, so
  # buildArtifactInitContainers returns nil and there are no init containers in
  # the pod at all. The init-container staging the title names is asserted by
  # behavioral_permutations_test.go's TestBuildArtifactInitContainers_* family
  # and by daemonset_integration_test.go, both of which configure a backend.

  # --------------------------------------------------------------------------
  # fly hijack
  # --------------------------------------------------------------------------

  # The exit code of an intercepted command is what lands in the operator's own
  # shell. Swallowing it makes a failed hijack look clean, which is how a
  # broken debugging session gets mistaken for a working one.
  # Scenario moved to live/interception.feature: real pod execution.

  # DISPOSITION — "execs into the existing pod without creating a new one" is
  # already live/interception.feature's "Intercepting a step attaches to the pod the step
  # created", which asserts the command's output reaches the operator AND that
  # the cluster still holds only the one pod. The ginkgo case additionally
  # asserted `execCalls[0].podName`; that is the routing class, and the
  # single-pod assertion covers its effect.

  # Real TTY/pipe behavior is covered in live/terminal.feature. The focused
  # Go attribute assertions remain; this migration does not retire them.

  # --------------------------------------------------------------------------
  # What the operator's counters say a Run did
  # --------------------------------------------------------------------------

  # These two counters are what a container-churn dashboard is built on. A
  # failure counted as a success hides a cluster that has stopped admitting
  # pods behind a healthy-looking creation rate.
  # Direct mode preserves the existing fallback contract. Exec mode uses
  # the production SPDY executor; no process is waited on or executed here.
  Scenario Outline: Pod creation counters describe the API outcome
    Given a Kubernetes worker "metric-worker" with a database behind it
    When a step's pod creation is "<outcome>" using "<mode>" mode
    Then the operator sees <created> container created and <failed> failed

    Examples:
      | outcome  | mode   | created | failed |
      | accepted | direct | 1       | 0      |
      | accepted | exec   | 1       | 0      |
      | refused  | direct | 0       | 1      |
      | refused  | exec   | 0       | 1      |

  # --------------------------------------------------------------------------
  # When the database, not Kubernetes, is what went wrong
  # --------------------------------------------------------------------------

  # The lookup is the first thing the request does. A lost connection has to
  # surface as a lookup failure; treating it as "not found" would insert a
  # second row for a container that already exists and orphan the first one's
  # pod.
  Scenario: A lost database connection is reported as a lookup failure
    Given a Kubernetes worker "k8s-worker-1" with a database behind it
    And the worker has lost its database connection
    When a task container "db-fail-handle" is requested for step "my-task"
    Then the container request fails saying "find container in db"

  # Handles are globally unique. One already taken on another worker misses
  # this worker's lookup and then collides on insert. Reporting anything other
  # than a create failure would schedule the step against a row it does not
  # own.
  Scenario: A handle another worker already holds cannot be claimed
    Given a Kubernetes worker "k8s-worker-1" with a database behind it
    And another worker already holds container "dup-handle"
    When a task container "dup-handle" is requested for step "my-task"
    Then the container request fails saying "create container in db"

  # A row left in `creating` by a web that died mid-request is invisible to the
  # collector and never reclaimed. The next request has to adopt it: a second
  # row would orphan the first one's pod, and leaving it alone leaks it
  # forever.
  Scenario: A container a crash left half-created is adopted, not duplicated
    Given a Kubernetes worker "k8s-worker-1" with a database behind it
    And a task container "stale-creating-handle" was left half-created by a crash
    When a task container "stale-creating-handle" is requested for step "my-task"
    Then the container request succeeds
    And the container "stale-creating-handle" is left in state "created"
    And exactly 1 container row carries the handle "stale-creating-handle"

  # And when adopting it fails too, it must be marked failed rather than left
  # in `creating` — `failed` is the state the collector can actually see.
  Scenario: A half-created container that still cannot be completed is left for the collector
    Given a Kubernetes worker "k8s-worker-1" with a database behind it
    And a task container "stale-fail-handle" was left half-created by a crash
    And the database cannot transition containers to created
    When a task container "stale-fail-handle" is requested for step "my-task"
    Then the container request fails saying "mark container as created"
    And the container "stale-fail-handle" is left in state "failed"

  # DISPOSITION — "marks the container as failed when Created() fails" (the
  # non-stale path) is already worker.feature's "A container that cannot be
  # recorded is left for the collector", same fault, same two assertions.
