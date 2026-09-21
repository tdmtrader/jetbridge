Feature: A template selects one stable task output for each Run result

  @core-review
  Scenario Outline: Invalid result declarations fail before a Run is allocated
    Given a Run result declaration with "<shape>"
    When the template author validates the result declaration
    Then the result declaration is refused with "<reason>"

    Examples:
      | shape | reason |
      | no task identity | task_id |
      | malformed task identity | canonical UUID |
      | uppercase task identity | canonical UUID |
      | zero task identity | canonical UUID |
      | repeated task identity | duplicated |
      | repeated result name | duplicated |
      | missing output | declared output |
      | duplicate output declaration | declared output |
      | file task | inline |
      | across producer | repeated |
      | retried producer | repeated |
      | retried enclosing group | repeated |
      | empty result name | result name |
      | path result name | result name |
      | unknown result field | unknown field |
      | ordinary pipeline | only valid on templates |
      | unreachable producer | expected work |
      | too many results | 64 |

  @core-review
  Scenario Outline: A valid declaration survives materialization and planning
    Given a Run result declaration with "<shape>"
    When the template author validates the result declaration
    Then materialization and planning preserve the stable task and selected output

    Examples:
      | shape |
      | one inline producer |
      | renamed producer |
      | a producer in a success hook |
      | a producer in a parallel group |

  @core-review
  Scenario: A saved task identity belongs to its original base template
    Given a Run result declaration with "one inline producer"
    When two base templates try to save the same stable task identity
    Then the second template is refused and the original declaration is intact

  @core-review
  Scenario: Result declarations cannot fall back to legacy Run execution
    Given a Run result declaration with "one inline producer"
    When the existing Run creator receives the result-bearing template
    Then Run result execution is held without allocating a Run or number
    And the admission refusal is returned to clients as a conflict
