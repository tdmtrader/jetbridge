Feature: SSM manager configuration detection

  Scenario Outline: Production SSM manager configuration — <profile>
    When the production SSM manager configuration profile "<profile>" is exercised
    Then the SSM manager configuration observation exactly matches "<profile>"

    Examples:
      | profile             |
      | empty-unconfigured  |
      | region-configured   |
