@GC-01 @GC-02 @GC-03 @GC-04 @GC-05 @GC-06 @GC-07
Feature: Reclaiming pods and the rows that track them

  Every finished step leaves a pod and a database row behind. The reaper's job
  is to reclaim both without ever reclaiming one that is still needed — a pod
  deleted too early loses a running build, and a row deleted too early makes a
  live container invisible to the scheduler.

  Source: k8s_runtime_behavioral_spec_20260331 — GC-01 to GC-07. Migrated
  from reaper_test.go — NOT whole. Five of its nineteen cases remain uncovered:
  the pod that vanishes between the list and the delete, the
  GetAllStartedBuilds error branch (distinct from the nil-lookup branch), the
  retained-pod-through-destroying path, and two production branches no
  scenario here can reach, because every pod these scenarios build has
  handle == pod.Name.
  From reaper_test.go, which carried no requirement identifiers.

  # GC-04. A container whose pod is present is alive; one whose pod is gone is
  # marked missing so the scheduler can eventually give up on it.
  @GC-04
  Scenario: Containers are alive if their pods are, and missing if they are not
    Given a Kubernetes worker whose reaper is running
    And a container "pod-aaa" exists on this worker
    And a container "pod-bbb" exists on this worker
    And a container "unreported-decoy" exists on this worker
    And a pod "pod-aaa" is running for it
    And a pod "pod-bbb" is running for it
    When the reaper runs
    Then the reaper completes without error
    And the container "pod-aaa" is not marked as missing
    And the container "pod-bbb" is not marked as missing
    And the container "unreported-decoy" is marked as missing

  # GC-05. The row is only forgotten once nothing reports its pod — and only
  # for this worker, because another worker's rows are not ours to delete.
  @GC-05
  Scenario: A destroyed container is forgotten, but only on this worker
    Given a Kubernetes worker whose reaper is running
    And a container "pod-ccc" exists on this worker
    And a container "pod-ddd" on this worker is being destroyed
    And a container "other-worker-pod" on another worker is being destroyed
    And a pod "pod-ccc" is running for it
    When the reaper runs
    Then the container "pod-ddd" is no longer tracked
    And the container "pod-ccc" is still tracked as "created"
    And the container "other-worker-pod" is still tracked as "destroying"

  # GC-07
  @GC-07
  Scenario: A pod belonging to a destroyed container is deleted
    Given a Kubernetes worker whose reaper is running
    And a container "doomed" on this worker is being destroyed
    And a pod "doomed" is running for it
    When the reaper runs
    Then the pod "doomed" is gone
    # The row is NOT dropped in the same sweep — it stays destroying and is
    # reclaimed on a later pass, once no pod reports it. Asserting otherwise
    # was my invention; reaper_test.go asserts exactly this.
    And the container "doomed" is still tracked as "destroying"

  @GC-07
  Scenario: A container whose pod already vanished is still forgotten
    Given a Kubernetes worker whose reaper is running
    And a container "ghost" on this worker is being destroyed
    When the reaper runs
    Then the reaper completes without error
    And the container "ghost" is no longer tracked

  Scenario: A live container is left alone
    Given a Kubernetes worker whose reaper is running
    And a container "keeper" exists on this worker
    And a pod "keeper" is running for it
    When the reaper runs
    Then the pod "keeper" is still there
    And the container "keeper" is still tracked as "created"

  # GC-02, and the drift the coverage matrix recorded. The spec says a pod
  # carrying the exit-status annotation is deleted immediately. It is not: a
  # finished step's pod is RETAINED while its build is still running, so a web
  # restart can re-attach and resume rather than re-running the step in a
  # dirty workspace. The tests are right and the specification is stale.
  @GC-02
  Scenario Outline: A finished step's pod is kept only while its build needs it — <case>
    Given a Kubernetes worker whose reaper is running
    And a finished step left a pod "finished-<slug>" behind, for a build that is "<build>"
    When the reaper runs
    Then the pod "finished-<slug>" is <fate>

    Examples:
      | case                  | slug     | build         | fate       |
      | build still running   | running  | still running | still there |
      | build finished        | done     | finished      | gone        |

  # A check has no build to resume, so there is nothing to wait for.
  @GC-02
  Scenario: A finished check's pod is reclaimed at once
    Given a Kubernetes worker whose reaper is running
    And a finished check left a pod "finished-check" behind, with no build to resume
    When the reaper runs
    Then the pod "finished-check" is gone

  # Retention is the safe default: a reaper that cannot tell which builds are
  # running must not guess, because guessing wrong kills a live build.
  @GC-02
  Scenario: Without a way to check builds, finished pods are kept
    Given a Kubernetes worker whose reaper is running
    And the reaper cannot tell which builds are running
    And a finished step left a pod "finished-unknown" behind, for a build that is "finished"
    When the reaper runs
    Then the pod "finished-unknown" is still there

  # The other retention path. A lookup that EXISTS but cannot answer is a
  # different branch from no lookup at all, and only the second had a
  # scenario. Reaping on a failed lookup deletes the pod of a build that is
  # still running, which loses the build.
  @GC-02
  Scenario: A build lookup that cannot answer still keeps finished pods
    Given a Kubernetes worker whose reaper is running
    And the reaper's view of running builds has been lost
    And a finished step left a pod "finished-unreadable" behind, for a build that is "finished"
    When the reaper runs
    Then the pod "finished-unreadable" is still there

  # GC-03. The pod name is readable; the handle is the database key. The label
  # is what joins them, and losing that join would make the reaper delete by
  # the wrong name.
  @GC-03
  Scenario: A pod is matched to its container by label, not by name
    Given a Kubernetes worker whose reaper is running
    And a container "handle-aaa" exists on this worker
    And a pod "my-pipeline-my-job-b1-task-handle-a" is running, labelled with the handle "handle-aaa"
    When the reaper runs
    Then the container "handle-aaa" is not marked as missing
    And the pod "my-pipeline-my-job-b1-task-handle-a" is still there

  # Running twice must not turn a success into a failure — the reaper runs on
  # a timer and will meet its own leftovers.
  Scenario: Reaping is safe to repeat
    Given a Kubernetes worker whose reaper is running
    And a container "twice" on this worker is being destroyed
    And a pod "twice" is running for it
    When the reaper runs
    And the reaper runs again
    Then the reaper completes without error
    And the pod "twice" is gone

  # GC-01. With no pods at all the reaper still has to report an empty set —
  # reporting nothing at all would leave every container's missing_since
  # untouched and the scheduler placing work on a worker with no pods.
  @GC-01 @GC-04
  Scenario: An empty cluster is reported as empty, not skipped
    Given a Kubernetes worker whose reaper is running
    And a container "orphan-row" exists on this worker
    When the reaper runs
    Then the reaper completes without error
    And the container "orphan-row" is marked as missing

  # GC-06. A pod with no container row is an orphan — it survived a database
  # it is no longer recorded in, and nothing else will ever clean it up.
  @GC-06
  Scenario: A pod nothing in the database knows about is reclaimed
    Given a Kubernetes worker whose reaper is running
    And a pod "unknown-pod" is running for it
    When the reaper runs
    And the reaper runs again
    Then the pod "unknown-pod" is gone

  # A container created but never marked for destruction must survive every
  # sweep; reaping it would kill a step that is still running.
  Scenario: A newly created container is never reaped
    Given a Kubernetes worker whose reaper is running
    And a container "brand-new" exists on this worker
    And a pod "brand-new" is running for it
    When the reaper runs
    And the reaper runs again
    Then the pod "brand-new" is still there
    And the container "brand-new" is still tracked as "created"

  # Retention has two halves. The pod survives, and so must the row that
  # tracks it: a retained pod is still in the cluster, so the same sweep has
  # to report it as active. Otherwise the container is marked missing, the
  # next sweep destroys the row, and the resumed build finds an exit-status
  # annotation with nothing to attach it to.
  @GC-02
  Scenario: A pod kept for a running build keeps its container row too
    Given a Kubernetes worker whose reaper is running
    And a finished step left a pod "kept-for-build" behind, for a build that is "still running"
    When the reaper runs
    And the reaper runs again
    Then the pod "kept-for-build" is still there
    And the container behind the pod "kept-for-build" is not marked as missing

  # GC-03's other half. The pod name is readable and the database key is the
  # handle, so the reaper has to delete BY POD NAME. Deleting by handle finds
  # nothing, the NotFound is swallowed as routine, and the pod leaks forever
  # while its row is destroyed — the cluster fills with pods no record points
  # at. The scenario above proves the label is READ; this one proves it is
  # USED to delete.
  @GC-03 @GC-07
  Scenario: A destroyed container's readable pod is deleted by its pod name
    Given a Kubernetes worker whose reaper is running
    And a container "handle-bbb" on this worker is being destroyed
    And a pod "my-pipeline-my-job-b2-task-handle-b" is running, labelled with the handle "handle-bbb"
    When the reaper runs
    Then the pod "my-pipeline-my-job-b2-task-handle-b" is gone

  # The pod vanished between the list and the delete — a concurrent sweep, an
  # operator with kubectl, a node that went away. NotFound on delete is
  # routine and must not fail the sweep: an erroring reaper returns before
  # cleaning up the artifacts those containers produced, and reports failure
  # on every cycle thereafter.
  #
  # The scenario above ("already vanished") does NOT reach this path: with no
  # pod at all, DestroyContainers removes the row first and
  # FindDestroyingContainers then returns nothing, so the delete loop never
  # runs. The container here has a pod at list time, which is what keeps its
  # row alive long enough for the delete to be attempted.
  @GC-07
  Scenario: A pod deleted by someone else mid-sweep does not fail the reaper
    Given a Kubernetes worker whose reaper is running
    And a container "racing" on this worker is being destroyed
    And a pod "racing" is running, labelled with the handle "racing"
    And the pod is deleted by someone else before the reaper gets to it
    When the reaper runs
    Then the reaper completes without error
    And the container "racing" is still tracked as "destroying"

  # A reaper that cannot reach the cluster has reclaimed nothing. Saying so is
  # the difference between an operator seeing a broken component and an
  # operator seeing a healthy one while pods pile up.
  Scenario: A reaper that cannot reach the cluster says so
    Given a Kubernetes worker whose reaper is running
    And the cluster stops answering when the reaper lists pods
    When the reaper runs
    Then the reaper reports that it could not sweep

  # GC-01's other edge. A namespace is shared: another Concourse worker, or a
  # pod nobody labelled, can sit alongside this worker's. Reaping by the label
  # selector is what keeps them apart, and the failure is not a miscount — an
  # unrecognised pod has no container row, so it is marked destroying and then
  # deleted. One worker would delete another worker's running builds.
  @GC-01
  Scenario: Another worker's pods in the same namespace are left alone
    Given a Kubernetes worker whose reaper is running
    And a pod "someone-elses-pod" belonging to another worker is running in the same namespace
    When the reaper runs
    And the reaper runs again
    Then the pod "someone-elses-pod" is still there
