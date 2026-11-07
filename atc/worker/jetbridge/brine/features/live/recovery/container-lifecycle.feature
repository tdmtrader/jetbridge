@live-kubernetes @PE-01 @PE-11
Feature: A real container across runs

  Actual previous pod exits supply both terminal phases. The production Run
  call must prepare a fresh unfinished pod with a new API UID. This covers
  pod replacement, not execution of the subsequent resource check command.

  @PE-01 @check-pod-reuse
  Scenario Outline: A finished pod is replaced, not reused
    Given a task using Kubernetes
    And the worker has a previous check pod in phase "<phase>"
    When the check runs again
    Then the check gets a new unfinished pod

    Examples:
      | phase     |
      | Succeeded |
      | Failed    |

  # TERM, a verified 64Mi cgroup OOM, or a bounded 24Mi/16Mi emptyDir eviction
  # stops only the owned pod. Kubelet supplies status; dial requests are unchanged.
  # OOM/eviction use explicit fixture pods; only their replacements are runtime-created.
  # Eviction replacement needs Failed/Evicted, not a node-pressure message.
  # Preemption metadata is covered by "Replacing a dead pause pod"
  # (pause_pod_replacement_test.go), whose death cases include Failed with
  # DisruptionTarget=PreemptionByScheduler; these real terminal pods verify
  # startup/dial recovery and creation/exec counts.
  # The second-death row delays CREATE delivery until real TERM has completed.
  @pause-recovery
  Scenario Outline: A task spends at most one replacement on a dead pause pod
    Given a task using Kubernetes
    When the pause pod stops "<when>" from "<cause>" before the task can execute
    Then the task reports "<outcome>" after 2 pod creations and <execs> exec attempts

    Examples:
      | when              | cause             | outcome                      | execs |
      | before startup    | TERM              | success                      | 1     |
      | on the first dial | TERM              | success                      | 2     |
      | on both dials     | TERM              | exec failure                 | 2     |
      | before startup    | OOMKilled         | success with OOM diagnostics | 1     |
      | on the first dial | OOMKilled         | success with OOM diagnostics | 2     |
      | before startup    | Evicted           | success with eviction log    | 1     |
      | on the first dial | Evicted           | success with eviction log    | 2     |
      | before startup    | Evicted then TERM | startup failure              | 0     |
