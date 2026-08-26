Feature: Observability span events

  The runtime narrates a pod's startup onto the step's trace span, so an
  operator can see where a slow or stuck step actually spent its time.

  Source: k8s_runtime_behavioral_spec_20260331 — OE-02, OE-04.
  Migrated from behavioral_runtime_spec_test.go.

  @OE-02
  Scenario: Pod initialization is recorded on the wait span
    Given a jetbridge worker whose spans are recorded
    And an exec-mode task container "oe-span-handle" is running
    When the pod reports itself initialized and then running
    Then the step exits 0
    And the "k8s.exec-process.wait-for-running" span records the event "pod.initialized"

  @OE-04
  Scenario: The end of an image pull is recorded on the wait span
    Given a jetbridge worker whose spans are recorded
    And an exec-mode task container "oe-pull-handle" is running
    When the pod pulls its image and then starts while the step waits
    Then the step exits 0
    And the "k8s.exec-process.wait-for-running" span records the event "image.pulled"

  # OE-01. "The step waited four minutes to be scheduled" is only actionable
  # with the node attached — that is what turns a slow build into a capacity
  # question about a specific machine.
  @OE-01
  Scenario: The trace records which node the step landed on
    Given a jetbridge worker whose spans are recorded
    And an exec-mode task container "oe01-scheduled" is running
    When the pod is placed on node "node-oe01" and then starts
    Then the step exits 0
    And the "k8s.exec-process.wait-for-running" span records the event "pod.scheduled"
    And the "pod.scheduled" event names the node "node-oe01"

  # OE-05, OE-06. An init container is where inputs are staged, so its outcome
  # separates "the step failed" from "the step never got its inputs".
  @OE-05
  Scenario: A successful init container is recorded
    Given a jetbridge worker whose spans are recorded
    And an exec-mode task container "oe05-init-done" is running
    When an init container finishes with exit code 0 and the pod then starts
    Then the "k8s.exec-process.wait-for-running" span records the event "init.container.completed"

  # OE-07
  @OE-07
  Scenario: A sidecar coming up is recorded
    Given a jetbridge worker whose spans are recorded
    And an exec-mode task container "oe07-sidecar" is running
    When a sidecar "postgres" reaches running and the pod then starts
    Then the "k8s.exec-process.wait-for-running" span records the event "sidecar.started"

  # OE-09. A watch re-delivers the same state repeatedly. Without deduplication
  # the trace of a slow step fills with identical events and stops being
  # readable — which defeats the only reason to open it.
  @OE-09
  Scenario: A condition seen twice is recorded once
    Given a jetbridge worker whose spans are recorded
    And an exec-mode task container "oe09-dedup" is running
    When the pod is placed on node "node-oe09", observed twice, and then starts
    Then the "k8s.exec-process.wait-for-running" span records the event "pod.scheduled" exactly once
