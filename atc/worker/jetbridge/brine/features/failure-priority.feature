@RF-06 @RF-09
Feature: Failure detection and deletion

  Literal Failed/ImagePullBackOff, Pending/CrashLoopBackOff without restart
  history, and Succeeded-without-container-status inputs now share the pure
  production pod-status policy table. They are not manufactured API events.
  Live startup, OOM-priority, compatibility-process and severed-artifact cases
  cover actual kubelet failures and execution; they retain their own premises.

  Scenario: A pod deleted while the runtime watches it is reported
    Given a Kubernetes worker "failure-worker" with a database behind it
    When the runtime watches pod "vanished-pod" until it is deleted
    Then the step is told the pod was deleted
