Feature: The Go Concourse client manages pipeline configuration through the real API

  Source: 20 initial specs. The original 10 are in
  go-concourse/concourse/configs_test.go. The production API round trips also
  replace 10 atc/api/config_test.go specs: existing read status/header/body
  (:490/:494/:502), valid instance lookup (:473), missing pipeline (:538),
  update 200 (:977), first create 201 (:1382), valid instanced save (:1485),
  and credential validation success/200 (:1095/:1101). Together they cover
  ordinary/instanced reads, create/update, credential and instance-vars query
  encoding, version headers, missing configs, and validation errors.

  Scenario Outline: Reading an existing <kind> config returns its version and job
    Given the production Go config client, real API, and PostgreSQL
    And the config client uses an "<kind>" reference
    And the real pipeline config already exists
    When the Go client reads the pipeline config
    Then the Go config client found the config
    And the Go config client returned 1 job(s)
    And the Go config client returned a nonempty version
    And the Go config client returned no error

    Examples:
      | kind      |
      | ordinary  |
      | instanced |

  Scenario: Reading a missing config returns not found without an error
    Given the production Go config client, real API, and PostgreSQL
    When the Go client reads the pipeline config
    Then the Go config client did not find the config
    And the Go config client returned no error

  Scenario Outline: Creating a <kind> config persists through the production handler
    Given the production Go config client, real API, and PostgreSQL
    And the config client uses an "<reference>" reference
    When the Go client saves a "create" pipeline config with credential checking "<check>"
    Then the Go config client reported "created=true;updated=false;warnings=0"
    And the Go config client returned no error

    Examples:
      | kind      | reference | check    |
      | ordinary  | ordinary  | disabled |
      | instanced | instanced | disabled |
      | checked   | ordinary  | enabled  |

  Scenario Outline: Updating a <kind> config uses the real version header
    Given the production Go config client, real API, and PostgreSQL
    And the config client uses an "<reference>" reference
    When the Go client saves a "update" pipeline config with credential checking "<check>"
    Then the Go config client reported "created=false;updated=true;warnings=0"
    And the Go config client returned no error

    Examples:
      | kind      | reference | check    |
      | ordinary  | ordinary  | disabled |
      | instanced | instanced | disabled |
      | checked   | ordinary  | enabled  |

  Scenario: Invalid YAML shape becomes the client's validation error
    Given the production Go config client, real API, and PostgreSQL
    When the Go client submits an invalid pipeline config
    Then the Go config client returned a validation error
