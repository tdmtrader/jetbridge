@SC-07 @live-kubernetes
Feature: Sidecar output reaches the build through real Kubernetes execution

  Scenario Outline: Sidecar output uses its dedicated stream or the labelled build log
    Given a task with "<routing>" sidecar output in "<mode>" mode
    When the task and its sidecar execute on Kubernetes
    Then every sidecar line reaches the intended build stream

    Examples:
      | routing   | mode   |
      | dedicated | exec |
      | fallback  | exec |
      | dedicated | direct |
      | fallback  | direct |
