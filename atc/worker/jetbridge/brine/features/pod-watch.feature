@PW-01 @PW-02 @PW-03 @PW-04 @PW-05 @PW-06 @PW-07
Feature: Keeping sight of a pod while a step runs

  A step that stops hearing about its pod hangs until it times out. So the
  runtime's watch has one job — keep telling the step what the pod is doing —
  and it has to keep doing it when the Kubernetes connection drops, when it
  reconnects, and when it cannot reconnect at all.

  Source: k8s_runtime_behavioral_spec_20260331 — PW-01 to PW-07. Migrated from
  watch_test.go, which carried no requirement identifiers. PW-03 selector
  scoping uses real kubelet transitions in live/pod-watch.feature.

  # PW-01, PW-02: the first answer comes from a direct read, not from the
  # watch, so a pod that changed before the watch existed is not missed.
  Scenario: The first answer does not wait for a watch event
    Given a Kubernetes API with a Pending pod "watch-pod"
    When the runtime asks what its pod is doing
    Then the runtime is told its pod is "Pending"

  # PW-02 subsequent changes are covered by the real selector case in
  # live/pod-watch.feature, which additionally pins the returned pod identity.

  # PW-04/PW-05 replay of real lifecycle changes after pod deletion now
  # runs in live/pod-watch.feature.

  # PW-06 fallback after actual watch revocation and pod startup now runs
  # in live/pod-watch.feature.

  # PW-07: eviction, node failure, spot preemption and a human with kubectl all
  # arrive as the same event, and the step has to be told rather than hanging.
  Scenario: A pod deleted out from under the step is reported, not waited on
    Given a Kubernetes API with a Pending pod "doomed-pod"
    When the runtime asks what its pod is doing
    And the pod is deleted out from under the step
    Then the runtime is told its pod was deleted

  # Establish a real stream with a persisted annotation, then cancel only the next
  # read. Its independent watch lifetime keeps HTTP closure from masking a
  # missing cancellation branch in the blocking select.
  Scenario: Cancelling a build that is already waiting stops the wait
    Given a Kubernetes API with a Pending pod "hanging-pod"
    When the runtime asks what its pod is doing
    And the build is cancelled while the runtime waits for its pod
    Then the runtime stops waiting

  # The real Running -> Succeeded burst now lives in live/pod-watch.feature.

  # Expired-history recovery and post-deletion replay run with actual kubelet
  # completion in live/pod-watch.feature.
