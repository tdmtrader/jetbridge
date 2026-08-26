@RC-01
Feature: A Kubernetes worker serving containers, volumes and artifacts

  This is the object the ATC holds when it decides where to put a step. It has
  to answer to a name, record a container before there is a pod, hand back the
  same container when a step is retried, let an operator intercept a step that
  is still running, persist artifact volumes, and hand downstream steps a
  reference to an output that keeps working after the producing pod is gone.

  Source: migrated whole from worker_test.go (37 cases), which carried no
  requirement identifiers except RC-01 ("SkipResourceCache"), attributed by
  coverage_matrix.md section 6.

  Two things this file deliberately does NOT say, per coverage_matrix.md
  Addendum 2. It never asserts which pod name was handed to the executor, and
  it never asserts that a volume is of a particular Go type. Both are replaced
  by the effect: a read that reaches the artifact daemon, or a read that dies
  with the producer pod. The executor here is a real one that runs commands;
  the daemon is a real HTTP server.

  # --------------------------------------------------------------------------
  # Identity
  # --------------------------------------------------------------------------

  # RC-01. The name is the placement key — every container and volume row is
  # written against it — and caching is on, so a get step is allowed to skip
  # a download the daemon already holds.
  @RC-01
  Scenario: The worker presents the identity the database gave it
    Given a Kubernetes worker "k8s-worker-1" with a database behind it
    Then the worker answers to the name "k8s-worker-1"
    And the worker takes part in resource caching

  # --------------------------------------------------------------------------
  # Containers
  # --------------------------------------------------------------------------

  # The row is written first and the pod only when the step actually runs,
  # because the command is not known until Run. A consumer sees a container it
  # can hold on to before anything is scheduled.
  Scenario: A container is recorded before any pod exists
    Given a Kubernetes worker "k8s-worker-1" with a database behind it
    When a task container "test-handle" is requested for step "my-task"
    And the container is run
    Then the container "test-handle" is recorded as a created task container for step "my-task"
    And no pod existed until the container ran
    And the pod "test-handle" is now on the cluster

  # A row stuck in `creating` is invisible to the collector and never
  # reclaimed. Marking it failed is what lets the cluster recover.
  Scenario: A container that cannot be recorded is left for the collector
    Given a Kubernetes worker "k8s-worker-1" with a database behind it
    And the database cannot transition containers to created
    When a task container "test-handle" is requested for step "my-task"
    Then the container request fails saying "db connection lost"
    And the container "test-handle" is left in state "failed"

  # A step that is retried, or a web that restarted mid-step, asks for the same
  # container again. A second row would orphan the first one's pod.
  Scenario: Asking again for a container returns the one already recorded
    Given a Kubernetes worker "k8s-worker-1" with a database behind it
    And a task container "existing-handle" has already been created for step "my-task"
    When a task container "existing-handle" is requested for step "my-task"
    Then the container request succeeds
    And it returns the container already recorded as "existing-handle"
    And exactly 1 container row carries the handle "existing-handle"

  # `fly intercept` resolves a handle to a container and then asks it for its
  # database row, which is what the hijack handler records the hijack against.
  #
  # NOTE, and it contradicts the ginkgo Context name this replaces ("when the
  # Pod exists"): lookup consults the DATABASE ONLY. The pod played no part in
  # the original test's outcome, so no pod appears here.
  Scenario: A recorded container is found and carries its database row
    Given a Kubernetes worker "k8s-worker-1" with a database behind it
    And a task container "lookup-handle" has already been created for step "lookup-task"
    When the container "lookup-handle" is looked up
    Then the container is found
    And it carries the database row for handle "lookup-handle"

  # A pod nobody has a row for is not a container. Reporting it as one would
  # let a caller hijack a pod Concourse cannot account for.
  Scenario: A pod with no container row is not a container
    Given a Kubernetes worker "k8s-worker-1" with a database behind it
    And the cluster is running a pod "orphan-pod" that no container row refers to
    When the container "orphan-pod" is looked up
    Then the container is not found

  Scenario: A handle that was never issued is not found
    Given a Kubernetes worker "k8s-worker-1" with a database behind it
    When the container "nonexistent" is looked up
    Then the container is not found

  # --------------------------------------------------------------------------
  # Intercepting a step
  # --------------------------------------------------------------------------
  #
  # `fly intercept -j my-pipeline/unit-test`. The database handle is an opaque
  # UUID; the pod the step created is named from its metadata. These three
  # scenarios are the whole reason that distinction matters.

  Scenario: Intercepting a step attaches to the pod the step created
    Given a Kubernetes worker "k8s-worker-1" with a database behind it
    And the worker can exec into pods
    And a task step of build 42 of "my-pipeline/unit-test" was recorded under the opaque handle "550e8400-e29b-41d4-a716-446655440000"
    And the step created the pod "my-pipeline-unit-test-b42-task-550e8400"
    When the operator intercepts the container "550e8400-e29b-41d4-a716-446655440000" and runs "echo interactive"
    Then the interception succeeds
    And the operator sees "interactive"
    And the cluster still holds only the pod "my-pipeline-unit-test-b42-task-550e8400"

  # The decoy is the point. It is named after the raw handle, so a worker that
  # resolved the handle straight to a pod name would find it, attach, and
  # report success. Only a worker that generated the pod name from the step's
  # metadata can fail here — and failing is the correct answer, because
  # fabricating a pod from an empty ContainerSpec produces a misleading
  # "empty image for resource type (unknown)".
  Scenario: An interception whose pod is gone says so rather than fabricating one
    Given a Kubernetes worker "k8s-worker-1" with a database behind it
    And the worker can exec into pods
    And a task step of build 42 of "my-pipeline/unit-test" was recorded under the opaque handle "550e8400-e29b-41d4-a716-446655440000"
    And the step created the pod "my-pipeline-unit-test-b42-task-550e8400"
    And that pod has since been reaped
    And a decoy pod named after the handle is running
    When the operator intercepts the container "550e8400-e29b-41d4-a716-446655440000" and runs "echo interactive"
    Then the interception fails saying "has no pod to intercept"
    And the cluster still holds only the pod "550e8400-e29b-41d4-a716-446655440000"

  # Replacing a completed pod would destroy the exit-status annotation a
  # restarted web reads to resume the step, turning a hijack into data loss.
  Scenario: An interception does not replace a pod that already exited
    Given a Kubernetes worker "k8s-worker-1" with a database behind it
    And the worker can exec into pods
    And a task step of build 42 of "my-pipeline/unit-test" was recorded under the opaque handle "550e8400-e29b-41d4-a716-446655440000"
    And the step created the pod "my-pipeline-unit-test-b42-task-550e8400"
    And that pod has since finished with exit status "0"
    When the operator intercepts the container "550e8400-e29b-41d4-a716-446655440000" and runs "echo interactive"
    Then the interception fails saying "already exited"
    And the pod "my-pipeline-unit-test-b42-task-550e8400" still records exit status "0"

  # --------------------------------------------------------------------------
  # Artifact volumes
  # --------------------------------------------------------------------------

  Scenario: An artifact volume is persisted with the artifact it carries
    Given a Kubernetes worker "k8s-worker-1" with a database behind it
    When the worker creates a volume for an artifact
    Then creating the volume succeeds
    And the volume is recorded for this worker and team in state "created" as type "artifact"
    And the volume row points at the artifact the caller was handed
    And the handle the caller got is the handle the database persisted

  # The ginkgo Context this replaces was called "when the artifact store is
  # configured" and configured nothing — its BeforeEach rebuilt the worker with
  # the same daemon-less config, so it asserted exactly what the sibling case
  # ("always returns a DaemonSetVolume") already asserted. Here the daemon is
  # really configured, and the claim is the one that matters to a consumer:
  # the volume it got back reads over HTTP from the daemon.
  Scenario: An artifact volume reads from the artifact daemon
    Given a Kubernetes worker "k8s-worker-1" with a database behind it
    And the cluster runs an artifact daemon holding every step output
    When the worker creates a volume for an artifact
    Then creating the volume succeeds
    And reading the volume yields "step-output-bytes"

  # The key is where the artifact lives in the store. A constant one puts
  # every artifact on the worker in the same place: the second overwrites the
  # first, and a step asking for its own output is handed another step's.
  #
  # step-integration.feature already checks a key against its handle, but on
  # the LookupVolume path. This is the creation path, and the mutation that
  # pins the key to a constant does not reach the other one.
  Scenario: Two artifacts do not share a storage key
    Given a Kubernetes worker "k8s-worker-1" with a database behind it
    When the worker creates two volumes for artifacts
    Then each artifact is stored under its own key
    And each artifact's key is its own volume handle

  Scenario: Creating an artifact volume without a volume repository is refused
    Given a Kubernetes worker "k8s-worker-1" with a database behind it
    And the worker has no volume repository configured
    When the worker creates a volume for an artifact
    Then creating the volume fails saying "volume repository not configured"

  Scenario: Creating an artifact volume reports a lost database
    Given a Kubernetes worker "k8s-worker-1" with a database behind it
    And the worker's volume repository has lost its database connection
    When the worker creates a volume for an artifact
    Then creating the volume fails saying "closed"

  # The row is left in `creating`, not deleted and not created. That is what
  # the volume collector expects to find, and it is the observable difference
  # between "half-written" and "never happened".
  Scenario: A volume that cannot be transitioned is left creating
    Given a Kubernetes worker "k8s-worker-1" with a database behind it
    And the volume repository cannot transition volumes to created
    When the worker creates a volume for an artifact
    Then creating the volume fails saying "transition error"
    And a volume for this worker is left in state "creating"

  Scenario: A volume whose artifact cannot be initialised leaves no artifact behind
    Given a Kubernetes worker "k8s-worker-1" with a database behind it
    And the volume repository cannot initialise artifacts
    When the worker creates a volume for an artifact
    Then creating the volume fails saying "artifact init error"
    And no artifact is recorded

  # --------------------------------------------------------------------------
  # Looking a volume up
  # --------------------------------------------------------------------------

  Scenario: A volume in the database is found by its handle
    Given a Kubernetes worker "k8s-worker-1" with a database behind it
    And a volume "vol-handle-1" exists on this worker
    When the volume "vol-handle-1" is looked up
    Then the volume is found
    And the volume's handle is "vol-handle-1"

  # A prefix is a different handle. Matching one loosely would hand a step
  # somebody else's artifact.
  Scenario Outline: A handle the database does not hold is not found — <case>
    Given a Kubernetes worker "k8s-worker-1" with a database behind it
    And a volume "vol-handle-1" exists on this worker
    When the volume "<handle>" is looked up
    Then the volume is not found

    Examples:
      | case                    | handle      |
      | a prefix of a real one  | vol-handle  |
      | nothing like it         | nonexistent |

  Scenario: Looking a volume up reports a lost database
    Given a Kubernetes worker "k8s-worker-1" with a database behind it
    And the worker's volume repository has lost its database connection
    When the volume "vol-handle-1" is looked up
    Then looking up the volume fails saying "closed"

  # Worth flagging rather than celebrating: an unconfigured worker reports
  # "not found" for every handle, so a misconfiguration is indistinguishable
  # from an absent volume. The code does this deliberately; the scenario
  # records it so a change of mind is visible.
  Scenario: A worker with no volume repository reports every volume missing
    Given a Kubernetes worker "k8s-worker-1" with a database behind it
    And the worker has no volume repository configured
    When the volume "vol-handle-1" is looked up
    Then the volume is not found

  # --------------------------------------------------------------------------
  # Resource caches already on a daemon
  # --------------------------------------------------------------------------

  Scenario: A resource cache a daemon already holds is found and readable
    Given a Kubernetes worker "k8s-worker-1" with a database behind it
    And the cluster runs an artifact daemon holding the resource cache 42
    When a get step looks for the resource cache 42
    Then the cache is found
    And the volume's handle is "rc-42"
    And it reports "k8s-worker-1" as its source
    And reading the volume yields "cached-tar-data"

  # Regression. FindDaemonResourceCache used to write the daemon POD IP into
  # the ArtifactLocator's NodeName field. The next step to wrap the same key —
  # `worker.ArtifactFromVolume(volume)` in get_step.go — read that IP back and
  # asked the Nodes API to resolve it, failing with `nodes "<IP>" not found`.
  # Wrapping and then reading is the whole test.
  Scenario: A cache hit survives being wrapped by the next step
    Given a Kubernetes worker "k8s-worker-1" with a database behind it
    And the cluster runs an artifact daemon holding the resource cache 42
    When a get step looks for the resource cache 42
    And a downstream step turns it into an artifact
    Then the artifact's handle is "rc-42"
    And reading the artifact yields "cached-tar-data"

  # After a node roll the locator still names the old node. Trusting it would
  # report a hit and then fail the read, which is worse than re-downloading.
  Scenario: A stale locator entry does not become a phantom cache hit
    Given a Kubernetes worker "k8s-worker-1" with a database behind it
    And the cluster runs an artifact daemon holding nothing
    And the worker still remembers the resource cache 42 on a node that has been rolled away
    When a get step looks for the resource cache 42
    Then the cache is not found

  # --------------------------------------------------------------------------
  # Handing a step's output downstream
  # --------------------------------------------------------------------------

  # The bug this exists to fix: without the wrap, a downstream read execs into
  # the producing pod and dies with `exec stream: pods "..." not found` once
  # the reaper has taken it. So the producing pod is gone in every row below,
  # and the read still has to work.
  #
  # The third row is the key check as well as a handle check: the daemon here
  # serves exactly one key and 404s everything else, so a read only succeeds if
  # the artifact key really is the volume's handle.
  Scenario Outline: A step's output outlives the pod that produced it — <case>
    Given a Kubernetes worker "k8s-worker-1" with a database behind it
    And the producing pod has been reaped
    And the cluster runs an artifact daemon holding the step output "<handle>"
    When a <kind> step output volume "<handle>" is turned into an artifact
    Then the artifact's handle is "<handle>"
    And reading the artifact yields "step-output-bytes"

    Examples:
      | case                       | kind    | handle            |
      | a container mount          | mounted | artifact-handle-1 |
      | a placeholder volume       | stub    | artifact-handle-2 |
      | an arbitrary handle as key | mounted | arbitrary-handle  |

  # The legacy exec-only fallback, stated as its consequence rather than as
  # "the same object came back". This is the failure mode the wrap removes.
  Scenario: Without a daemon a step's output dies with its pod
    Given a Kubernetes worker "k8s-worker-1" with a database behind it
    And the producing pod has been reaped
    And the worker has no artifact daemon configured
    When a mounted step output volume "legacy-handle" is turned into an artifact
    Then the artifact's handle is "legacy-handle"
    And reading the artifact fails saying "the producer pod has been reaped"

  # A step with nothing to publish must get nothing back, not a wrapper around
  # nothing — get_step.go calls ArtifactFromVolume unconditionally and would
  # register a reference that panics on read.
  Scenario: A step with no output volume is handed no artifact
    Given a Kubernetes worker "k8s-worker-1" with a database behind it
    And the cluster runs an artifact daemon holding the step output "unused"
    When a step with no output volume asks for an artifact
    Then no artifact is handed back

  Scenario: A step with no output volume is handed no artifact without a daemon either
    Given a Kubernetes worker "k8s-worker-1" with a database behind it
    And the worker has no artifact daemon configured
    When a step with no output volume asks for an artifact
    Then no artifact is handed back
