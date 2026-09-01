Feature: Job configuration derives scheduling, inputs, and outputs

  Source: all 23 specs in atc/job_config_test.go. Scenarios call the production
  JobConfig methods directly and preserve get/put metadata without mocks.

  Scenario Outline: Max-in-flight profile <profile> returns <result>
    When the production job config handles profile "max-<profile>"
    Then the job config result is "<result>"

    Examples:
      | profile          | result |
      | raw              | 42     |
      | serial           | 1,1,1  |
      | serial-overrides | 1,1,1  |
      | default          | 0      |

  Scenario Outline: Input profile <profile> derives <inputs>
    When the production job config handles profile "input-<profile>"
    Then the job config result is "<inputs>"

    Examples:
      | profile  | inputs                                                                                                                    |
      | empty    |                                                                                                                           |
      | serial   | some-get-plan=some-get-plan[passed:a+b,trigger:true],some-other-get-plan=some-other-get-plan[trigger:false]               |
      | version  | a=a[trigger:false,version:every]                                                                                           |
      | ensure   | a=a[trigger:false],b=b[trigger:false]                                                                                      |
      | success  | a=a[trigger:false],b=b[trigger:false]                                                                                      |
      | failure  | a=a[trigger:false],b=b[trigger:false]                                                                                      |
      | abort    | a=a[trigger:false],b=b[trigger:false]                                                                                      |
      | error    | a=a[trigger:false],b=b[trigger:false]                                                                                      |
      | resource | some-get-plan=some-get-resource[trigger:false]                                                                             |
      | parallel | a=a[trigger:false],b=some-resource[passed:x,trigger:false],c=c[trigger:true]                                                 |
      | no-gets  |                                                                                                                           |

  Scenario Outline: Output profile <profile> derives <outputs>
    When the production job config handles profile "output-<profile>"
    Then the job config result is "<outputs>"

    Examples:
      | profile | outputs                 |
      | empty   |                         |
      | simple  | some-name=some-resource |
      | ensure  | a=a,b=b                 |
      | success | a=a,b=b                 |
      | failure | a=a,b=b                 |
      | abort   | a=a,b=b                 |
      | error   | a=a,b=b                 |
      | no-puts |                         |
