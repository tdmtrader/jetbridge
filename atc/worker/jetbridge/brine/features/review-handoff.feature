@review @review-linux
Feature: Handing credentials to one already-started review worker

  Exercise real worker and helper processes, Unix sockets and tmpfs. Only the
  model subprocess is deterministic. This is the worker transport boundary;
  platform authorization and kubelet loss acceptance are separate requirements.

  Background:
    Given a committed review change with a deleted file and an external plan
    And the review change is captured from the command line

  Scenario: A completed handoff can disconnect while the review continues
    When the review socket handoff encounters "disconnect after readiness"
    Then the published review has verdict "no_findings"
    And the review session leaves no credential files or credential-bearing output
    And the captured review input is unchanged

  Scenario: Execution admission can precede the worker socket
    When the review socket handoff encounters "socket opens late"
    Then the published review has verdict "no_findings"
    And the review session leaves no credential files or credential-bearing output
    And the captured review input is unchanged

  Scenario Outline: Failed or abandoned handoffs never publish a review
    When the review socket handoff encounters <case>
    Then the review worker fails without publishing a report
    And the review session leaves no credential files or credential-bearing output

    Examples:
      | case                      |
      | "wrong Run"               |
      | "invalid credentials"     |
      | "oversized credentials"   |
      | "no connection"           |
      | "open credential stream"  |
      | "cancel before handoff"   |
      | "cancel after readiness"  |
