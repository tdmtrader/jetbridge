@PE-01 @PE-11
Feature: A container across runs

  A step's container outlives any one execution of it. A check runs on a timer
  against the same container; a web restart re-attaches to a step already in
  flight. What the runtime remembers, and what it refuses to reuse, is the
  difference between a resumed build and a broken one.

  Source: k8s_runtime_behavioral_spec_20260331 — PE-01, PE-11.
  Migrated from container_test.go.

  # Terminal check-pod reuse now runs against actual completed pods in
  # live/container-lifecycle.feature.

  # PE-11. The property store is the runtime's in-process memory of a step's
  # result, and it is the first place Attach looks — before it asks Kubernetes
  # anything at all.
  @PE-11
  Scenario: What a container records can be read back
    Given a Kubernetes worker "recovery-worker" with a database behind it
    And the container records "my-key" as "my-value"
    Then reading it back yields "my-key" as "my-value"
    When 20 callers write distinct properties while as many callers read them
    Then all 20 property writes survive and readback snapshots are independent

  # Completed-task recovery now runs in live/task-command.feature: exit 0/3
  # annotations come from actual commands; the original runtime container
  # also recovers after UID-scoped pod deletion, with no annotation fallback.

  # And when nothing recorded the result, re-attaching must FAIL — reporting
  # success would mark an unfinished step complete and let the build proceed on
  # outputs that were never produced.
  @PE-12
  Scenario: With no record of the result the step is run again rather than assumed
    Given a Kubernetes worker "recovery-worker" with a database behind it
    And the pending pod has no recorded completion
    Then the step cannot be recovered and must be run again

    # The API supplies Pending. TestExecRecoveryPolicy retains the distinct
    # unreported phase as literal input without manufacturing API status.
