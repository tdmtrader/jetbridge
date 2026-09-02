Feature: Versions API behavior over real HTTP and PostgreSQL

  Scenario Outline: Production versions API behavior — <profile>
    Given the production versions API behavior "<profile>" is exercised
    Then the versions API behavior exactly matches "<profile>"

    Examples:
      | profile               |
      | list-filter-all       |
      | list-filter-space     |
      | list-filter-percent   |
      | list-filter-colon     |
      | list-filter-invalid   |
      | list-default-limit    |
      | list-json-metadata    |
      | enable-exact          |
      | disable-exact         |
      | pin-exact             |
      | pin-success           |
      | clear-resource-count  |
      | clear-resource-target |
      | clear-resource-scope  |
      | clear-type-count      |
      | clear-type-target     |
      | clear-type-scope      |
      | list-private-metadata-anonymous |
