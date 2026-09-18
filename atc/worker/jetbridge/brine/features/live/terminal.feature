@live-kubernetes @PE-08
Feature: Terminal choices reach resource and supervised task processes

  BusyBox observes the resource process's real terminal. A supervised task with
  nil stdin must also forward the declared TTY choice in exactly one exec.
  Its command writes through supervisor logs, so file redirection is not a TTY oracle.

  Scenario Outline: Both execution paths retain the requested terminal mode
    Given a task using Kubernetes
    When resource and supervised task steps run with "<attached>" terminal attached
    Then the resource reports "<output>" and the task retains its terminal choice

    Examples:
      | attached | output   |
      | one      | terminal |
      | none     | pipe     |
