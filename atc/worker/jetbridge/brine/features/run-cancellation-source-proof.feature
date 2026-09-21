Feature: Cancellation preserves sources until the exact execution is proven finished

  @core-review
  Scenario: An accepted cancellation is not proof of execution finish
    Given a Run producer with an authoritative "start only" finish
    When cancellation tries to select source release without finish evidence
    Then the Run keeps its source and handoff open

  @core-review
  Scenario Outline: Only exact retained evidence opens the release path
    Given a Run producer with an authoritative "<state>" finish
    When cancellation evidence exercises "<case>"
    Then source proof follows its exact execution and transaction
    Examples:
      | state         | case                     |
      | success       | verified finish          |
      | failure       | verified finish          |
      | never started | fenced never started     |
      | success       | replay                   |
      | success       | rollback                 |
      | success       | signature                |
      | success       | node                     |
      | success       | pod                      |
      | success       | execution                |
      | start only    | executing                |
      | never started | missing start closure    |
      | never started | refused start closure    |
      | success       | stale owner              |

  @core-review
  Scenario: A finished daemon record still needs retained Run evidence
    Given a Run producer with an authoritative "success" finish
    When cancellation tries to select source release without finish evidence
    Then the Run keeps its source and handoff open
