@GC-01 @GC-02 @GC-03 @GC-04 @GC-05 @GC-06 @GC-07
Feature: Reclaiming pods and the rows that track them

  Every finished step leaves a pod and a database row behind. The reaper's job
  is to reclaim both without ever reclaiming one that is still needed — a pod
  deleted too early loses a running build, and a row deleted too early makes a
  live container invisible to the scheduler.

  Source: k8s_runtime_behavioral_spec_20260331 — GC-01 to GC-07. Migrated whole
  from reaper_test.go, which carried no requirement identifiers.

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
