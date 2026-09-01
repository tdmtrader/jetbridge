Feature: Token claims become the access profile seen by API handlers

  Source: the 38 non-table specs in atc/api/accessor/accessor_test.go. Together
  with access-control.feature's 52 table entries, this moves the entire
  90-spec source file. Team-backed rows use teams persisted in PostgreSQL;
  every row executes the production accessor and display-ID generator.

  Scenario Outline: The accessor exposes the profile API consumers rely on — <profile>
    Given the production accessor evaluates profile "<profile>"
    Then the accessor observation is "<observation>"

    Examples:
      | profile                            | observation                                                                                                                                                |
      | has-token/no-token                 | false                                                                                                                                                      |
      | has-token/token-present            | true                                                                                                                                                       |
      | authenticated/no-token             | false                                                                                                                                                      |
      | authenticated/invalid-token        | false                                                                                                                                                      |
      | authenticated/valid-token          | true                                                                                                                                                       |
      | authorized/no-token                | false                                                                                                                                                      |
      | authorized/invalid-token           | false                                                                                                                                                      |
      | authorized/admin-on-another-team   | true                                                                                                                                                       |
      | team-names/no-token                | none                                                                                                                                                       |
      | team-names/invalid-token           | none                                                                                                                                                       |
      | team-names/admin                   | main,team-2,team-3                                                                                                                                         |
      | team-names/viewer                  | team-1,team-2,team-3                                                                                                                                       |
      | team-names/member                  | team-1,team-2                                                                                                                                              |
      | team-names/owner                   | team-1                                                                                                                                                     |
      | admin/no-token                     | false                                                                                                                                                      |
      | admin/invalid-token                | false                                                                                                                                                      |
      | admin/non-admin-teams              | false                                                                                                                                                      |
      | admin/viewer                       | false                                                                                                                                                      |
      | admin/member                       | false                                                                                                                                                      |
      | admin/owner                        | true                                                                                                                                                       |
      | system/no-token                    | false                                                                                                                                                      |
      | system/invalid-token               | false                                                                                                                                                      |
      | system/wrong-subject               | false                                                                                                                                                      |
      | system/matching-subject            | true                                                                                                                                                       |
      | claims/no-token                    | none                                                                                                                                                       |
      | claims/invalid-token               | none                                                                                                                                                       |
      | claims/valid-token                 | sub=some-sub,name=some-name,user-id=some-id,username=some-user-name,email=some-email,connector=some-connector                                                |
      | user-info/all-fields               | sub=some-sub,name=some-name,user-id=some-id,username=some-user-name,email=some-email,admin=false,system=false,teams=none,connector=oidc,display=some-id       |
      | user-info/unconfigured-display     | some-user-name                                                                                                                                            |
      | team-roles/no-token                | none                                                                                                                                                       |
      | team-roles/invalid-token           | none                                                                                                                                                       |
      | team-roles/no-membership           | none                                                                                                                                                       |
      | team-roles/by-user-id              | team-1=owner;team-2=member;team-3=viewer                                                                                                                   |
      | team-roles/by-user-name            | team-1=owner;team-2=member;team-3=viewer                                                                                                                   |
      | team-roles/cloudfoundry-alias      | team-1=owner;team-2=member;team-3=viewer                                                                                                                   |
      | team-roles/by-group                | team-1=owner;team-2=member;team-3=viewer                                                                                                                   |
      | team-roles/multiple                | team-1=member,owner                                                                                                                                        |
      | team-roles/deduplicated            | team-1=owner                                                                                                                                               |
