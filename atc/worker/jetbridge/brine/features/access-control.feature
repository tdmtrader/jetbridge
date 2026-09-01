Feature: A persisted team role decides whether a user may act

  These 52 rows are the 52 source entries in
  atc/api/accessor/accessor_test.go:228-379 and :776-788. They use the
  production accessor, production display-ID generator, and (for role checks)
  a team read back from real PostgreSQL. One outline replaces the repeated
  user/group matrices without replacing any collaborator with a fake.

  Scenario Outline: A user role has the expected authority — <actual> for <required>
    Given a real team grants its "<actual>" role to user brine-user
    When the user attempts an action requiring "<required>"
    Then the real accessor says the request is "<decision>"

    Examples:
      | required          | actual            | decision   |
      | viewer            | viewer            | authorized |
      | viewer            | pipeline-operator | authorized |
      | viewer            | member            | authorized |
      | viewer            | owner             | authorized |
      | pipeline-operator | viewer            | denied     |
      | pipeline-operator | pipeline-operator | authorized |
      | pipeline-operator | member            | authorized |
      | pipeline-operator | owner             | authorized |
      | member            | viewer            | denied     |
      | member            | pipeline-operator | denied     |
      | member            | member            | authorized |
      | member            | owner             | authorized |
      | owner             | viewer            | denied     |
      | owner             | pipeline-operator | denied     |
      | owner             | member            | denied     |
      | owner             | owner             | authorized |

  Scenario Outline: A group role has the expected authority — <actual> for <required>
    Given a real team grants its "<actual>" role to group brine-group
    When the user attempts an action requiring "<required>"
    Then the real accessor says the request is "<decision>"

    Examples:
      | required          | actual            | decision   |
      | viewer            | viewer            | authorized |
      | viewer            | pipeline-operator | authorized |
      | viewer            | member            | authorized |
      | viewer            | owner             | authorized |
      | pipeline-operator | viewer            | denied     |
      | pipeline-operator | pipeline-operator | authorized |
      | pipeline-operator | member            | authorized |
      | pipeline-operator | owner             | authorized |
      | member            | viewer            | denied     |
      | member            | pipeline-operator | denied     |
      | member            | member            | authorized |
      | member            | owner             | authorized |
      | owner             | viewer            | denied     |
      | owner             | pipeline-operator | denied     |
      | owner             | member            | denied     |
      | owner             | owner             | authorized |

  Scenario Outline: User and group grants combine — <user>/<group> for <required>
    Given a real team grants user role "<user>" and group role "<group>"
    When the user attempts an action requiring "<required>"
    Then the real accessor says the request is "<decision>"

    Examples:
      | required          | user   | group  | decision   |
      | owner             | member | viewer | denied     |
      | owner             | viewer | member | denied     |
      | owner             | member | member | denied     |
      | owner             | viewer | viewer | denied     |
      | member            | member | viewer | authorized |
      | member            | viewer | member | authorized |
      | member            | member | member | authorized |
      | member            | viewer | viewer | denied     |
      | pipeline-operator | member | viewer | authorized |
      | pipeline-operator | viewer | member | authorized |
      | pipeline-operator | member | member | authorized |
      | pipeline-operator | viewer | viewer | denied     |
      | viewer            | member | viewer | authorized |
      | viewer            | viewer | member | authorized |
      | viewer            | member | member | authorized |
      | viewer            | viewer | viewer | authorized |

  Scenario Outline: The configured OIDC claim is used as the displayed identity — <field>
    Given an OIDC user whose display field is "<field>"
    Then the displayed identity is "<identity>"

    Examples:
      | field    | identity       |
      | user_id  | some-id        |
      | name     | some-name      |
      | username | some-user-name |
      | email    | some-email     |
