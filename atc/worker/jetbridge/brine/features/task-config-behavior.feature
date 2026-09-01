Feature: Task configuration decoding preserves the public YAML contract

  Source: all 37 specs in atc/task_test.go. Every row calls NewTaskConfig,
  TaskConfig.Validate, or ImageResource.ApplySourceDefaults directly. Values
  below are the decoded user-visible configuration or validation diagnostic;
  there are no decoder or resource-type doubles.

  Scenario Outline: Task config profile <profile>
    Given the production task config evaluates profile "<profile>"
    Then the task config observation <comparison> "<expected>"

    Examples:
      | profile                      | comparison | expected                                                                                         |
      | decode/basic                 | is         | platform=beos;path=a/file                                                                        |
      | params/bool                  | is         | true                                                                                             |
      | params/int                   | is         | 1059262                                                                                          |
      | params/large-int             | is         | 18446744073709551615                                                                             |
      | params/unquoted-scientific   | is         | 18446744000000000000                                                                             |
      | params/quoted-scientific     | is         | 1.8446744e+19                                                                                    |
      | params/float                 | is         | 1059262.123123123                                                                                |
      | params/map                   | is         | eyJmb28iOiJiYXIifQ                                                                                |
      | params/empty                 | is         |                                                                                                  |
      | params/numeric-environment   | is         | platform=beos;FOO=1                                                                              |
      | decode/unknown-key           | contains   | error:                                                                                           |
      | decode/invalid-input-output  | contains   | error:                                                                                           |
      | validate/missing-platform    | contains   | missing 'platform'                                                                               |
      | limits/both-with-unit        | is         | limits=cpu:1024,memory:1024;requests=nil                                                        |
      | limits/both-no-unit          | is         | limits=cpu:1024,memory:209715200;requests=nil                                                   |
      | limits/memory-only           | is         | limits=cpu:nil,memory:1024;requests=nil                                                         |
      | limits/cpu-only              | is         | limits=cpu:355,memory:nil;requests=nil                                                          |
      | limits/invalid-memory        | contains   | could not parse container memory limit                                                          |
      | limits/invalid-cpu           | contains   | cpu limit must be an integer                                                                     |
      | inputs/valid                 | is         | valid                                                                                            |
      | inputs/one-missing           | contains   | input in position 1 is missing a name                                                           |
      | inputs/two-missing           | is         | missing-input-names=1,2                                                                          |
      | outputs/valid                | is         | valid                                                                                            |
      | outputs/one-missing          | contains   | output in position 1 is missing a name                                                          |
      | outputs/two-missing          | is         | missing-output-names=1,2                                                                         |
      | requests/both                | is         | limits=nil;requests=cpu:512,memory:1073741824                                                   |
      | requests/memory-only         | is         | limits=nil;requests=cpu:nil,memory:268435456                                                    |
      | requests/with-limits         | is         | limits=cpu:2048,memory:4294967296;requests=cpu:512,memory:1073741824                            |
      | requests/without-limits      | is         | limits=nil;requests=cpu:256,memory:536870912                                                    |
      | scratch/two                  | is         | scratch=/scratch/buildkit,/tmp/workspace                                                        |
      | scratch/empty                | is         | scratch-count=0                                                                                  |
      | scratch/with-cache           | is         | cache=/tmp/cache;scratch=/scratch/work                                                          |
      | validate/missing-run         | is         | valid                                                                                            |
      | image/nil                    | is         | nil                                                                                              |
      | image/no-defaults            | is         | a=b;evaluated-value=((task-variable-name))                                                      |
      | image/base-defaults          | is         | a=b;evaluated-value=((task-variable-name));some-key=some-value                                  |
      | image/custom-type-defaults   | is         | a=b;evaluated-value=((task-variable-name));some-key=some-value                                  |
