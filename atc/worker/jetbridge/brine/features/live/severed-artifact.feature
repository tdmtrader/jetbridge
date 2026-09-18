@live-kubernetes @severed-artifact
Feature: A severed task cannot publish its partial artifact

  Scenario: A step torn from its pod leaves nothing for a later step to read
    Given a task using Kubernetes
    When a task step whose connection to its pod is severed while it writes "out"
    Then the step fails rather than reporting success
    And the half-written artifact cannot be located by a later step
