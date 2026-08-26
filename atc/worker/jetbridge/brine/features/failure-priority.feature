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

  # RF-01, RF-02, RF-03: the failures a user meets most often. Each row is a
  # different way the cluster refuses to run the step, and in every case the
  # build log has to name the cause — "the step failed" is not actionable.
  @RF-01 @RF-02 @RF-03
  Scenario Outline: A container that cannot start says why
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "rf-<slug>" is running
    When the pod is "Pending" with waiting reason "<reason>" and last terminated reason "none"
    Then the step fails naming "<reason>"

    Examples:
      | slug        | reason           |
      | pullbackoff | ImagePullBackOff |
      | errpull     | ErrImagePull     |
      | crashloop   | CrashLoopBackOff |

  # RF-01. An OOM kill is a memory problem, and saying so is the whole value —
  # the user has to know to raise the limit rather than debug their code.
  @RF-01
  Scenario: A step killed for using too much memory is told so
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "rf01-oom" is running
    When the main container is killed for using too much memory
    Then the step fails naming "OOMKilled"
    And the failure explains "exceeded memory limit"
    And the build log explains "Pod Failure Diagnostics"

  # RF-05. The cluster took the pod away; this is not the pipeline's fault and
  # the log has to make that distinguishable.
  @RF-05
  # DRIFT, recorded rather than smoothed over. RF-05 specifies the error
  # `"pod failed: Evicted: %s"`. The code reports `pod interrupted: evicted`
  # (process.go:1290) — an INTERRUPTION rather than a failure, which is a
  # different build classification, not just different wording. The scenario
  # states what the code does; the specification is stale.
  Scenario: An evicted step reports the eviction, not a generic failure
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "rf05-evicted" is running
    When the node evicts the pod
    Then the failure explains "evicted"
    And the build log explains "Evicted"

  # RF-07 is NOT migrated, and the reason is worth recording rather than
  # hiding. An unschedulable pod is only reported once the SCHEDULING deadline
  # passes, and that deadline is only enforced on the exec-mode path
  # (process_test.go tests it with a ContainerTypeGet container and an
  # executor). Expressed over the direct-mode chain used above, the scenario
  # does not fail — it hangs for the 15-minute default, which is how it was
  # found. Migrating it needs an impatient EXEC-mode worker; the vocabulary
  # for that exists ("a jetbridge worker that waits only seconds for a pod to
  # be scheduled") and is left in place for whoever writes it.

  # RF-06
  @RF-06
  Scenario: A pod deleted underneath a running step is reported
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "rf06-deleted" is running
    When the pod is deleted from the cluster
    Then the step is told the pod was deleted

  # F23. When the exec connection to a running step is severed — a web restart,
  # an API-server rollout — the step's process is still alive in the pod and
  # still writing its outputs. If the runtime published an artifact location
  # anyway, an on_failure or on_error hook could stream out a HALF-WRITTEN
  # artifact and get no error at all. The missing location is what makes the
  # hook fail fast instead.
  #
  # The assertion is about an ABSENCE, which is precisely what a spy assertion
  # cannot distinguish from "the call happened with different arguments".
  Scenario: A step torn from its pod leaves nothing for a later step to read
    Given a task step whose connection to its pod is severed while it writes "out"
    Then the step fails rather than reporting success
    And the half-written artifact cannot be located by a later step

  # RF-07. Nothing in the cluster can host this pod — the wrong node selector,
  # a resource request no node can satisfy, a cluster at capacity. The step
  # has to be told, with the scheduler's own message, rather than waiting.
  #
  # This is EXEC MODE, and that is not incidental. Written over the
  # direct-mode chain the scenario HANGS: Process.pollUntilDone blocks in
  # watcher.Next, so it only re-checks the scheduling deadline when another
  # watch event arrives, and a pod nobody can schedule stops producing them.
  # execProcess.waitForRunning polls on a timer instead and does time out.
  # The scenario was removed once before for exactly this hang; putting it on
  # the right chain is what fixes it, not a longer timeout.
  @RF-07
  Scenario: A pod nothing can schedule fails the step instead of waiting
    When a resource step waits for a pod nothing in the cluster can schedule
    Then the step fails naming "Unschedulable"
    And the step fails naming "pod scheduling timeout"

  # A pod can reach a terminal phase carrying no container status at all —
  # the kubelet pruned it, or the container never started. podExitCode falls
  # back on the phase alone, and that fallback decides whether a build passes.
  #
  # Both directions were uncovered. A Failed pod defaulting to exit 0 turns a
  # task that died into a green build, which is the worse of the two.
  Scenario: A failed pod with no container status still fails the step
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "no-status-failed" is running
    When the pod reaches "Failed" without ever reporting a container status
    Then the step's exit status is 1

  Scenario: A succeeded pod with no container status still passes the step
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "no-status-succeeded" is running
    When the pod reaches "Succeeded" without ever reporting a container status
    Then the step's exit status is 0

  # The step's result is the MAIN container's exit code. A sidecar that
  # finished first must not supply it: a log shipper exiting cleanly would
  # then mask a task that failed, and the build would go green on a step that
  # did not work.
  Scenario: A clean sidecar does not mask a failing task
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "sidecar-mask" is running
    When a sidecar exits 0 before the main container exits 1
    Then the step's exit status is 1
