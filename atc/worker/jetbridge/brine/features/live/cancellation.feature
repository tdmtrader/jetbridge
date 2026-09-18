@live-kubernetes
Feature: Cancellation of real commands

  Cancellation must stop work while retaining resource pods for hijack.
  Running commands are observed inside real pods. Task termination is
  observed through the kubelet before releasing an owned test finalizer.

  @PE-10 @supervised-cancellation
  Scenario Outline: Cancellation stops work and preserves only hijackable resource pods
    Given a task using Kubernetes
    And the worker can exec into pods
    When an exec-mode "<kind>" step "<handle>" is cancelled "<when>"
    Then the cancelled command stops and reports cancellation
    And the cancelled step's pod is "<pod>"

    Examples:
      | kind | handle         | when         | pod      |
      | get  | hijackable     | before-start | retained |
      | get  | running-get    | running      | retained |
      | task | waiting-task   | before-start | removed  |
      | task | running-task   | running      | removed  |
      | put  | running-put    | running      | retained |
      | check | running-check | running      | retained |

  Scenario: Ending a hijack session preserves the task being debugged
    Given a task using Kubernetes
    And the worker can exec into pods
    When a running hijack session on task "debugged-task" is cancelled
    Then ending the hijack preserves the original task and pod

  Scenario Outline: Resource commands preserve protocol streams and exit status
    Given a task using Kubernetes
    And the worker can exec into pods
    When a real "<kind>" resource command exits with status <status>
    Then the resource protocol preserves its streams and exit status

    Examples:
      | kind  | status |
      | get   | 0      |
      | put   | 17     |
      | check | 42     |
