Feature: Pipeline references preserve instance variables across display and HTTP

  Source: all 19 dynamically registered specs in atc/pipeline_test.go. Every
  original table case executes the production String, QueryParams, or parser.

  Scenario Outline: Pipeline reference profile <profile> is verified
    When the production pipeline reference handles profile "<profile>"
    Then the pipeline reference result is "verified"

    Examples:
      | profile            |
      | string-simple      |
      | string-instance    |
      | string-sorted      |
      | string-special     |
      | string-yaml        |
      | string-primitives  |
      | query-empty        |
      | query-simple       |
      | query-nested       |
      | query-quoted       |
      | parse-empty        |
      | parse-simple       |
      | parse-complex      |
      | parse-json         |
      | parse-root         |
      | parse-root-subvars |
      | parse-ignore       |
      | parse-invalid-ref  |
      | parse-invalid-json |
