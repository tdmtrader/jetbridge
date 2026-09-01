Feature: Core ATC values retain their user-visible semantics

  Source: all 15 specs in atc/container_limits_test.go, all 8 in
  atc/build_test.go, all 5 in atc/public_plan_test.go, and all 3 in
  atc/worker_test.go. These 31 specs execute production value methods directly.

  Scenario Outline: Memory profile <profile> parses to <value>
    When the production ATC value model handles profile "memory-<profile>"
    Then the ATC value result is "<value>"
    And the ATC value operation returned no error

    Examples:
      | profile       | value                    |
      | bytes         | 1024                     |
      | kb            | 1024                     |
      | mb            | 1048576                  |
      | gb            | 1073741824               |
      | kib           | 1024                     |
      | mib           | 1048576                  |
      | gib           | 1073741824               |
      | case-unit     | equal=true               |
      | case-binary   | equal=true               |
      | single-prefix | 1024,1048576,1073741824  |

  Scenario: Invalid memory text is rejected
    When the production ATC value model handles profile "memory-invalid"
    Then the ATC value operation returned an error

  Scenario Outline: Ephemeral storage profile <profile> serializes as <value>
    When the production ATC value model handles profile "ephemeral-<profile>"
    Then the ATC value result is "<value>"
    And the ATC value operation returned no error

    Examples:
      | profile | value       |
      | number  | 1073741824  |
      | 5g      | 5368709120  |
      | 2gib    | 2147483648  |
      | omit    | {}          |

  Scenario Outline: Build value profile <profile> is <value>
    When the production ATC value model handles profile "build-<profile>"
    Then the ATC value result is "<value>"

    Examples:
      | profile             | value |
      | one-off             | true  |
      | job                 | false |
      | running-pending     | true  |
      | running-started     | true  |
      | running-finished    | false |
      | abortable-pending   | true  |
      | abortable-started   | true  |
      | abortable-finished  | false |

  Scenario Outline: Worker version profile <profile> has result <result>
    When the production ATC value model handles profile "worker-<profile>"
    Then the ATC value result is "<result>"
    And the ATC value operation returned no error

    Examples:
      | profile | result |
      | empty   | valid  |
      | numeric | valid  |

  Scenario: Non-numeric worker versions are rejected
    When the production ATC value model handles profile "worker-invalid"
    Then the ATC value operation returned an error

  Scenario Outline: Public plan profile <profile> produces <result>
    When the production ATC value model handles profile "<profile>"
    Then the ATC value result is "<result>"

    Examples:
      | profile                  | result                                                        |
      | sidecar-id               | 42/sidecar/cloud-sql-proxy                                    |
      | sidecar-new              | 10/sidecar/redis:redis:redis:7                                 |
      | sidecar-public           | id=5/sidecar/postgres;name=postgres;image=postgres:16;has-image=true |
      | sidecar-public-no-image  | id=5/sidecar/helper;name=helper;image=<nil>;has-image=false    |
      | plan-sanitized           | sanitized=true                                                |
