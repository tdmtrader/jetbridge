Feature: Named concrete variable sources preserve qualification semantics

  Scenario Outline: Named variable profile <profile>
    Given the production named variable profile "<profile>" is evaluated
    Then the named variable observation is "<expected>"

    Examples:
      | profile        | expected                                      |
      | no-sources     | value=nil;found=false;error=nil               |
      | missing-source | error:missing source 's3' in var: s3:foo     |
      | no-source-name | value=nil;found=false;error=nil               |
      | list           | list=s1:a,s1:b,s2:b,s2:c                      |
