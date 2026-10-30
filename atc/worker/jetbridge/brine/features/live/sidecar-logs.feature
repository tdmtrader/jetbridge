@SC-07 @live-kubernetes
Feature: Sidecar output reaches the build through real Kubernetes execution

  Scenario Outline: Sidecar output reaches its dedicated stream and nothing else
    Given a task with sidecar output in "<mode>" mode
    When the task and its sidecar execute on Kubernetes
    Then every sidecar line reaches the intended build stream

    # Every sidecar line arrives on the sidecar's own writer, byte for byte,
    # and none on the step's stdout. There is no other place for them: the
    # `[helper] `-prefixed fallback into stdout was cut (one mechanism per
    # job), so the exec and direct paths now expect the same thing.
    Examples:
      | mode   |
      | exec   |
      | direct |
