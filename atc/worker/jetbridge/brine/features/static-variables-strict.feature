Feature: Concrete variable maps preserve lookup and shape semantics

  Each scenario calls the production StaticVariables, KVPairs, or Reference
  implementation with concrete Go values. No variable provider or observer is
  replaced by a test double.

  Scenario Outline: Static variable profile <profile>
    Given the production static variable profile "<profile>" is evaluated
    Then the static variable observation is "<expected>"

    Examples:
      | profile                 | expected                         |
      | get-found               | value=foo;found=true;error=nil   |
      | get-missing             | value=nil;found=false;error=nil  |
      | get-local-source        | value=nil;found=false;error=nil  |
      | get-named-source        | value=nil;found=false;error=nil  |
      | get-fields              | value=foo;found=true;error=nil   |
      | get-missing-field       | error=missing-field              |
      | get-invalid-field       | error=invalid-field              |
      | list                    | list-preserved                   |
      | flatten                 | flatten-preserved                |
      | expand-simple           | expand-simple-preserved          |
      | expand-recursive        | expand-recursive-preserved       |
      | expand-overwrite-nonmap | expand-overwrite-nonmap-preserved|
      | expand-overwrite-full   | expand-overwrite-full-preserved  |
