@PW-01 @PW-02 @PW-03 @PW-04 @PW-05 @PW-06 @PW-07
Feature: Keeping sight of a pod while a step runs

  A step that stops hearing about its pod hangs until it times out. So the
  runtime's watch has one job — keep telling the step what the pod is doing —
  and it has to keep doing it when the Kubernetes connection drops, when it
  reconnects, and when it cannot reconnect at all.

  Source: k8s_runtime_behavioral_spec_20260331 — PW-01 to PW-07. Migrated whole
  from watch_test.go, which carried no requirement identifiers.

  # PW-01, PW-02: the first answer comes from a direct read, not from the
  # watch, so a pod that changed before the watch existed is not missed.
  Scenario: The first answer does not wait for a watch event
    Given a pod "watch-pod" that the runtime is watching
    And the connection to Kubernetes is steady
    When the runtime asks what the pod is doing
    Then the runtime is told the pod is "Pending"

  Scenario: Subsequent changes arrive as they happen
    Given a pod "watch-pod" that the runtime is watching
    And the connection to Kubernetes is steady
    When the runtime asks what the pod is doing
    And the pod becomes "Running"
    Then the runtime is told the pod is "Running"

  # PW-04, PW-05: the reconnect carries the last resource version, so a change
  # that lands during the gap is still delivered. This is the scenario that
  # would fail if the runtime reconnected from scratch.
  Scenario: A dropped connection does not lose the change that happened during it
    Given a pod "reconnect-pod" that the runtime is watching
    And the connection to Kubernetes drops and comes back
    When the runtime asks what the pod is doing
    And the watch connection is interrupted and the pod becomes "Running"
    Then the runtime is told the pod is "Running"

  # PW-06: when the watch cannot be re-established the runtime falls back to
  # reading directly rather than giving up on the step.
  Scenario: A watch that cannot be re-established still reports the pod
    Given a pod "fallback-pod" that the runtime is watching
    And the connection to Kubernetes drops and cannot be re-established
    When the runtime asks what the pod is doing
    And the watch connection is interrupted and the pod becomes "Running"
    Then the runtime is told the pod is "Running"

  # PW-07: eviction, node failure, spot preemption and a human with kubectl all
  # arrive as the same event, and the step has to be told rather than hanging.
  Scenario: A pod deleted out from under the step is reported, not waited on
    Given a pod "doomed-pod" that the runtime is watching
    And the connection to Kubernetes is steady
    When the runtime asks what the pod is doing
    And the pod is deleted out from under the step
    Then the runtime is told the pod was deleted

  Scenario: Cancelling the build stops the wait
    Given a pod "watch-pod" that the runtime is watching
    And the connection to Kubernetes is steady
    When the runtime asks what the pod is doing
    And the build is cancelled
    Then the runtime is told to stop waiting

  # A watch delivers a burst of updates faster than the step consumes them.
  # What must never happen is the step settling on a stale one — it would wait
  # on a pod state the cluster has already left behind.
  Scenario: A burst of updates does not leave the step on a stale state
    Given a pod "rapid-pod" that the runtime is watching
    And the connection to Kubernetes is steady
    When the runtime asks what the pod is doing
    And the pod becomes "Running"
    And the pod becomes "Succeeded"
    Then the runtime is told the pod is "Succeeded"

  # PW-03. The watch follows ONE pod. The neighbour below sits in a different
  # phase on purpose: a watch that reported the wrong pod would say "Running"
  # here. On a busy cluster this is the difference between a working watch and
  # a step that reacts to somebody else's pod.
  @PW-03
  Scenario: The watch follows one pod, not the whole namespace
    Given a pod "selective-pod" that the runtime is watching
    And another pod "noisy-neighbour" is churning in the same namespace
    And the connection to Kubernetes is steady
    When the runtime asks what the pod is doing
    Then the runtime is told the pod is "Pending"
