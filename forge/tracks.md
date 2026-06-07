# Tracks

> Migrated from Conductor on 2026-03-03.

## Active Tracks

---

## [x] Track: k8s-e2e CI reliability (stale-image + OOM)
*Link: [./archive/ci_reliability_k8s_e2e_20260530/](./archive/ci_reliability_k8s_e2e_20260530/)*
*Completed 2026-05-31 — source decoupled from toolchain image (build from `repo` git resource, no tag bump needed); `attempts: 2` added; validated via #181 + #102/#103. Phase 3 (digest pinning, OOM sizing) deferred as out-of-scope future hardening.*

---

## [x] Track: Resolve resource_config_scope FK-violation leak
*Link: [./archive/resource_config_scope_fk_leak_fix_20260530/](./archive/resource_config_scope_fk_leak_fix_20260530/)*
*Completed 2026-05-31 — root cause was CI image staleness (not the FK code); fixed via tag bump, behavioral #102/#103 green. Phase 3 guarded native lidar paths. Supersedes `resource_config_scope_gc_race_20260408`. Caveat: 2/3 consecutive runs; real-DB check-flow test deferred.*

---

## [x] Track: DaemonSet Artifact Security Hardening
*Link: [./archive/daemonset_artifact_security_20260408/](./archive/daemonset_artifact_security_20260408/)*
*Completed 2026-06-06 — daemon mTLS (server + ATC clients + init containers), NetworkPolicy, SecurityContext (root + CAP_DAC_OVERRIDE). Verification surfaced & fixed the incomplete ATC data plane + by-IP cert SAN/init-container bugs; green end-to-end on k8s-e2e CI #192 (plain 128/0, mTLS 10/0), now run automatically via ARTIFACT_DAEMON_TLS=true.*

---

## [x] Track: Fix resource_config_scope GC Race Condition
*Link: [./archive/resource_config_scope_gc_race_20260408/](./archive/resource_config_scope_gc_race_20260408/)*
*Completed 2026-05-31 — guard code correct; behavioral spec green (#102/#103) once image-staleness was fixed. `resource_config_scope_fk_leak_fix_20260530` continues defense-in-depth.*

---

## [x] Track: K8s E2E CI Failures
*Link: [./archive/k8s_e2e_ci_failures_20260407/](./archive/k8s_e2e_ci_failures_20260407/)*
*Completed 2026-05-31 — both jobs green (behavioral #103, integration #184); FAILURES.md updated.*

---

## Completed / Archived Tracks

---

## [x] Track: K8s Runtime Behavioral Specification
*Link: [./archive/k8s_runtime_behavioral_spec_20260331/](./archive/k8s_runtime_behavioral_spec_20260331/)*
*Completed 2026-06-07 — 87-requirement behavioral spec + coverage backfill for the JetBridge K8s runtime. All 5 phases done; coverage 83/87 Full (95%), 0 Missing (up from 62/87 at start, 73/87 mid-track). Added 14 specs (OE span events/metrics, PE/RF edge cases) + SC-11 sidecar log-stream 5s bound verified live on theborg (control 1.11s / test 6.90s). Fixed a pre-existing process_test.go goroutine-leak flake; suite 332/332 deterministic. Remaining 4 Partials (PW-08, PW-10, OE-03, CF-06) intentionally deferred as low-risk edge cases.*

### [x] Track: Fix file-config read after producer pod reap
_Link: [./archive/fix_file_config_read_after_pod_reap_20260530/](./archive/fix_file_config_read_after_pod_reap_20260530/)_
_Root cause was a test race (get pod deleted mid-fetch), not artifact routing; fixed test gating + wired daemonClient on lookup volumes. CI #184 green (128 passed, 0 failed)._

---

### [x] Track: Resource History Preservation Across Config Changes
_Link: [./archive/resource_history_preservation_20260411/](./archive/resource_history_preservation_20260411/)_

---

### [x] Track: ATC-Embedded MCP Server
_Link: [./archive/atc_embedded_mcp_server_20260408/](./archive/atc_embedded_mcp_server_20260408/)_

---

### [x] Track: Fix in-flight check tracking leak (committed fix)
_Link: [./archive/check_scheduling_inflight_leak_20260409/](./archive/check_scheduling_inflight_leak_20260409/)_

---

### [x] Track: Auth Session Lifetime & Refresh Tokens
_Link: [./archive/auth_session_refresh_tokens_20260408/](./archive/auth_session_refresh_tokens_20260408/)_

---

### [x] Track: RBAC & Pod Security Hardening
_Link: [./archive/rbac_pod_security_hardening_20260408/](./archive/rbac_pod_security_hardening_20260408/)_

---

### [x] Track: Fix gzip invalid header on artifact StreamIn
_Link: [./archive/gzip_invalid_header_artifact_streaming_20260408/](./archive/gzip_invalid_header_artifact_streaming_20260408/)_

---

### [x] Track: Stub Volume StreamOut Panic on Daemon Cache Hit
_Link: [./archive/stub_volume_streamout_panic_20260408/](./archive/stub_volume_streamout_panic_20260408/)_

---

### [x] Track: ATC Scheduler Behavioral Specification
_Link: [./archive/scheduler_behavioral_spec_20260331/](./archive/scheduler_behavioral_spec_20260331/)_

---

### [x] Track: Exec Step Behavioral Specification
_Link: [./archive/exec_step_behavioral_spec_20260331/](./archive/exec_step_behavioral_spec_20260331/)_

---

### [x] Track: Cross-Node Artifact Reliability
_Link: [./archive/cross_node_artifact_reliability_20260331/](./archive/cross_node_artifact_reliability_20260331/)_

---

### [x] Track: Batch Artifact Resolution
_Link: [./archive/batch_artifact_resolution_20260331/](./archive/batch_artifact_resolution_20260331/)_

---

### [x] Track: Check Runner Behavioral Specification
_Link: [./archive/check_runner_behavioral_spec_20260331/](./archive/check_runner_behavioral_spec_20260331/)_

---

### [x] Track: Build Tracker Behavioral Specification
_Link: [./archive/build_tracker_behavioral_spec_20260331/](./archive/build_tracker_behavioral_spec_20260331/)_

---

### [x] Track: GC Collectors Behavioral Specification
_Link: [./archive/gc_collectors_behavioral_spec_20260331/](./archive/gc_collectors_behavioral_spec_20260331/)_

---

### [x] Track: Fly CLI Behavioral Specification
_Link: [./archive/fly_cli_behavioral_spec_20260331/](./archive/fly_cli_behavioral_spec_20260331/)_

---

### [x] Track: Credential Management Behavioral Specification
_Link: [./archive/creds_behavioral_spec_20260331/](./archive/creds_behavioral_spec_20260331/)_

---

### [x] Track: Storage Backend Interface Extraction
_Link: [./archive/storage_backend_interface_20260330/](./archive/storage_backend_interface_20260330/)_

---

### [x] Track: Scratch path volumes for task containers
_Link: [./archive/scratch_path_volumes_for_task_containers_20260325/](./archive/scratch_path_volumes_for_task_containers_20260325/)_

---

### [x] Track: Cache backed by GCS Fuse
_Link: [./archive/cache_backed_by_gcs_fuse_20260324/](./archive/cache_backed_by_gcs_fuse_20260324/)_

---

### [x] Track: CI Agent OTel GenAI Tracing
_Link: [./archive/ci_agent_otel_genai_tracing_20260319/](./archive/ci_agent_otel_genai_tracing_20260319/)_

---

### [x] Track: DaemonSet Direct HostPath
_Link: [./archive/daemonset_direct_hostpath_20260327/](./archive/daemonset_direct_hostpath_20260327/)_

---

### [~] Track: Finish Setup (Conductor Migration)
_Link: [./archive/finish_setup_20260303/](./archive/finish_setup_20260303/)_

---

### [x] Track: Implementation Agent (ci-agent-implement)
_Link: [./archive/agent_can_iterate_on_a_story_given_a_spec_20260209/](./archive/agent_can_iterate_on_a_story_given_a_spec_20260209/)_

---

### [x] Track: Agent Plan from Prompt/Jira Story
_Link: [./archive/agent_can_produce_a_clear_documented_plan_from_a_promptjira_story_20260209/](./archive/agent_can_produce_a_clear_documented_plan_from_a_promptjira_story_20260209/)_

---

### [x] Track: CI Agent QA Task
_Link: [./archive/agent_can_qa_a_story_given_a_spec_20260209/](./archive/agent_can_qa_a_story_given_a_spec_20260209/)_

---

### [x] Track: Agent Fix Step
_Link: [./archive/agent_can_resolve_simple_fixes_from_the_agent_review_step_20260209/](./archive/agent_can_resolve_simple_fixes_from_the_agent_review_step_20260209/)_

---

### [x] Track: Agent Step Output Schema
_Link: [./archive/agent_step_output_schema_20260209/](./archive/agent_step_output_schema_20260209/)_

---

### [x] Track: CI Agent Review Task
_Link: [./archive/ci_agent_review_20260209/](./archive/ci_agent_review_20260209/)_

---

### [x] Track: Codebase Hardening
_Link: [./archive/codebase-hardening-20260210/](./archive/codebase-hardening-20260210/)_

---

### [x] Track: Configurable Base Resource Types
_Link: [./archive/configurable-base-resource-types/](./archive/configurable-base-resource-types/)_

---

### [x] Track: Deprecate produces: registry-image
_Link: [./archive/deprecate-produces-registry-image/](./archive/deprecate-produces-registry-image/)_

---

### [x] Track: Fix Dir Volume Bug
_Link: [./archive/dir_volume_bug_20260211/](./archive/dir_volume_bug_20260211/)_

---

### [x] Track: Fix Empty Image for Git-Backed Custom Resource Types
_Link: [./archive/fix-empty-image-git-custom-types/](./archive/fix-empty-image-git-custom-types/)_

---

### [x] Track: GCS Fuse Pod Annotation
_Link: [./archive/gcs-fuse-pod-annotation/](./archive/gcs-fuse-pod-annotation/)_

---

### [x] Track: Get Step skip_download
_Link: [./archive/get-step-skip-download/](./archive/get-step-skip-download/)_

---

### [x] Track: Helm Chart Parity
_Link: [./archive/helm-chart-parity/](./archive/helm-chart-parity/)_

---

### [x] Track: Human Feedback on Agent Reviews
_Link: [./archive/human_feedback_on_agent_reviews_20260209/](./archive/human_feedback_on_agent_reviews_20260209/)_

---

### [x] Track: Inline Sidecar Config
_Link: [./archive/inline-sidecar-config/](./archive/inline-sidecar-config/)_

---

### [x] Track: Fix Input/Output Volume Shadowing
_Link: [./archive/input-output-volume-shadowing/](./archive/input-output-volume-shadowing/)_

---

### [x] Track: Integration Test Performance
_Link: [./archive/integration_test_performance_20260226/](./archive/integration_test_performance_20260226/)_

---

### [x] Track: Isolate K8s Test Suites
_Link: [./archive/isolate-k8s-test-suites/](./archive/isolate-k8s-test-suites/)_

---

### [x] Track: K8s Behavioral Integration Tests
_Link: [./archive/k8s-behavioral-integration-tests/](./archive/k8s-behavioral-integration-tests/)_

---

### [x] Track: K8s Behavioral Test Failures
_Link: [./archive/k8s-behavioral-test-failures/](./archive/k8s-behavioral-test-failures/)_

---

### [x] Track: K8s Native Image Fetch
_Link: [./archive/k8s_native_image_fetch_20260211/](./archive/k8s_native_image_fetch_20260211/)_

---

### [x] Track: Kubernetes Spec and Sidecar
_Link: [./archive/kubernetes_spec_and_sidecar_20260209/](./archive/kubernetes_spec_and_sidecar_20260209/)_

---

### [x] Track: Legacy Cleanup
_Link: [./archive/legacy-cleanup-20260210/](./archive/legacy-cleanup-20260210/)_

---

### [x] Track: Notify-Driven Build Tracker and Scheduler Job Payload
_Link: [./archive/notify_driven_build_tracker_and_scheduler_job_payload_20260227/](./archive/notify_driven_build_tracker_and_scheduler_job_payload_20260227/)_

---

### [x] Track: Observability Hardening
_Link: [./archive/observability-hardening/](./archive/observability-hardening/)_

---

### [x] Track: OpenTelemetry
_Link: [./archive/open_telemetry_20260228/](./archive/open_telemetry_20260228/)_

---

### [x] Track: Pipeline Split — Agent Testing
_Link: [./archive/pipeline-split-agent-testing/](./archive/pipeline-split-agent-testing/)_

---

### [x] Track: Pod Leak Investigation
_Link: [./archive/pod-leak-investigation/](./archive/pod-leak-investigation/)_

---

### [x] Track: Production Readiness
_Link: [./archive/prod_ready_20260209/](./archive/prod_ready_20260209/)_

---

### [x] Track: Direct Image References for Resource Types
_Link: [./archive/resource-type-image-refs/](./archive/resource-type-image-refs/)_

---

### [x] Track: Scheduler Notify on Build Request
_Link: [./archive/scheduler_notify_on_build_request_20260226/](./archive/scheduler_notify_on_build_request_20260226/)_

---

### [x] Track: Skip Image Get on K8s
_Link: [./archive/skip-image-get-k8s/](./archive/skip-image-get-k8s/)_

---

### [x] Track: Slim Check Pods
_Link: [./archive/slim-check-pods/](./archive/slim-check-pods/)_

---

### [x] Track: Too Many Check Pods
_Link: [./archive/too_many_check_pods_20260211/](./archive/too_many_check_pods_20260211/)_

---

### [x] Track: Add safeguards against redudant checks
_Link: [./archive/add_safeguards_against_redudant_checks_20260303/](./archive/add_safeguards_against_redudant_checks_20260303/)_

---

### [x] Track: Other pg notify opportunities
_Link: [./archive/other_pg_notify_opportunities_20260303/](./archive/other_pg_notify_opportunities_20260303/)_

---

### [x] Track: OTel observability pipeline for Grafana and test suites
_Link: [./archive/otel_observability_pipeline_for_grafana_and_test_suites_20260305/](./archive/otel_observability_pipeline_for_grafana_and_test_suites_20260305/)_

---

### [x] Track: Deprecate old code paths
_Link: [./archive/deprecate_old_code_paths_20260305/](./archive/deprecate_old_code_paths_20260305/)_

---

### [x] Track: K8s Sidecars
_Link: [./archive/k8s_sidecars_20260305/](./archive/k8s_sidecars_20260305/)_

---

### [x] Track: Nested test spans for integration suite tracing
_Link: [./archive/nested_test_spans_for_integration_suite_tracing_20260308/](./archive/nested_test_spans_for_integration_suite_tracing_20260308/)_

---

### [x] Track: evaluate-tempo-traces
_Link: [./archive/evaluate_tempo_traces_20260309/](./archive/evaluate_tempo_traces_20260309/)_

---

### [x] Track: dead-code-cleanup
_Link: [./archive/dead_code_cleanup_20260310/](./archive/dead_code_cleanup_20260310/)_

---

### [x] Track: Test running
_Link: [./archive/test_running_20260310/](./archive/test_running_20260310/)_

---

### [x] Track: AI feature segregation
_Link: [./archive/ai_feature_segregation_20260311/](./archive/ai_feature_segregation_20260311/)_

---

### [x] Track: Image Ref Hardening
_Link: [./archive/image_ref_hardening_20260311/](./archive/image_ref_hardening_20260311/)_

---

### [x] Track: Image Ref Hardening for tasks, etc
_Link: [./archive/image_ref_hardening_for_tasks_etc_20260311/](./archive/image_ref_hardening_for_tasks_etc_20260311/)_

---

### [x] Track: deploy-and-validate
_Link: [./archive/deploy_and_validate_20260313/](./archive/deploy_and_validate_20260313/)_

---

### [x] Track: production readiness
_Link: [./archive/production_readiness_20260313/](./archive/production_readiness_20260313/)_
_Completed 2026-05-31 — reconciled stale plan; finished the valid slice (volume metric, WorkerHeartbeatStale alert, helm securityContext test, RBAC verify, manual verification). Caveat: hardens deploy/chart (e2e chart), NOT prod's upstream-chart ArgoCD deploy._

---

### [ ] Track: GCP Hyperdisk Exploration
_Link: [./archive/gcp_hyperdisk_exploration_20260313/](./archive/gcp_hyperdisk_exploration_20260313/)_

---

### [x] Track: documentation
_Link: [./archive/documentation_20260316/](./archive/documentation_20260316/)_

---

### [x] Track: AI Communication and Documentation Standards
_Link: [./archive/ai_communication_and_documentation_standards_20260319/](./archive/ai_communication_and_documentation_standards_20260319/)_

---

### [x] Track: GHCR Docker Image Publishing
_Link: [./archive/ghcr_docker_image_publishing_20260319/](./archive/ghcr_docker_image_publishing_20260319/)_

---

### [x] Track: GCP Artifact Auth
_Link: [./archive/gcp_artifact_auth_20260319/](./archive/gcp_artifact_auth_20260319/)_
_Completed 2026-06-03 — scanner/image resolver now uses `authn.NewMultiKeychain(google.Keychain, authn.DefaultKeychain)` for GCP Artifact Registry (Workload Identity/ADC) with Docker-config fallback. Landed in `35aaacbfb1`, unit test green, live in theborg build `f6a6a8833d`. Status had been stale at `backlog`._

---

### [x] Track: Burstable QoS for task containers
_Link: [./archive/burstable_qos_for_task_containers_20260321/](./archive/burstable_qos_for_task_containers_20260321/)_

---

### [ ] Track: Test and Deploy Pipeline
_Link: [./tracks/test_and_deploy_pipeline_20260322/](./tracks/test_and_deploy_pipeline_20260322/)_

---

### [x] Track: Sidecar image handoff
_Link: [./archive/sidecar_image_handoff_20260323/](./archive/sidecar_image_handoff_20260323/)_

---

### [ ] Track: Load var
_Link: [./tracks/load_var_20260323/](./tracks/load_var_20260323/)_

---

### [ ] Track: image artifact failrue
_Link: [./tracks/image_artifact_failrue_20260324/](./tracks/image_artifact_failrue_20260324/)_

---

### [x] Track: Native registry-image check resolution
_Link: [./archive/native_registry_image_check_resolution_20260324/](./archive/native_registry_image_check_resolution_20260324/)_
_Completed 2026-06-07 — reconciled & closed. Feature shipped via the cleaner Option B (native resolution in `CheckStep`, `f8a29d4e31`) rather than the Option A (`TryCreateCheck`) the plan assumed: `WithCheckResolver` is wired into every CheckStep (`step_factory.go:144`), so ALL check paths (Lidar/manual/webhook/job-trigger) resolve `registry-image` natively with no check pods, using GCP Workload Identity. Live-verified on theborg via sibling `fix_native_check_self_notification_feedback_loop_20260413`; sidecar `image_artifact` covered by `skip_image_get_test.go:174`. Residual `insecure`/`ca_certs` gap → new track `native_resolver_insecure_ca_certs_20260607`._

---

### [ ] Track: Native resolver honors `insecure` and `ca_certs`
_Link: [./tracks/native_resolver_insecure_ca_certs_20260607/](./tracks/native_resolver_insecure_ca_certs_20260607/)_
_Spun off from the native registry-image track (2026-06-07). The native OCI resolver ignores `source.insecure` / `source.ca_certs`, so `registry-image` resources on private/self-signed/insecure registries fail native resolution — a regression vs the legacy check-pod path. Restore parity in `atc/imageresolver/resolver.go` + the three native call sites._

---

### [x] Track: Caching behavior and PVC
_Link: [./archive/caching_behavior_and_pvc_20260324/](./archive/caching_behavior_and_pvc_20260324/)_
_Completed 2026-06-07 — reconciled & closed. Task-cache persistence fix (stable `(jobID,stepName,path)` key + hostPath, `159fc0c482`) shipped and was adopted by the artifact daemon (`DaemonSetBackend.CacheVolume` reuses `stableCacheKey`). Both original bugs (UUID key, emptyDir wipe) fixed in production; verified by CI behavioral suite `caching_test.go` (CACHE HIT / scoping / clear-task-cache, not among k8s-e2e failures). De-scoped: GC of stale on-disk cache dirs → spun off as a follow-up hygiene task._

---

### [ ] Track: OCI Build Task Cache Testing
_Link: [./archive/oci_build_task_cache_testing_20260324/](./archive/oci_build_task_cache_testing_20260324/)_

---

### [ ] Track: Sidecar Details
_Link: [./tracks/sidecar_details_20260325/](./tracks/sidecar_details_20260325/)_

---

### [ ] Track: Scratch Mount & Cache strategies
_Link: [./archive/scratch_mount_cache_strategies_20260325/](./archive/scratch_mount_cache_strategies_20260325/)_

---

### [x] Track: Version bumps
_Link: [./archive/version_bumps_20260327/](./archive/version_bumps_20260327/)_

---

### [x] Track: database_migration_runbook
_Link: [./archive/database_migration_runbook_20260327/](./archive/database_migration_runbook_20260327/)_

---

### [x] Track: pipeline_migration_guide
_Link: [./archive/pipeline_migration_guide_20260327/](./archive/pipeline_migration_guide_20260327/)_

---

### [ ] Track: Release versioning for fly and JetBridge image
_Link: [./archive/release_versioning_for_fly_and_jetbridge_image_20260327/](./archive/release_versioning_for_fly_and_jetbridge_image_20260327/)_

---

### [x] Track: deprecate old agent paths and update tests
_Link: [./archive/deprecate_old_agent_paths_and_update_tests_20260327/](./archive/deprecate_old_agent_paths_and_update_tests_20260327/)_

---

### [x] Track: Daemon-mediated artifact resolution
_Link: [./archive/daemon_mediated_artifact_resolution_20260327/](./archive/daemon_mediated_artifact_resolution_20260327/)_
_Completed 2026-06-06 — daemon is the sole artifact authority (/resolve, /register, peer fetch, flat keys, single-call init containers); resource caching re-enabled; daemon Prometheus metrics added (9495ece8e6). 56/56 tasks; green on k8s-e2e #192._

---

### [ ] Track: Legacy cleanup: dead code, stale config, and broken references
_Link: [./tracks/legacy_cleanup_dead_code_stale_config_and_broken_references_20260328/](./tracks/legacy_cleanup_dead_code_stale_config_and_broken_references_20260328/)_

---

### [ ] Track: JetBridge Storage & Artifact Behavioral Specification
_Link: [./tracks/jetbridge_storage_behavioral_spec_20260330/](./tracks/jetbridge_storage_behavioral_spec_20260330/)_

---

### [x] Track: Sidecar working directory bug
_Link: [./archive/sidecar_workdir_bug_20260331/](./archive/sidecar_workdir_bug_20260331/)_

---

### [x] Track: Telemetry simplification
_Link: [./archive/telemetry_simplification_20260331/](./archive/telemetry_simplification_20260331/)_

---

### [x] Track: K8s Secret Ref Passthrough for Pipeline Vars
_Link: [./archive/k8s_secret_ref_passthrough_for_pipeline_vars_20260412/](./archive/k8s_secret_ref_passthrough_for_pipeline_vars_20260412/)_

---

### [x] Track: Remove implicit registry-image skip download
_Link: [./archive/remove_implicit_registry_image_skip_download_20260412/](./archive/remove_implicit_registry_image_skip_download_20260412/)_

---

### [x] Track: Fix native check self-notification feedback loop
_Link: [./archive/fix_native_check_self_notification_feedback_loop_20260413/](./archive/fix_native_check_self_notification_feedback_loop_20260413/)_
_Completed 2026-06-03 — gated `SaveVersions`/`SetResourceConfigScope` notifies and dropped the `UpdateLastCheckEndTime` notify (committed `c3e9f6d48b`). Cluster-verified on theborg/`cicd` via `LISTEN scanner`: 13 checks → only 2 notifies (both genuine new versions); native registry-image path resolved on the web node with 8 same-digest resolves → 1 saved version, 0 steady-state notifies; pin/unpin still notify. Follow-up spun off: native resolver ignores `insecure`/`ca_certs`._

---

### [x] Track: Route artifact reads through DaemonSet; remove exec-backed artifact I/O
_Link: [./archive/route_artifact_reads_through_daemonset_remove_exec_backed_artifact_io_20260418/](./archive/route_artifact_reads_through_daemonset_remove_exec_backed_artifact_io_20260418/)_

---

### [x] Track: fix cache locator pod ip poisoning
_Link: [./archive/fix_cache_locator_pod_ip_poisoning_20260423/](./archive/fix_cache_locator_pod_ip_poisoning_20260423/)_

---

### [x] Track: Artifact Daemon Resilience
_Link: [./archive/artifact_daemon_resilience_20260425/](./archive/artifact_daemon_resilience_20260425/)_

---
