Feature: Remaining strict pipelines API behavior crosses production boundaries

  Source: 55 exact leaves in atc/api/pipelines_test.go. Every request crosses a
  real TCP listener, the production router, authorization/accessor middleware,
  production pipeline handlers, and PostgreSQL-backed domain objects.

  Scenario Outline: The remaining pipelines API behavior is exact for <profile>
    Given the remaining production pipelines API behavior "<profile>" is exercised
    Then the remaining pipelines API behavior exactly matches "<profile>"

    Examples:
      | profile                         |
      | get-private-unauth-primary      |
      | get-private-unauth-secondary    |
      | badge-private-unauth            |
      | delete-unauth                   |
      | pause-unauth                    |
      | archive-unauth                  |
      | unpause-unauth                  |
      | expose-unauth                   |
      | hide-unauth                     |
      | order-global-unauth             |
      | order-instance-unauth           |
      | versions-unauth                 |
      | rename-unauth                   |
      | list-builds-unauth              |
      | create-build-unauth             |
      | get-private-outsider            |
      | badge-private-outsider          |
      | delete-outsider                 |
      | pause-outsider                  |
      | unpause-outsider                |
      | expose-outsider                 |
      | hide-outsider                   |
      | order-global-outsider           |
      | order-instance-outsider         |
      | rename-outsider                 |
      | create-build-outsider           |
      | global-list-json                |
      | team-list-json                  |
      | get-json                        |
      | versions-json                   |
      | list-builds-json                |
      | create-build-json               |
      | badge-public-ok                 |
      | badge-owner-ok                  |
      | pause-owner-ok                  |
      | unpause-owner-ok                |
      | expose-owner-ok                 |
      | hide-owner-ok                   |
      | order-global-owner-ok           |
      | order-instance-owner-ok         |
      | list-builds-owner-ok            |
      | create-build-owner-created      |
      | order-instance-missing-bad-request |
      | rename-missing-not-found        |
      | rename-empty-bad-request        |
      | rename-success-state            |
      | rename-invalid-warning          |
      | badge-owner-headers             |
      | badge-unknown-body              |
      | badge-success-body              |
      | badge-aborted-body              |
      | badge-errored-body              |
      | badge-failed-body               |
      | list-builds-public-ok           |
      | list-builds-bounded-ok          |
