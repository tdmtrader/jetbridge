Feature: Container limits preserve binary-unit and JSON behavior

  Source: all 15 leaf specs in atc/container_limits_test.go.
  Every row calls the production atc parser or JSON methods with the original
  concrete input and observes the same value, error, equality, or omission.

  Scenario Outline: Strict container limit profile <profile>
    Given the production container limits evaluate profile "<profile>"
    Then the strict container limits observation is "<observation>"

    Examples:
      | profile | observation |
      | memory/plain | value=1024 |
      | memory/kb | value=1024 |
      | memory/mb | value=1048576 |
      | memory/gb | value=1073741824 |
      | memory/kib | value=1024 |
      | memory/mib | value=1048576 |
      | memory/gib | value=1073741824 |
      | memory/case-unit | equal=true;lower=none;upper=none |
      | memory/case-prefix | equal=true;lower=none;upper=none |
      | memory/invalid | error:could not parse container memory limit |
      | memory/single-prefixes | K=value=1024;M=value=1048576;G=value=1073741824 |
      | ephemeral/numeric | value=1073741824 |
      | ephemeral/5g | value=5368709120 |
      | ephemeral/2gib | value=2147483648 |
      | ephemeral/omit-nil | {} |
