@RF-06 @RF-09
Feature: Failure detection and deletion

  Literal Failed/ImagePullBackOff, Pending/CrashLoopBackOff without restart
  history, and Succeeded-without-container-status inputs go through the
  classification podStateFor in pod_status.go holds: isPodOOMKilled, then
  isPodFailedFast, then interruptionErrorForPod, then podExitCode.
  Process.pollUntilDone (direct_process.go), execProcess.waitForRunning
  (exec_readiness.go) and jobProcess.followPod (job_process.go) all call it.
  These are not manufactured API events.

  Live startup, OOM-priority and compatibility-process cases cover actual
  kubelet failures and execution; they retain their own premises. The
  severed-artifact case runs in features/live/severed-artifact.feature and
  asserts that an exec whose socket dies is reported as a failure rather
  than a success.

  Scenario: A pod deleted while the runtime watches it is reported
    Given a Kubernetes worker "failure-worker" with a database behind it
    When the runtime watches pod "vanished-pod" until it is deleted
    Then the step is told the pod was deleted
