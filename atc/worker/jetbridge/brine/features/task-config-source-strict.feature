Feature: Task configuration sources transform concrete task configuration

  Source: the production-backed subset of atc/exec/task_config_source_test.go.
  These scenarios call the concrete TaskConfigSource implementations with a
  production logger, repository, task configuration, and variable resolver.
  Call-record assertions and injected-error collaborators are excluded.

  Scenario Outline: Task config source profile <profile> produces its exact observable result
    When the production task config source evaluates profile "<profile>"
    Then the task config source observation is "<observation>"

    Examples:
      | profile                         | observation                                      |
      | static/nil                      | error=nil;zero=true                              |
      | params/no-override-config       | same=true                                        |
      | params/override-values          | params=EXTRA_PARAM:C,ORIG_PARAM:D,PARAM:B        |
      | params/override-warning         | warnings=EXTRA_PARAM-missing                     |
      | limits/new-success              | error=nil                                        |
      | limits/new-values               | limits=cpu:2048,memory:209715200                 |
      | limits/existing-values          | limits=cpu:2048,memory:209715200                 |
      | requests/new-values             | limits=cpu:1024,memory:209715200;requests=cpu:512,memory:1073741824 |
      | limits-and-requests-values       | limits=cpu:2048,memory:209715200;requests=cpu:256,memory:nil |
      | requests-empty-values           | limits=nil;requests=cpu:nil,memory:536870912     |
      | validating/valid                | error=nil;same=true                              |
      | validating/invalid              | error=validation                                 |
      | interpolate/all-success         | error=nil                                        |
      | interpolate/all-values          | args=-al,task-variable-value;params=key1-task-variable-value,key2-task-variable-value;source=task-variable-value |
      | interpolate/missing-success      | error=nil                                        |
      | interpolate/missing-values       | args=-al,((task-variable-name));params=key1-((task-variable-name)),key2-((task-variable-name));source=((task-variable-name)) |
      | defaults/base                   | error=nil;default=some-value                     |
      | defaults/custom                 | error=nil;default=some-value                     |
