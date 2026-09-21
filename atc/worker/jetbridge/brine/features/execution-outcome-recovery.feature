Feature: Exact execution recovers a completed command after losing its response

  @core-review
  Scenario: The supervisor's retained exit is recovered without running the command again
    Given a Run producer and a ready output node
    When a real controlled command loses its completed exec response
    Then its exact outcome survives without repeating the command

  @core-review
  Scenario Outline: Recovery cannot invent an exit from a missing or foreign journal
    Given a Run producer and a ready output node
    When a completed controlled command loses its response with "<fault>"
    Then the execution stays unresolved without repeating the command
    Examples:
      | fault           |
      | missing exit    |
      | corrupt exit    |
      | replacement Pod |

  @core-review
  Scenario: An uncertain command delivery is closed as stopped, never reopened
    Given a Run producer and a ready output node
    When a controlled command reconnects after an uncertain start delivery
    Then the execution is closed as stopped without running the command
