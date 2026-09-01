Feature: Pipeline references preserve instance variables across display and HTTP

  Source: all 19 table entries in atc/pipeline_test.go. Each row calls the
  production PipelineRef String or QueryParams method, or the production query
  parser, with the same concrete input and expected value as the source spec.

  Scenario Outline: Strict pipeline reference profile <profile>
    When the strict production pipeline reference handles profile "<profile>"
    Then the strict pipeline reference matches the original expectation

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
