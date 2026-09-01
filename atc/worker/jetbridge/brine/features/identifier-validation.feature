Feature: Pipeline identifiers report compatibility warnings and hard errors

  Source: all 14 dynamically registered specs in atc/configwarning_test.go.
  Every identifier and context is passed to production ValidateIdentifier.

  Scenario: A lowercase-letter identifier is accepted
    When the production identifier validator handles profile "letter"
    Then the exact identifier result is "warning=<nil>;error=<nil>"

  Scenario: A multilingual identifier is accepted
    When the production identifier validator handles profile "multilingual"
    Then the exact identifier result is "warning=<nil>;error=<nil>"

  Scenario: An identifier containing an underscore is accepted
    When the production identifier validator handles profile "underscore"
    Then the exact identifier result is "warning=<nil>;error=<nil>"

  Scenario: An identifier starting with a number is accepted
    When the production identifier validator handles profile "number"
    Then the exact identifier result is "warning=<nil>;error=<nil>"

  Scenario: A numeric identifier containing an underscore is accepted
    When the production identifier validator handles profile "number-underscore"
    Then the exact identifier result is "warning=<nil>;error=<nil>"

  Scenario: An identifier starting with a hyphen reports the start warning
    When the production identifier validator handles profile "hyphen"
    Then the exact identifier result is "warning=invalid_identifier:: '-something' is not a valid identifier: must start with a lowercase letter or a number;error=<nil>"

  Scenario: An identifier starting with a period reports the start warning
    When the production identifier validator handles profile "period"
    Then the exact identifier result is "warning=invalid_identifier:: '.something' is not a valid identifier: must start with a lowercase letter or a number;error=<nil>"

  Scenario: An identifier starting uppercase reports the start warning
    When the production identifier validator handles profile "uppercase-start"
    Then the exact identifier result is "warning=invalid_identifier:: 'Something' is not a valid identifier: must start with a lowercase letter or a number;error=<nil>"

  Scenario: An identifier containing a space reports the illegal character
    When the production identifier validator handles profile "space"
    Then the exact identifier result is "warning=invalid_identifier:: 'some thing' is not a valid identifier: illegal character ' ';error=<nil>"

  Scenario: An identifier containing uppercase reports the illegal character
    When the production identifier validator handles profile "uppercase-inner"
    Then the exact identifier result is "warning=invalid_identifier:: 'someThing' is not a valid identifier: illegal character 'T';error=<nil>"

  Scenario: An empty identifier reports the exact hard error
    When the production identifier validator handles profile "empty"
    Then the exact identifier result is "warning=<nil>;error=: identifier cannot be an empty string"

  Scenario: An across task interpolation bypasses identifier validation
    When the production identifier validator handles profile "across-task"
    Then the exact identifier result is "warning=<nil>;error=<nil>"

  Scenario: An across set-pipeline interpolation bypasses identifier validation
    When the production identifier validator handles profile "across-pipeline"
    Then the exact identifier result is "warning=<nil>;error=<nil>"

  Scenario: An invalid identifier warning includes its context and identifier
    When the production identifier validator handles profile "warning-context"
    Then the exact identifier result is "warning=invalid_identifier:pipeline: '_something' is not a valid identifier: must start with a lowercase letter or a number;error=<nil>"
