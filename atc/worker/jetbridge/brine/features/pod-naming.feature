@PN-01 @PN-02 @PN-03 @PN-04 @PN-05 @PN-06 @PN-07
Feature: Naming a pod after the step that runs in it

  A pod's name is what an operator reads in `kubectl get pods` when a build is
  misbehaving. It carries the pipeline, job, build and step so the pod can be
  found without cross-referencing a UUID, and it stays inside Kubernetes' DNS
  label rules however long those names are.

  Source: k8s_runtime_behavioral_spec_20260331 — PN-01 to PN-07. Migrated
  whole from podname_test.go, which carried no requirement identifiers despite
  covering the family completely.

  Scenario Outline: A pod is named after its step — <case>
    Given a "<type>" container in pipeline "<pipeline>" job "<job>" build "<build>" step "<step>" with handle "<handle>"
    When the pod name is generated
    Then the pod name matches "<pattern>"
    And the pod name is a valid DNS label

    Examples: build steps
      | case            | type | pipeline    | job       | build | step       | handle                               | pattern                                  |
      | task            | task | my-pipeline | unit-test | 42    | run-tests  | 550e8400-e29b-41d4-a716-446655440000 | ^my-pipeline-unit-test-b99-task-[a-f0-9]{8}$ |
      | get             | get  | ci          | build     | 7     | source     | aabbccdd-1122-3344-5566-778899aabbcc | ^ci-build-b7-get-[a-f0-9]{8}$            |
      | put             | put  | ci          | build     | 7     | push-image | aabbccdd-1122-3344-5566-778899aabbcc | ^ci-build-b7-put-[a-f0-9]{8}$            |

    Examples: check containers
      | case            | type  | pipeline | job | build | step            | handle                               | pattern                        |
      | check           | check |          |     |       | my-git-resource | aabbccdd-1122-3344-5566-778899aabbcc | ^chk-my-git-resource-[a-f0-9]{8}$ |

    Examples: resource-type operations with no job context
      | case            | type | pipeline  | job | build | step           | handle                               | pattern                            |
      | get, pipeline   | get  | jetbridge |     |       | custom-time    | 10ddb92a-077e-4efd-8fd6-1c4f777fd309 | ^rt-custom-time-get-[a-f0-9]{8}$   |
      | get, step only  | get  |           |     |       | registry-image | aabbccdd-1122-3344-5566-778899aabbcc | ^rt-registry-image-get-[a-f0-9]{8}$ |

  # PN-05. Every one of these inputs would produce an invalid DNS label if
  # passed through unsanitized, which is why the label check rides every row.
  Scenario Outline: A hostile name is sanitized — <case>
    Given a "task" container in pipeline "<pipeline>" job "<job>" build "1" step "" with handle "abcdef12-0000-0000-0000-000000000000"
    When the pod name is generated
    Then the pod name is a valid DNS label
    # The raw input must not survive into the name. Asserting the whole
    # unsanitized fragment is stronger than naming one forbidden character,
    # and it is expressible for every case — a lone space cannot be written
    # in a table cell, because Gherkin trims cell edges.
    And the pod name does not contain "<raw>"

    Examples:
      | case            | pipeline     | job       | raw          |
      | uppercase       | My-Pipeline  | Unit-TEST | My-Pipeline  |
      | underscores     | my_pipe      | unit_test | my_pipe      |
      | dots            | my.pipe.line | job       | my.pipe.line |
      | spaces          | my pipe      | unit test | my pipe      |
      | punctuation     | pipe@line!   | job#1     | pipe@line!   |
      | doubled hyphens | my--pipe     | my___job  | my--pipe     |

  # PN-06. The 63-character cap is a hard Kubernetes limit; exceeding it means
  # the pod cannot be created at all.
  Scenario: A very long pipeline and job still fit in a DNS label
    Given a "task" container in pipeline "extremely-long-pipeline-name-that-goes-on-forever" job "extremely-long-job-name-that-goes-on-forever-too" build "999999" step "" with handle "abcdef12-0000-0000-0000-000000000000"
    When the pod name is generated
    Then the pod name is at most 63 characters
    And the pod name is a valid DNS label

  Scenario: Truncation does not leave a trailing hyphen
    Given a "task" container in pipeline "abcdefghijklmnopqrst-uvwxyz" job "j" build "1" step "" with handle "abcdef12-0000-0000-0000-000000000000"
    When the pod name is generated
    Then the pod name is a valid DNS label

  # PN-04. `fly execute` has no pipeline or job, so there is nothing readable
  # to build a name from and the handle is used as-is.
  Scenario Outline: Without metadata the handle is the name — <case>
    Given a "<type>" container in pipeline "" job "" build "" step "" with handle "550e8400-e29b-41d4-a716-446655440000"
    When the pod name is generated
    Then the pod name is the handle unchanged

    Examples:
      | case            | type  |
      | task, no metadata | task  |
      | check, no step  | check |
      | get, no step    | get   |

  # The suffix is what disambiguates two pods for the same step in the same
  # build. It is the first 8 hex characters of the handle with hyphens removed.
  Scenario Outline: The handle supplies a disambiguating suffix — <case>
    Given a "task" container in pipeline "p" job "j" build "1" step "" with handle "<handle>"
    When the pod name is generated
    Then the pod name ends with "<suffix>"

    Examples:
      | case              | handle                               | suffix   |
      | standard uuid     | abcdef12-3456-7890-abcd-ef1234567890 | abcdef12 |
      | hyphens stripped  | abcd-ef12-3456-7890-abcdef123456     | abcdef12 |
      | handle under 8    | abc                                  | abc      |
