@live-kubernetes
Feature: Direct compatibility process completion

  These cases cover the explicit Process fallback, not production task
  execution. Real BusyBox commands and kubelet status replace reported exits.
  The controller-failure case separately covers a pod that never executes.

  # PE-09. The exit status is the step's whole result, and the pod going away
  # afterwards is what keeps a busy cluster from filling with finished pods.
  @PE-09
  Scenario: Legacy compatibility: A step whose pod succeeds reports success and leaves nothing behind
    Given a task using Kubernetes
    When the compatibility pod ends with its main container exiting 0
    Then the step comes back with exit status 0
    And the pod has been removed from the cluster

  # A non-zero exit is a failed step, NOT a failed runtime: the error return is
  # reserved for the runtime being unable to say what happened. Conflating the
  # two is what turns "your tests failed" into "errored".
  @PE-09
  Scenario: Legacy compatibility: A step whose command fails reports the exit code rather than an error
    Given a task using Kubernetes
    When the compatibility pod ends with its main container exiting 1
    Then the step comes back with exit status 1

  # PodGC, not a status setter, fails an unscheduled terminating pod. A
  # scheduling gate prevents execution and an owned finalizer retains the
  # actual Failed/no-container-status state for compatibility Process.Wait.
  @RF-09 @controller-fallback
  Scenario: A failed pod with no container status still fails the step
    Given a task using Kubernetes
    When the controller fails an unscheduled pod before any container starts
    Then the step's exit status is 1

  # SC-10. The pod outlives the step's own command whenever a sidecar is still
  # running, so somebody has to take it away. If nobody does, a postgres
  # sidecar runs until the reaper notices, on a node the build has finished
  # with.
  @SC-10
  Scenario Outline: Legacy compatibility: main completion wins over a running sidecar
    Given a task using Kubernetes
    When compatibility pod "<handle>" exits <exit> while sidecar "<sidecar>" runs "<image>"
    Then the step comes back with exit status <exit>
    And the pod has been removed from the cluster

    Examples:
      | handle            | sidecar  | image       | exit |
      | sidecar-lifecycle | postgres | postgres:15 | 0    |
      | sidecar-nonzero   | redis    | redis:7     | 42   |

  # The kubelet reports the clean sidecar before main is released to fail.
  # Its status is also first in the actual array: selecting any terminated
  # container would mask the failing main. No status or result is supplied.
  Scenario: A clean sidecar does not mask a failing task
    Given a task using Kubernetes
    When a sidecar exits 0 before the main container exits 1
    Then the step's exit status is 1

  # RF-12. Real pod-read permission is revoked, then restored after one or two
  # API denials. Responses are forwarded unchanged; the runtime must retry.
  @RF-12
  Scenario Outline: Legacy compatibility: A step survives transient failures to read its pod
    Given a task using Kubernetes
    When pod "transient-tolerated" succeeds but the next <failures> status reads fail
    Then the step comes back with exit status 0

    Examples:
      | failures |
      | 1        |
      | 2        |

  # SC-09. Main's completed command wins over a sidecar pull refusal.
  # The kubelet leaves this pod Pending, not the reported fixture's Running.
  @SC-09
  Scenario: Legacy compatibility: A sidecar that breaks after the step has finished does not fail it
    Given a task using Kubernetes
    When the sidecar "bad-image" has an image-pull failure after the main container exited 0
    Then the step comes back with exit status 0

  # RF-05. Exceeding a tiny emptyDir size limit triggers pod-local kubelet
  # eviction without node-wide pressure. Preserve the actual interruption
  # classification and the original diagnostic assertion.
  # The older RF-05 spec says "pod failed: Evicted"; current behavior is
  # "pod interrupted: evicted", a distinct build classification, not just wording.
  @RF-05 @RF-09
  Scenario: An evicted step reports the eviction, not a generic failure
    Given a task using Kubernetes
    When the kubelet evicts a pod that exceeds its scratch volume limit
    Then the step fails naming "evicted"
    And the step was interrupted rather than failed, because it was "evicted"
    And the build log explains "Pod Failure Diagnostics"
    And the build log explains "Evicted"
