@live-kubernetes @artifact-integration
Feature: Persisted artifacts cross real task and resource boundaries

  @CO-09
  Scenario: An artifact a previous step persisted becomes this step's input
    Given a task using Kubernetes
    When a persisted artifact becomes a task input
    Then the step's pod mounts "/tmp/build/workdir/my-input"
    And the step's output is "artifact data received"
    And the step reports exit status 0
    And the step's container row is a created "task" container on worker "k8s-worker-1"

  Scenario: A task's output reaches the put step that publishes it
    Given a task using Kubernetes
    When a task's output is published by a Git put
    Then the resource published the task's commit
    And the step reports exit status 0
    And the step's pod mounts "/tmp/build/put/binary"
    And the step's container row is a created "put" container on worker "k8s-worker-1"
