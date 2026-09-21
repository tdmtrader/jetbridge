@review @review-linux
Feature: Producing typed findings without retaining session credentials

  Run the real Linux worker, tmpfs, Git and report renderer. Only the model
  process is substituted with deterministic output; no network model calls are
  made. These scenarios do not claim detached Run or Kubernetes-loss acceptance.

  Background:
    Given a committed review change with a deleted file and an external plan
    And the review change is captured from the command line

  Scenario Outline: Execution success is separate from the review verdict
    When the review worker receives model output <mode>
    Then the published review has verdict <verdict>
    And the review session leaves no credential files or credential-bearing output
    And the captured review input is unchanged

    Examples:
      | mode               | verdict       |
      | "valid"            | "no_findings" |
      | "finding"          | "findings"    |
      | "deletion-finding" | "findings"    |
      | "partial"          | "incomplete"  |
      | "missing-coverage" | "incomplete"  |

  Scenario Outline: Invalid model results never become authoritative reports
    When the review worker receives model output <mode>
    Then the review worker fails without publishing a report
    And the review session leaves no credential files or credential-bearing output
    And the captured review input is unchanged

    Examples:
      | mode            |
      | "nonzero"       |
      | "provider-startup-error" |
      | "bundle-paths" |
      | "missing"       |
      | "malformed"     |
      | "forbidden"     |
      | "invalid-line"  |
      | "foreign-path"  |
      | "unknown-field" |
      | "timeout"       |

  Scenario Outline: Each review needs its own valid subscription credentials
    When the review worker receives credentials <kind>
    Then the review worker fails without publishing a report
    And the review session leaves no credential files or credential-bearing output

    Examples:
      | kind            |
      | "api-key"       |
      | "empty"         |
      | "invalid-json"  |

  Scenario: A later review cannot reuse credentials from an earlier review
    When the review worker receives model output "valid"
    Then the published review has verdict "no_findings"
    When a second review receives no subscription tokens
    Then the review worker fails without publishing a report
    And the review session leaves no credential files or credential-bearing output

  Scenario: An abandoned credential handoff has a bounded session lifetime
    When the review credential pipe is left open without delivering credentials
    Then the review worker fails without publishing a report
    And the review session leaves no credential files or credential-bearing output

  Scenario: Terminating an active worker destroys its refreshed credentials
    When the review worker is terminated after the model refreshes credentials
    Then the review worker fails without publishing a report
    And the review session leaves no credential files or credential-bearing output

  Scenario: A later invocation cannot overwrite an authoritative report
    When the review worker receives model output "finding"
    Then the published review has verdict "findings"
    And the existing review is protected from a second invocation
