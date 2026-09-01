Feature: Sidecar configuration uses the production parser and union model

  Source: all 24 specs in atc/sidecar_test.go, with YAML/JSON passed directly
  to production parsing, validation, marshaling, and unmarshaling methods.

  Scenario Outline: Valid sidecar profile <profile> produces <result>
    When the production sidecar model handles profile "<profile>"
    Then the sidecar result is "<result>"
    And the sidecar operation returned no error

    Examples:
      | profile          | result                         |
      | parse-full       | full                           |
      | parse-multiple   | postgres,redis                 |
      | parse-minimal    | minimal=true                   |
      | parse-empty      | count=0                        |
      | json-round-trip  | equal=true                     |
      | source-string    | file=repo/sidecar.yml          |
      | source-object    | postgres:postgres:15           |
      | marshal-file     | json-string                    |
      | marshal-object   | redis:redis:7                   |
      | mixed-round-trip | repo/custom.yml,postgres,redis |
      | validate-valid   | valid                          |
      | validate-protocols | valid                        |

  Scenario Outline: Invalid parsed sidecar profile <profile> reports <message>
    When the production sidecar model handles profile "<profile>"
    Then the sidecar error contains "<message>"

    Examples:
      | profile          | message                 |
      | missing-name     | missing 'name'          |
      | missing-image    | missing 'image'         |
      | empty-name       | missing 'name'          |
      | duplicate        | duplicate sidecar name  |
      | reserved-main    | reserved container name |
      | reserved-helper  | reserved container name |
      | unknown-field    | bogusField              |
      | invalid-yaml     | parsing sidecar config  |

  Scenario: A numeric sidecar source is rejected
    When the production sidecar model handles profile "source-invalid"
    Then the sidecar error contains "must be a string"

  Scenario Outline: Invalid direct validation profile <profile> reports <message>
    When the production sidecar model handles profile "<profile>"
    Then the sidecar error contains "<message>"

    Examples:
      | profile                   | message               |
      | validate-empty-name       | missing 'name'        |
      | validate-empty-image      | missing 'image'       |
      | validate-invalid-protocol | invalid port protocol |
