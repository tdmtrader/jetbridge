Feature: Remaining versions API behavior over real HTTP and PostgreSQL

  Scenario Outline: Remaining production versions API behavior — <profile>
    Given the production versions API behavior "<profile>" is exercised
    Then the versions API behavior exactly matches "<profile>"

    Examples:
      | profile                             |
      | list-empty-status                   |
      | list-public-status                  |
      | list-authorized-status              |
      | list-public-content-type            |
      | list-authorized-content-type        |
      | list-resource-not-found             |
      | list-private-metadata-authenticated |
      | enable-finds-resource               |
      | enable-status                       |
      | enable-resource-not-found           |
      | disable-finds-resource              |
      | disable-status                      |
      | disable-resource-not-found          |
      | pin-status                          |
      | pin-resource-not-found              |
      | clear-resource-status               |
      | clear-resource-content-type         |
      | clear-resource-not-found            |
      | clear-type-status                   |
      | clear-type-content-type             |
      | clear-type-not-found                |
