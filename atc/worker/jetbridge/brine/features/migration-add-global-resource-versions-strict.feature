Feature: Add global resource versions database migration

  Scenario Outline: Production add-global-resource-versions migration — <profile>
    When the production add-global-resource-versions migration profile "<profile>" is exercised
    Then the add-global-resource-versions migration observation exactly matches "<profile>"

    Examples:
      | profile             |
      | up-disabled         |
      | up-build-inputs     |
      | up-build-outputs    |
      | down-all-versions   |
