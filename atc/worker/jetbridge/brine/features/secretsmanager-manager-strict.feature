Feature: AWS Secrets Manager production configuration detection

  Scenario Outline: Production Secrets Manager configuration — <profile>
    Given the production Secrets Manager configuration profile "<profile>" is exercised
    Then the Secrets Manager configuration observation exactly matches "<profile>"

    Examples:
      | profile            |
      | empty-unconfigured |
      | region-configured  |
