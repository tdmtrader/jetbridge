@review @review-linux
Feature: A local receipt preserves a detached review invocation

  Completed submissions read the uploaded bundle through Hangar's real node
  materializer, including its receipt at the input mount root.

  Background:
    Given a Run producer and a ready output node
    When its runtime producer publishes a successful review
    And its Run records the published source release
    And its published producer finishes as "succeeded"
    And its aggregate Run result is inspected
    And its other Run jobs finish as "succeeded"
    And its scheduler consumes all requested Run work
    And the aggregate Run completion is attempted
    And its result is admitted as a named input with "an input and a result"

  Scenario Outline: Interrupting submission keeps the same durable Run
    Then a saved review submission encounters <case>

    Examples:
      | case |
      | "interruption before readiness" |
      | "a fresh client replay" |
      | "a changed input" |
      | "a different template" |
      | "a receipt inside the input" |
      | "a fresh CLI replay" |
      | "a changed input through MCP" |
      | "the installed review template" |
      | "ready CLI" |
      | "ready MCP" |
