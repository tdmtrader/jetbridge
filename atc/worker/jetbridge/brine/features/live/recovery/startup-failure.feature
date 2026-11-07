@RF-04 @RF-01 @RF-02 @RF-03 @RF-10 @live-kubernetes
Feature: Real Kubernetes startup refusals

  These cases diagnose actual Pending pods the scheduler cannot place or the
  kubelet cannot start.
  No status, command result, registry response or Secret is manufactured.
  The refusal classifier uses direct compatibility; timeout and image-pull
  diagnostics also cover the distinct production execProcess path.

  Scenario Outline: The compatibility runtime reports why the kubelet cannot start a task
    Given a task using Kubernetes
    When the compatibility runtime reads a task the kubelet cannot start because of "<cause>"
    Then the step fails naming "<reason>"

    Examples:
      | cause                     | reason                     |
      | an invalid image name     | InvalidImageName           |
      | a missing required secret | CreateContainerConfigError |
      | a failed image pull       | ErrImagePull               |
      | image pull backoff        | ImagePullBackOff           |

  # RF-08. A real init process holds Pending after scheduling. The production
  # execProcess reaches its original 200ms startup/scheduling deadline; this
  # is distinct from the direct compatibility classifier above.
  @RF-08
  Scenario: A pod that never starts times out with its state in the log
    Given a task using Kubernetes
    When the pod is scheduled but never reaches Running
    Then the step fails saying "timed out"
    And the build log shows "Pod Failure Diagnostics"

  # RF-10/SC-08. Real main and sidecar pull failures share the same diagnostic
  # and counter contract on both runtime paths. A sidecar cannot hide behind
  # an unfinished main container; no status or registry response is supplied.
  # "Condition: PodScheduled=True" is the line that tells a reader the pull
  # failed on a node the scheduler had already chosen, rather than the pod
  # never being placed; it is printed even though the condition is simply
  # True with no Reason.
  @RF-10
  Scenario Outline: An image-pull failure identifies its cause and is counted
    Given a task using Kubernetes
    When the "<execution>" runtime diagnoses an image-pull failure in "<container>"
    Then the step fails saying "ImagePullBackOff"
    And the build log names the requested image
    And the build log shows "Condition: PodScheduled=True"
    And the image pull failure count has gone up by 1
    And the failure came from "<execution>" execution

    Examples:
      | execution            | container  |
      | production           | main       |
      | direct compatibility | main       |
      | production           | my-sidecar |
      | direct compatibility | my-sidecar |

  # RF-07. A pod asks for more CPU than any eligible node has, even if empty.
  # The scheduler supplies the refusal; resource exec must retain its complete
  # reason and expire the original 2s startup / 3s scheduling deadline.
  @RF-07 @RF-09
  Scenario: A pod nothing can schedule fails the step instead of waiting
    Given a task using Kubernetes
    When a resource step waits for an unschedulable pod with 2000 milliseconds for startup and 3000 milliseconds for scheduling
    Then the step fails naming "Unschedulable"
    And the step fails naming "pod scheduling timeout"
    And the step reports the scheduler's complete refusal
    And the build log explains "waiting up to"
    And the build log explains "cluster resources"
