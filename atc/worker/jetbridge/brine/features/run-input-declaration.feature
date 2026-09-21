Feature: A Run input names exact stable task slots

  @core-review
  Scenario Outline: Invalid input routes fail template validation
    Given a Run input declaration with "<shape>"
    When the template author validates the result declaration
    Then the result declaration is refused with "<reason>"

    Examples:
      | shape | reason |
      | no task identity | task_id |
      | missing input | declared input |
      | duplicate input declaration | declared input |
      | duplicate route | duplicated |
      | conflicting slot | duplicated |
      | empty input name | input name |
      | path input name | input name |
      | unknown input field | unknown field |
      | file task | inline |
      | across target | repeated |
      | retried target | repeated |
      | unreachable target | expected work |
      | ordinary pipeline | only valid on templates |
      | too many input names | 64 |
      | too many routes | 256 |
      | ambient input mapping | input_mapping |
      | output collision | overlap |
      | nested output collision | overlap |
      | cache collision | overlap |
      | scratch collision | overlap |
      | ambient input collision | overlap |
      | escaping input path | relative |
      | interpolated input path | literal |

  @core-review
  Scenario Outline: Named inputs retain their routes through execution planning
    Given a Run input declaration with "<shape>"
    When the template author validates the result declaration
    Then the execution plan retains every named Run input route

    Examples:
      | shape |
      | one inline target |
      | renamed target |
      | two distinct slots |
      | two task targets |
      | separate input path |

  @core-review
  Scenario: Input mappings cannot bypass the Run activation hold
    Given a Run input declaration with "one inline target"
    When the existing Run creator receives the result-bearing template
    Then Run result execution is held without allocating a Run or number
