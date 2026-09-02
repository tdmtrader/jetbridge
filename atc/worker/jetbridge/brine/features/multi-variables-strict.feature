Feature: Multiple concrete variable sources are searched in production order

  Scenario Outline: Multi variable profile <profile>
    Given the production multi variable profile "<profile>" is evaluated
    Then the multi variable observation is "<expected>"

    Examples:
      | profile        | expected                        |
      | no-sources     | value=nil;found=false;error=nil |
      | missing-in-all | value=nil;found=false;error=nil |
      | list           | list=a,b,b,c                    |
