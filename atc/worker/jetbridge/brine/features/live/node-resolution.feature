@live-kubernetes
Feature: Resolving a real node keeps its published address cached

  This is read-only against the selected cluster. Only a local forwarding
  route is closed; no shared Node, status or RBAC object is changed.

  Scenario: A node keeps resolving after its API route becomes unavailable
    When a real node is resolved before and after its API route closes
    Then both answers retain its published internal address with only one API read
