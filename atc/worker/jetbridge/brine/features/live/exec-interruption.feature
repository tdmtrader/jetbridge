@RF-15 @live-kubernetes
Feature: A broken exec stream gets a real pod diagnosis

  # Also assert the runtime never creates or execs again after output. The
  # defensive upgrade-error-after-output combination is a pure policy test,
  # not a manufactured error returned by this real transport.

  Scenario: An exec severed by a vanished pod says the pod is gone
    Given a task using Kubernetes
    When its exec connection is cut and its pod "disappears"
    Then the step fails saying "exec in pod"
    And the build log shows "pod no longer exists"

  @RF-01
  Scenario: An interrupted exec reports the container's OOM kill
    Given a task using Kubernetes
    When its exec connection is cut and its pod "runs out of memory"
    Then the step fails saying "exec in pod"
    And the build log shows "Pod Failure Diagnostics"
    And the build log shows "OOMKilled"
    And the build log names the task's actual node
    When the compatibility watcher diagnoses the same OOM-killed pod
    Then the step fails naming "OOMKilled"
    And the step fails naming "exceeded memory limit"
    And the build log explains "Pod Failure Diagnostics"
