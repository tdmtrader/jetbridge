Feature: RunState stores and scopes production build values

  Source: 25 non-scripted leaves in atc/exec/run_state_test.go. The four
  Run leaves remain in Ginkgo because they assert a scripted Step's return
  values or recorded invocation. Every scenario below calls the production
  RunState, buildVariables, variable trackers, result map, and artifact
  repository directly with concrete values.

  Scenario Outline: RunState profile <profile> has observation <observation>
    When production RunState evaluates profile "<profile>"
    Then the RunState observation is "<observation>"

    Examples:
      | profile                         | observation                                      |
      | result-missing-found            | found=false                                      |
      | result-missing-preserves        | destination=42                                   |
      | result-other-found              | found=false                                      |
      | result-other-preserves          | destination=42                                   |
      | result-present-found            | found=true                                       |
      | result-present-mutates          | destination=123                                  |
      | result-wrong-type-found         | found=false                                      |
      | result-wrong-type-preserves     | destination=42                                   |
      | get-credential                  | error=false;found=true;value=v1                  |
      | get-missing-local-field         | error=true                                       |
      | get-tracked-credentials         | k1=v1;k2=v2;k3-absent=true                       |
      | list-credentials                | error=false;refs=:k1,:k2,:k3                    |
      | list-with-locals                | error=false;refs=.:l1,.:l2,:k1,:k2,:k3          |
      | local-redacted-get              | error=false;found=true;value=bar                 |
      | local-redacted-tracked          | foo=bar                                          |
      | scope-parent                    | same=true                                        |
      | scope-parent-local              | value=world                                      |
      | scope-child-isolated            | found=false                                      |
      | scope-shared-credential         | value=v1                                         |
      | scope-late-parent-local         | value=world                                      |
      | scope-child-shadows             | value=2                                          |
      | scope-parent-result-in-child    | destination=hello                                |
      | scope-child-result-in-parent    | destination=hello                                |
      | scope-artifact-parent           | same=true                                        |
      | scope-tracked-child-preferred   | a=from child                                     |
