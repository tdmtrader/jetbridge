Feature: Production OpenTelemetry instruments export the recorded measurements

  Source: 31 of 32 specs across atc/metric/otel_metrics_test.go,
  otel_artifact_upload_test.go, otel_build_lifecycle_test.go,
  otel_db_checks_test.go, otel_scheduling_test.go,
  otel_step_duration_test.go, otel_step_waiting_test.go, and otel_gc_test.go.
  Each row installs a real OTel SDK MeterProvider and ManualReader, initializes
  the production metric family, records through the production function, and
  validates the exported name, value, and relevant attributes. The sole
  omitted spec is artifact upload's "not initialized" no-panic check: package
  instruments cannot be reset to nil, and the source itself notes that it does
  not actually establish the claimed nil-guard state.

  Scenario Outline: OTel profile <profile> exports its production measurement
    When production OpenTelemetry records profile "<profile>"
    Then the OpenTelemetry result is "recorded=true"

    Examples:
      | profile                    |
      | core-build-duration        |
      | core-http-duration         |
      | core-pod-startup           |
      | core-containers-created    |
      | core-volumes-created       |
      | core-volume-operation      |
      | core-volume-operations     |
      | artifact-duration          |
      | artifact-size              |
      | artifact-files             |
      | artifact-phases            |
      | artifact-attributes        |
      | lifecycle-builds-started   |
      | lifecycle-builds-running   |
      | lifecycle-build-finished   |
      | lifecycle-checks-started   |
      | lifecycle-checks-running   |
      | db-queries                 |
      | db-connections             |
      | checks-started             |
      | checks-finished            |
      | checks-enqueued            |
      | scheduling-scheduled       |
      | scheduling-running         |
      | scheduling-duration        |
      | step-duration              |
      | step-duration-attributes   |
      | waiting-count              |
      | waiting-duration           |
      | gc-duration                |
      | gc-attributes              |
