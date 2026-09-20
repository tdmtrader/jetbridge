@SC-07 @live-kubernetes
Feature: Sidecar output reaches the build through real Kubernetes execution

  Scenario Outline: Sidecar output uses its dedicated stream or the labelled build log
    Given a task with "<routing>" sidecar output in "<mode>" mode
    When the task and its sidecar execute on Kubernetes
    Then every sidecar line reaches the intended build stream

    # With no dedicated writer the sidecar's lines arrive on the step's
    # stdout, each prefixed `[helper] `. The exec path (sidecar_logs.go)
    # leaves an unterminated final line unterminated; the direct path
    # terminates it, so the two `fallback` rows expect different tails.
    Examples:
      | routing   | mode   |
      | dedicated | exec   |
      | dedicated | direct |
      | fallback  | exec   |
      | fallback  | direct |
