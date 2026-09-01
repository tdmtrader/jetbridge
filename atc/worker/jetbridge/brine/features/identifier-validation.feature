Feature: Pipeline identifiers report compatibility warnings and hard errors

  Source: all 14 dynamically registered specs in atc/configwarning_test.go.
  Every identifier and context is passed to production ValidateIdentifier.

  Scenario Outline: Identifier profile <profile> is classified as <kind>
    When the production identifier validator handles profile "<profile>"
    Then the identifier result is "<kind>"

    Examples:
      | profile           | kind    |
      | letter            | clean   |
      | multilingual      | clean   |
      | underscore        | clean   |
      | number            | clean   |
      | number-underscore | clean   |
      | hyphen            | warning |
      | period            | warning |
      | uppercase-start   | warning |
      | space             | warning |
      | uppercase-inner   | warning |
      | empty             | error   |
      | across-task       | clean   |
      | across-pipeline   | clean   |
      | warning-context   | warning |
