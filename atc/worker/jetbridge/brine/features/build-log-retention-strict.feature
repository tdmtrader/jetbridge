Feature: Build log retention combines job, default, and maximum policy

  Scenario Outline: Build log retention profile <profile>
    Given the production build log retention profile "<profile>" is calculated
    Then the build log retention is "<expected>"

    Examples:
      | profile                    | expected             |
      | nothing                    | builds=0;days=0;min=0 |
      | job-only                   | builds=3;days=2;min=1 |
      | defaults                   | builds=5;days=4;min=0 |
      | job-over-defaults          | builds=6;days=3;min=0 |
      | max-lower                  | builds=6;days=6;min=0 |
      | max-only                   | builds=4;days=3;min=0 |
      | mixed-max                  | builds=4;days=2;min=0 |
      | min-equals-builds          | builds=5;days=3;min=5 |
      | min-greater-than-builds    | builds=5;days=3;min=0 |
      | only-max-builds-with-job   | builds=5;days=7;min=0 |
      | only-max-days-with-job     | builds=7;days=5;min=0 |
