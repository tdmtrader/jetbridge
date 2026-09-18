@live-kubernetes @PE-01
Feature: Actual Git resources

  The upstream resource executes against an owned Git repository inside its
  pod. The put input is supplied through the real volume API into its mount.
  This is not daemon publication or automatic init-container artifact fetch.

  Scenario Outline: A Git resource preserves its protocol and container contract
    Given a task using Kubernetes
    And the worker can exec into pods
    When a Git "<kind>" resource runs with a "<request>" request
    Then the Git resource reports "<outcome>"
    And the Git resource keeps its container and pod contract

    Examples:
      | kind  | request | outcome   |
      | get   | valid   | fetched   |
      | put   | valid   | published |
      | check | valid   | checked   |
      | get   | invalid | rejected  |
