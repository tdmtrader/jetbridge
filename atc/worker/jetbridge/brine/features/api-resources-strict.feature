Feature: Resource API behavior through real HTTP and PostgreSQL

  Scenario: Global resources include authorized private and public persisted state
    Given the production resources API executes profile "all-auth"
    Then the resources API observation exactly matches profile "all-auth"

  Scenario: Anonymous global resources include only exposed persisted state
    Given the production resources API executes profile "all-anon"
    Then the resources API observation exactly matches profile "all-anon"

  Scenario: Administrators receive every persisted resource
    Given the production resources API executes profile "all-admin"
    Then the resources API observation exactly matches profile "all-admin"

  Scenario: Pipeline resources serialize exact persisted check state
    Given the production resources API executes profile "pipeline-resources"
    Then the resources API observation exactly matches profile "pipeline-resources"

  Scenario: Pipeline resource types serialize exact persisted configuration
    Given the production resources API executes profile "resource-types"
    Then the resources API observation exactly matches profile "resource-types"

  Scenario: Resource detail returns persisted check and build state
    Given the production resources API executes profile "get-details"
    Then the resources API observation exactly matches profile "get-details"

  Scenario: Resource detail reports a pipeline-config pin
    Given the production resources API executes profile "get-config-pin"
    Then the resources API observation exactly matches profile "get-config-pin"

  Scenario: Resource detail reports an API pin and its comment
    Given the production resources API executes profile "get-api-pin"
    Then the resources API observation exactly matches profile "get-api-pin"

  Scenario: Missing resource detail returns not found
    Given the production resources API executes profile "get-missing"
    Then the resources API observation exactly matches profile "get-missing"

  Scenario: Unpin removes the persisted API pin
    Given the production resources API executes profile "unpin-success"
    Then the resources API observation exactly matches profile "unpin-success"

  Scenario: Unpin without a persisted pin reports a server error
    Given the production resources API executes profile "unpin-empty"
    Then the resources API observation exactly matches profile "unpin-empty"

  Scenario: Pin comment is persisted without changing the pin
    Given the production resources API executes profile "comment-success"
    Then the resources API observation exactly matches profile "comment-success"

  Scenario: Malformed pin comment JSON returns bad request
    Given the production resources API executes profile "comment-malformed"
    Then the resources API observation exactly matches profile "comment-malformed"

  Scenario: Version-scoped cache deletion preserves the decoy association
    Given the production resources API executes profile "cache-version"
    Then the resources API observation exactly matches profile "cache-version"

  Scenario: Unscoped cache deletion removes every persisted association
    Given the production resources API executes profile "cache-all"
    Then the resources API observation exactly matches profile "cache-all"

  Scenario: Cache deletion preserves malformed and missing statuses
    Given the production resources API executes profile "cache-malformed-missing"
    Then the resources API observation exactly matches profile "cache-malformed-missing"
