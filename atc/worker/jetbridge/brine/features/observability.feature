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
