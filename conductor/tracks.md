# Project Tracks

---

## [x] Track: Production Readiness — Complete runtime gaps, Helm chart, and production validation
*Completed: 2026-02-09*
*Archive: [./conductor/archive/prod_ready_20260209/](./conductor/archive/prod_ready_20260209/)*

---

## [x] Track: Kubernetes Spec and Sidecar — Task step sidecar container support
*Completed: 2026-02-09*
*Archive: [./conductor/archive/kubernetes_spec_and_sidecar_20260209/](./conductor/archive/kubernetes_spec_and_sidecar_20260209/)*

---

## [x] Track: Agent step output schema — results.json and events.ndjson conventions
*Completed: 2026-02-09*
*Link: [./conductor/tracks/agent_step_output_schema_20260209/](./conductor/tracks/agent_step_output_schema_20260209/)*

---

## [x] Track: CI Agent Review Task — TDD-driven autonomous code review
*Completed: 2026-02-10*
*Link: [./conductor/tracks/ci_agent_review_20260209/](./conductor/tracks/ci_agent_review_20260209/)*

---

## [x] Track: Agent Fix Step — Resolve proven issues from agent-review, commit fixes, output repo for PR
*Completed: 2026-02-10*
*Link: [./conductor/tracks/agent_can_resolve_simple_fixes_from_the_agent_review_step_20260209/](./conductor/tracks/agent_can_resolve_simple_fixes_from_the_agent_review_step_20260209/)*

---

## [x] Track: Human Feedback on Agent Reviews — Interactive Elm UI for collecting structured verdicts, stored in PostgreSQL
*Completed: 2026-02-10*
*Link: [./conductor/tracks/human_feedback_on_agent_reviews_20260209/](./conductor/tracks/human_feedback_on_agent_reviews_20260209/)*

---

## [x] Track: Agent can produce a clear, documented plan from a prompt/Jira story
*Completed: 2026-02-10*
*Link: [./conductor/tracks/agent_can_produce_a_clear_documented_plan_from_a_promptjira_story_20260209/](./conductor/tracks/agent_can_produce_a_clear_documented_plan_from_a_promptjira_story_20260209/)*

---

## [x] Track: CI Agent QA — Spec-driven functional QA with coverage analysis and browser QA plan
*Completed: 2026-02-10*
*Link: [./conductor/tracks/agent_can_qa_a_story_given_a_spec_20260209/](./conductor/tracks/agent_can_qa_a_story_given_a_spec_20260209/)*

---

## [x] Track: Agent can iterate on a story given a spec — TDD-driven implementation agent
*Completed: 2026-02-10*
*Link: [./conductor/tracks/agent_can_iterate_on_a_story_given_a_spec_20260209/](./conductor/tracks/agent_can_iterate_on_a_story_given_a_spec_20260209/)*

---

## [x] Track: Codebase Hardening — Wire feedback API, fill test gaps, remove deprecated code
*Completed: 2026-02-10*
*Archive: [./conductor/archive/codebase-hardening-20260210/](./conductor/archive/codebase-hardening-20260210/)*

---

## [x] Track: Legacy Cleanup — Remove dead Garden/containerd/TSA/BOSH code
*Completed: 2026-02-11*
*Archive: [./conductor/archive/legacy-cleanup-20260210/](./conductor/archive/legacy-cleanup-20260210/)*

---

## [x] Track: Helm Chart Parity — Restore service annotations, loadBalancerIP, TLS, and extra volume mounts
*Completed: 2026-02-11*
*Archive: [./conductor/archive/helm-chart-parity/](./conductor/archive/helm-chart-parity/)*

---

## [x] Track: Pipeline Split — Separate Agent Testing
*Completed: 2026-02-11*
*Archive: [./conductor/archive/pipeline-split-agent-testing/](./conductor/archive/pipeline-split-agent-testing/)*

---

## [x] Track: GCS Fuse Pod Annotation — Add gke-gcsfuse/volumes annotation to artifact store pods
*Completed: 2026-02-10*
*Link: [./conductor/tracks/gcs-fuse-pod-annotation/](./conductor/tracks/gcs-fuse-pod-annotation/)*

---

## [x] Track: Observability Hardening — OTel-native traces, metrics, and spans with Grafana/GCP compatibility
*Completed: 2026-02-11*
*Archive: [./conductor/archive/observability-hardening/](./conductor/archive/observability-hardening/)*

---

## [x] Track: Too Many Check Pods — Deduplicate in-flight checks and cap failed containers
*Completed: 2026-02-11*
*Archive: [./conductor/archive/too_many_check_pods_20260211/](./conductor/archive/too_many_check_pods_20260211/)*

---

## [x] Track: Dir Volume Bug — Fix missing K8s emptyDir for spec.Dir in buildVolumeMounts
*Completed: 2026-02-11*
*Link: [./conductor/tracks/dir_volume_bug_20260211/](./conductor/tracks/dir_volume_bug_20260211/)*

---

## [x] Track: K8s Native Image Fetch — Skip physical image download for K8s runtime, let kubelet pull
*Completed: 2026-02-11*
*Archive: [./conductor/archive/k8s_native_image_fetch_20260211/](./conductor/archive/k8s_native_image_fetch_20260211/)*

---

## [x] Track: Skip Image Resource Download on K8s — Skip get step for image resources on K8s, pass only digest/SHA
*Completed: 2026-02-12*
*Archive: [./conductor/archive/skip-image-get-k8s/](./conductor/archive/skip-image-get-k8s/)*

---

## [x] Track: Pod Leak Investigation — Fix K8s pod leak from in-memory check build Finish() orphaning pods
*Completed: 2026-02-11*
*Link: [./conductor/tracks/pod-leak-investigation/](./conductor/tracks/pod-leak-investigation/)*

---

## [x] Track: Configurable Base Resource Types — Operator-defined base types + metadata-only FetchImage on K8s
*Completed: 2026-02-12*
*Link: [./conductor/tracks/configurable-base-resource-types/](./conductor/tracks/configurable-base-resource-types/)*

---

## [x] Track: Slim Check Pods — Remove unnecessary artifact-helper sidecar and GCS FUSE from check pods
*Completed: 2026-02-12*
*Link: [./conductor/tracks/slim-check-pods/](./conductor/tracks/slim-check-pods/)*

---

## [x] Track: Fix Empty Image for Git-Backed Custom Resource Types on K8s — Resolve custom type images via ResourceTypeImages config when ImageArtifact path is unsupported
*Completed: 2026-02-12*
*Archive: [./conductor/archive/fix-empty-image-git-custom-types/](./conductor/archive/fix-empty-image-git-custom-types/)*

---

## [x] Track: Direct Image References for Resource Types — Add `image:` field to resource types, bypassing check/get for static image refs
*Completed: 2026-02-12*
*Link: [./conductor/tracks/resource-type-image-refs/](./conductor/tracks/resource-type-image-refs/)*

---

## [x] Track: Fix Input/Output Volume Shadowing — Share single volume when task input and output target the same path
*Completed: 2026-02-12*
*Link: [./conductor/tracks/input-output-volume-shadowing/](./conductor/tracks/input-output-volume-shadowing/)*

---

## [x] Track: Inline Sidecar Config — Allow defining sidecars directly in pipeline YAML instead of requiring a separate file reference
*Completed: 2026-02-13*
*Archive: [./conductor/archive/inline-sidecar-config/](./conductor/archive/inline-sidecar-config/)*

---

## [x] Track: Get Step skip_download — Explicit metadata-only get for image resources, skip artifact download and let kubelet pull
*Completed: 2026-02-13*
*Archive: [./conductor/archive/get-step-skip-download/](./conductor/archive/get-step-skip-download/)*

---

## [~] Track: K8s Behavioral Integration Test Suite — Comprehensive mock-free behavioral tests for the K8s-native runtime
*Link: [./conductor/tracks/k8s-behavioral-integration-tests/](./conductor/tracks/k8s-behavioral-integration-tests/)*

---

## [x] Track: K8s Behavioral Test Failures — Fix 26 failing integration tests (artifact streaming, pod lifecycle, behavioral gaps)
*Completed: 2026-02-16*
*Link: [./conductor/tracks/k8s-behavioral-test-failures/](./conductor/tracks/k8s-behavioral-test-failures/)*

---

## [ ] Track: Isolate K8s Test Suites — Remove external cluster mode, make tests self-contained with testcontainers-go
*Link: [./conductor/tracks/isolate-k8s-test-suites/](./conductor/tracks/isolate-k8s-test-suites/)*

---

## [ ] Track: Deprecate produces: registry-image syntax
*Link: [./conductor/tracks/deprecate-produces-registry-image/](./conductor/tracks/deprecate-produces-registry-image/)*

---

### [ ] Track: Restore service annotations, loadBalancerIP, TLS support, and extra volume mounts to the Helm chart

_Link: [./tracks/helm-chart-parity/](./tracks/helm-chart-parity/)_

---

### [ ] Track: Legacy Cleanup — Remove dead Garden/containerd/TSA/BOSH code

_Link: [./tracks/legacy-cleanup-20260210/](./tracks/legacy-cleanup-20260210/)_

---

### [ ] Track: Pipeline Split — Separate Agent Testing

_Link: [./tracks/pipeline-split-agent-testing/](./tracks/pipeline-split-agent-testing/)_

---

### [ ] Track: Too many check pods — deduplicate in-flight checks and cap failed containers

_Link: [./tracks/too_many_check_pods_20260211/](./tracks/too_many_check_pods_20260211/)_

---

### [x] Track: Codebase Hardening — Wire feedback API, fill test gaps, remove deprecated code

_Link: [./archive/codebase-hardening-20260210/](./archive/codebase-hardening-20260210/)_

---

### [x] Track: Fix Empty Image for Git-Backed Custom Resource Types on K8s — Resolve custom type images via ResourceTypeImages config when ImageArtifact path is unsupported

_Link: [./archive/fix-empty-image-git-custom-types/](./archive/fix-empty-image-git-custom-types/)_

---

### [x] Track: Get Step skip_download — Explicit metadata-only get for image resources, skip artifact download and let kubelet pull

_Link: [./archive/get-step-skip-download/](./archive/get-step-skip-download/)_

---

### [x] Track: Allow defining sidecars inline in pipeline YAML instead of requiring a separate file reference

_Link: [./archive/inline-sidecar-config/](./archive/inline-sidecar-config/)_

---

### [x] Track: K8s Native Image Fetch — Skip physical image download for K8s runtime, let kubelet pull

_Link: [./archive/k8s_native_image_fetch_20260211/](./archive/k8s_native_image_fetch_20260211/)_

---

### [x] Track: Kubernetes Spec and Sidecar

_Link: [./archive/kubernetes_spec_and_sidecar_20260209/](./archive/kubernetes_spec_and_sidecar_20260209/)_

---

### [x] Track: Unify and extend OTel-native observability (traces, metrics) with Grafana and GCP compatibility

_Link: [./archive/observability-hardening/](./archive/observability-hardening/)_

---

### [x] Track: Production Readiness — Complete remaining runtime gaps, add Helm chart, and validate for production deployment

_Link: [./archive/prod_ready_20260209/](./archive/prod_ready_20260209/)_

---

### [x] Track: Skip Image Resource Download on K8s — Skip get step for image resources on K8s, pass only digest/SHA

_Link: [./archive/skip-image-get-k8s/](./archive/skip-image-get-k8s/)_

---
