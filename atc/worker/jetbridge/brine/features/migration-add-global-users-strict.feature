Feature: Add global users database migration

  Scenario Outline: Production add-global-users migration — <profile>
    When the production add-global-users migration profile "<profile>" is exercised
    Then the add-global-users migration observation exactly matches "<profile>"

    Examples:
      | profile                         |
      | up-github                       |
      | up-basic                        |
      | up-uaa                          |
      | up-gitlab                       |
      | up-oauth                        |
      | up-oidc                         |
      | reject-bitbucket-cloud          |
      | reject-bitbucket-server         |
      | reject-uaa-provider-conflict    |
      | reject-duplicate-basic-user     |
      | down-main-only-changed          |
      | down-non-main-changed           |
