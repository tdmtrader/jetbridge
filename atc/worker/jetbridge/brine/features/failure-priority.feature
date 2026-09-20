@RF-06 @RF-09
Feature: Failure detection and deletion

  Literal Failed/ImagePullBackOff, Pending/CrashLoopBackOff without restart
  history, and Succeeded-without-container-status inputs go through the
  classification both runtime paths open-code in process.go: isPodOOMKilled,
  then isPodFailedFast, then interruptionErrorForPod, then podExitCode,
  written out once in Process.pollUntilDone and again in
  execProcess.waitForRunning. There is no shared policy table — podStateFor,
  which had factored those four into one, went with the rest of pod_status.go
  in commit 388692eb54. These are not manufactured API events.

  Live startup, OOM-priority and compatibility-process cases cover actual
  kubelet failures and execution; they retain their own premises. The
  severed-artifact case does NOT: it is in
  features/live/pending/severed-artifact.feature and never runs, because
  without exec_status.go's statusCheckingUpgrader core reports an exec whose
  socket died as a success, so the scenario is red against core by design.

  Scenario: A pod deleted while the runtime watches it is reported
    Given a Kubernetes worker "failure-worker" with a database behind it
    When the runtime watches pod "vanished-pod" until it is deleted
    Then the step is told the pod was deleted
