@live-kubernetes @severed-artifact
Feature: A severed task cannot publish its partial artifact

  `steps/live_severed_artifact.go` closes the owned TLS route while the task
  is still appending to its output. An error stream that never carried a
  status is not exit zero (exec_status.go), so the half-written artifact is
  never published for a later step to read.

  Scenario: A step torn from its pod leaves nothing for a later step to read
    Given a task using Kubernetes
    When a task step whose connection to its pod is severed while it writes "out"
    Then the step fails rather than reporting success
    And the half-written artifact cannot be located by a later step
