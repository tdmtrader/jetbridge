Feature: Concrete production credential value evaluation

  Scenario Outline: Production credential value behavior — <profile>
    Given the production credential value profile "<profile>" is exercised
    Then the credential value observation exactly matches "<profile>"

    Examples:
      | profile            |
      | string-interpolate |
      | string-error       |
      | string-plain       |
      | list-whole         |
      | list-element       |
      | source             |
