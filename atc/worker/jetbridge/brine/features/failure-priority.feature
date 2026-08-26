@RF-09
Feature: Failure detection priority order

  A pod can present more than one terminal failure at once. When it does, the
  runtime reports the most actionable cause rather than the most recent one —
  an OOM kill is not allowed to hide behind the CrashLoopBackOff it caused.

  Source: k8s_runtime_behavioral_spec_20260331, RF-09. The full requirement
  states a five-level priority: OOMKilled, then terminal waiting states, then
  Evicted, then Unschedulable, then exit code. The two scenarios below are the
  two that behavioral_runtime_spec_test.go asserted; the remaining pairs are
  absent here because they were absent there. Adding a row is how they arrive.

  Scenario: An OOM kill is reported ahead of the crash loop it caused
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "rf09-oom-vs-crash" is running
    When the pod is "Running" with waiting reason "CrashLoopBackOff" and last terminated reason "OOMKilled"
    Then the step fails naming "OOMKilled"
    And the failure does not mention "CrashLoopBackOff"

  Scenario: An image pull failure is reported ahead of the exit code
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "rf09-pull-vs-exit" is running
    When the pod is "Pending" with waiting reason "ImagePullBackOff" and last terminated reason "none"
    Then the step fails naming "ImagePullBackOff"

  # RF-04: additional terminal waiting states. Every row below reuses the
  # vocabulary the priority scenarios already established — this requirement
  # migrated as feature text alone, with no new step definition.
  @RF-04
  Scenario Outline: A terminal waiting state fails the pod immediately
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "rf04-<slug>" is running
    When the pod is "Pending" with waiting reason "<reason>" and last terminated reason "none"
    Then the step fails naming "<reason>"

    Examples:
      | slug        | reason                     |
      | invalidname | InvalidImageName           |
      | configerr   | CreateContainerConfigError |
      | pullbackoff | ImagePullBackOff           |
      | errpull     | ErrImagePull               |
      | crashloop   | CrashLoopBackOff           |
