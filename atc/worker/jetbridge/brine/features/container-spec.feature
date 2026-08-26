Feature: Container spec construction

  What a described container becomes in the pod spec: which image it resolves
  to, how it is pulled, and how environment variables from the container spec
  and the process spec combine.

  Source: k8s_runtime_behavioral_spec_20260331 — PE-03, PE-05, PE-06.
  Migrated from behavioral_runtime_spec_test.go.

  @PE-03
  Scenario: The main container is pulled only when absent
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "pe03-handle" built from image "busybox:latest"
    When the container runs
    Then the main container is named "main"
    And the main container image pull policy is "IfNotPresent"

  @PE-05
  Scenario Outline: A Concourse image URL prefix is stripped
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "pe05-<slug>" built from image "<raw>"
    When the container runs
    Then the main container image is "<resolved>"

    Examples:
      | slug      | raw                      | resolved       |
      | docker3   | docker:///busybox:latest | busybox:latest |
      | docker2   | docker://busybox:latest  | busybox:latest |
      | rawpfx    | raw:///alpine:3          | alpine:3       |
      | plain     | alpine:3.18              | alpine:3.18    |

  @PE-06
  Scenario: Environment merges both specs
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "pe06-handle" built from image "busybox"
    And the container environment sets "CONTAINER_VAR=from_container"
    And the container environment sets "SHARED_VAR=container_value"
    And the process environment sets "PROCESS_VAR=from_process"
    When the container runs
    Then the main container environment resolves "CONTAINER_VAR" to "from_container"
    And the main container environment resolves "PROCESS_VAR" to "from_process"

  @PE-06
  Scenario: The process spec wins a collision
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "pe06-override-handle" built from image "busybox"
    And the container environment sets "SHARED_VAR=from_container"
    And the process environment sets "SHARED_VAR=from_process"
    When the container runs
    Then the main container environment resolves "SHARED_VAR" to "from_process"
