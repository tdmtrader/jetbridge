@PE-10 @RF-10 @RF-11 @RF-13
Feature: Runtime cleanup and diagnostic API wiring

  Real API deletion drives both production exec startup and direct compatibility.
  No pod or node status is supplied. Node cordoning is desired configuration;
  node absence is verified independently before the runtime reads it.
  Literal pressure, restart history and failure priority are covered by the
  shared pod-status and node-diagnostic policy tables. Real image-pull, OOM,
  eviction, completion and timeout behavior remains in the live features.

  Scenario: Legacy compatibility: A cancelled build takes its pod with it
    Given a Kubernetes worker "process-worker" with a database behind it
    And the worker uses "direct compatibility" execution
    And the worker prepares task "wait-cancelled" from image "busybox"
    And it declares no working directory
    And the described container starts
    When the build is cancelled while the step is waiting
    Then the step fails saying "context canceled"
    And the pod has been removed from the cluster

  Scenario Outline: Pod deletion preserves best-effort node diagnostics
    Given a Kubernetes worker "process-worker" with a database behind it
    And the worker uses "<execution>" execution
    When the runtime diagnoses pod deletion on a "<node>" node
    Then the build log shows "Pod Failure Diagnostics"
    And the build log shows "<detail>"
    And the build log shows "<explanation>"
    And the failure came from "<execution>" execution

    Examples:
      | execution            | node     | detail                   | explanation             |
      | production           | cordoned | cordoned (unschedulable)  | node may be draining    |
      | direct compatibility | cordoned | cordoned (unschedulable)  | node may be draining    |
      | production           | missing  | nonexistent-node         | unable to fetch details |
      | direct compatibility | missing  | nonexistent-node         | unable to fetch details |

  Scenario: Legacy compatibility: A cluster that stops answering fails the step rather than hanging
    Given a Kubernetes worker "read-worker" with a database behind it
    When every read of pod "transient-exhausted" fails
    Then the initial pod read exhausts 3 attempts
