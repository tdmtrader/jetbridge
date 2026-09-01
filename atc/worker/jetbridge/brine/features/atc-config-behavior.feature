Feature: Core ATC configuration types preserve their public transformations

  Source: all 19 specs in atc/config_test.go. The scenarios call production
  ImageForType, JSON marshal/unmarshal, and var-source dependency ordering with
  concrete configuration values and no doubles.

  Scenario Outline: ATC config profile <profile>
    Given the production ATC config evaluates profile "<profile>"
    Then the ATC config observation <comparison> "<expected>"

    Examples:
      | profile                               | comparison | expected                                                       |
      | type-image/direct                     | is         | ref=my-registry/custom-git:latest;base=;privileged=true;get=false;check=false |
      | type-image/source-plans               | is         | ref=;get=true;check=true                                        |
      | type-image/registry-skip-download     | is         | skip-download=true                                             |
      | type-image/non-registry-download      | is         | skip-download=false                                            |
      | type-image/resolved-digest            | is         | ref=my-registry/custom-git@sha256:abc123;base=;get=false;check=false |
      | type-image/base                       | is         | ref=;base=git;get=false;check=false                            |
      | version/string-values                 | is         | some=version;other=8                                            |
      | version/non-string                    | is         | error:the value 8 of some is not a string                       |
      | var-sources/ideal                     | is         | vs1,vs2,vs3,vs4,vs5                                            |
      | var-sources/random                    | is         | vs2,vs4,vs1,vs3,vs5                                            |
      | var-sources/unresolved                | is         | error:could not resolve inter-dependent var sources: vs5, vs3  |
      | var-sources/cyclic                    | is         | error:could not resolve inter-dependent var sources: vs1, vs5, vs3 |
      | check-every/unmarshal-never           | is         | never=true;interval=0s                                         |
      | check-every/unmarshal-duration        | is         | never=false;interval=10s                                       |
      | check-every/unmarshal-invalid-duration | contains  | invalid duration                                               |
      | check-every/unmarshal-non-string      | is         | error:non-string value provided                                 |
      | check-every/marshal-never             | is         | never                                                          |
      | check-every/marshal-duration          | is         | 10s                                                            |
      | check-every/marshal-empty             | is         |                                                                |
