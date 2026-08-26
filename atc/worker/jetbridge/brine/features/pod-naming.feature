@PN-01 @PN-02 @PN-03 @PN-04 @PN-05 @PN-06 @PN-07
Feature: Naming a pod after the step that runs in it

  A pod's name is what an operator reads in `kubectl get pods` when a build is
  misbehaving. It carries the pipeline, job, build and step so the pod can be
  found without cross-referencing a UUID, and it stays inside Kubernetes' DNS
  label rules however long those names are.

  Source: k8s_runtime_behavioral_spec_20260331 — PN-01 to PN-07. Migrated
  from podname_test.go, which carried no requirement identifiers despite
  covering the family completely. The per-segment cap and the chk- truncation
  rule were absent until a review found them missing; they are the last two
  scenarios in this file.

  Scenario Outline: A pod is named after its step — <case>
    Given a "<type>" container in pipeline "<pipeline>" job "<job>" build "<build>" step "<step>" with handle "<handle>"
    When the pod name is generated
    Then the pod name matches "<pattern>"
    And the pod name is a valid DNS label

    Examples: build steps
      | case            | type | pipeline    | job       | build | step       | handle                               | pattern                                  |
      | task            | task | my-pipeline | unit-test | 42    | run-tests  | 550e8400-e29b-41d4-a716-446655440000 | ^my-pipeline-unit-test-b42-task-[a-f0-9]{8}$ |
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
    # The positive form. Negative assertions alone let a mutation DELETE a
    # separator instead of hyphenating it — "my_pipe" becomes "mypipe", still a
    # valid label and still wrong.
    And the pod name reads "<reads>"

    Examples:
      | case            | pipeline     | job       | raw          | reads        |
      | uppercase       | My-Pipeline  | Unit-TEST | My-Pipeline  | my-pipeline  |
      | underscores     | my_pipe      | unit_test | my_pipe      | my-pipe      |
      | dots            | my.pipe.line | job       | my.pipe.line | my-pipe-line |
      | spaces          | my pipe      | unit test | my pipe      | my-pipe      |
      | punctuation     | pipe@line!   | job#1     | pipe@line!   | pipeline     |
      | doubled hyphens | my--pipe     | my___job  | my--pipe     | my-pipe      |
      | edge separators | _my-pipe_    | unit_test | _my-pipe_    | my-pipe      |

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

  # PN-06's per-segment cap, distinct from the 63-character total. Without it a
  # single very long pipeline name would consume the whole budget and leave no
  # room for the job — the name would still be a valid label and would still be
  # useless for finding the pod.
  @PN-06
  Scenario: One long segment cannot crowd out the others
    Given a "task" container in pipeline "this-is-a-very-long-pipeline-name-that-exceeds" job "this-is-a-very-long-job-name-that-exceeds-too" build "1" step "" with handle "abcdef12-0000-0000-0000-000000000000"
    When the pod name is generated
    Then the pod name is a valid DNS label
    And the pod name still identifies its job

  # PN-02 with a resource name long enough to need truncating. A check pod is
  # named after its resource, and resource names are not bounded.
  @PN-02 @PN-06
  Scenario: A check on a very long resource name still fits
    Given a "check" container in pipeline "" job "" build "" step "extremely-long-resource-name-that-goes-on-forever-and-ever" with handle "aabbccdd-1122-3344-5566-778899aabbcc"
    When the pod name is generated
    Then the pod name is at most 63 characters
    And the pod name is a valid DNS label
    And the pod name matches "^chk-"

  # An untyped container still has to be findable. "task" is the default and
  # it is what an operator greps for; a different default silently breaks that
  # habit for every container Concourse fails to type.
  Scenario: A container with no recorded type is named as a task
    Given a "" container in pipeline "p" job "j" build "1" step "" with handle "abcdef12-0000-0000-0000-000000000000"
    When the pod name is generated
    Then the pod name matches "^p-j-b1-task-[a-f0-9]{8}$"
    And the pod name is a valid DNS label

  # The 63-character cap has to hold for every input, not just long pipeline
  # and job names. Pipeline and job SHARE the remaining budget; a deletion
  # probe gave each of them the whole of it and the suite stayed green,
  # because no scenario made the fixed part big enough for the split to
  # matter. With a long build number it produces a 76-character name, which
  # Kubernetes rejects outright.
  Scenario: A long build number cannot push the pod name past the DNS limit
    Given a "task" container in pipeline "extremely-long-pipeline-name" job "extremely-long-job-name" build "1234567890123456789" step "" with handle "abcdef12-0000-0000-0000-000000000000"
    When the pod name is generated
    Then the pod name is at most 63 characters
    And the pod name is a valid DNS label
