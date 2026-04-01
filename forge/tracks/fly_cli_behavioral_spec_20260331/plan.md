# Fly CLI Behavioral Spec — Coverage Matrix & Implementation Plan

## Coverage Matrix

### Section 1: Pipeline Management Commands (16 requirements)

| ID | Requirement | Coverage | Test Location | Notes |
|----|------------|----------|---------------|-------|
| PM-01 | set-pipeline | ✅ Full | set_pipeline_test.go (1438 LOC, 28 tests) | Variables, instances, dry-run, credentials |
| PM-02 | get-pipeline | ✅ Full | get_pipeline_test.go | Config retrieval, YAML output |
| PM-03 | pipelines | ✅ Full | pipelines_test.go | List, --all, --json |
| PM-04 | paused-pipelines | ✅ Full | paused_pipelines_test.go:46-70 | Lists only paused, excludes unpaused |
| PM-05 | destroy-pipeline | ✅ Full | destroy_pipeline_test.go | Confirmation, non-interactive |
| PM-06 | pause-pipeline | ✅ Full | pause_pipeline_test.go | PUT to pause |
| PM-07 | unpause-pipeline | ✅ Full | unpause_pipeline_test.go | PUT to unpause |
| PM-08 | archive-pipeline | ✅ Full | archive_pipeline_test.go (423 LOC) | Confirmation prompts |
| PM-09 | expose-pipeline | ✅ Full | expose_pipeline_test.go | PUT to expose |
| PM-10 | hide-pipeline | ✅ Full | hide_pipeline_test.go | PUT to hide |
| PM-11 | rename-pipeline | ✅ Full | rename_pipeline_test.go | PUT rename |
| PM-12 | order-pipelines | ✅ Full | ordering_pipelines_test.go | PUT ordering |
| PM-13 | order-instanced-pipelines | ✅ Full | ordering_instanced_pipelines_test.go | Instance group ordering |
| PM-14 | validate-pipeline | ✅ Full | validate_pipeline_test.go | Local validation |
| PM-15 | format-pipeline | ✅ Full | format_pipeline_test.go | YAML formatting |
| PM-16 | checklist | ✅ Full | checklist_test.go | Checkfile format |

**Summary:** 16/16 Full (100%)

---

### Section 2: Build & Execution Commands (6 requirements)

| ID | Requirement | Coverage | Test Location | Notes |
|----|------------|----------|---------------|-------|
| BE-01 | execute | ✅ Full | execute_test.go + 3 variant files (2800+ LOC) | Inputs, outputs, git repos, params |
| BE-02 | builds | ✅ Full | builds_test.go (1110 LOC) | Pagination, filtering, formatting |
| BE-03 | abort-build | ✅ Full | abort_build_test.go | PUT abort |
| BE-04 | trigger-job | ✅ Full | trigger_job_test.go | POST create, team scoping |
| BE-05 | watch | ✅ Full | watch_test.go | SSE streaming |
| BE-06 | rerun-build | ✅ Full | rerun_build_test.go:37-77 | Rerun success + not found error |

**Summary:** 6/6 Full (100%)

---

### Section 3: Resource Management Commands (8 requirements)

| ID | Requirement | Coverage | Test Location | Notes |
|----|------------|----------|---------------|-------|
| RM-01 | resources | ✅ Full | resources_test.go (460 LOC) | Listing, pagination, instance vars |
| RM-02 | resource-versions | ✅ Full | resource_versions_test.go | Version listing, pagination |
| RM-03 | check-resource | ✅ Full | check_resource_test.go | POST check trigger |
| RM-04 | check-resource-type | ✅ Full | check_resource_type_test.go | POST type check |
| RM-05 | pin-resource | ✅ Full | pin_resource_test.go | PUT pin version |
| RM-06 | unpin-resource | ✅ Full | unpin_resource_test.go | PUT unpin |
| RM-07 | enable/disable-resource-version | ✅ Full | enable_resource_version_test.go, disable_resource_version_test.go | Toggle version |
| RM-08 | clear-versions | ✅ Full | clear_versions_test.go (425 LOC) | Confirmation, async modes |

**Summary:** 8/8 Full (100%)

---

### Section 4: Team & Auth Commands (8 requirements)

| ID | Requirement | Coverage | Test Location | Notes |
|----|------------|----------|---------------|-------|
| TA-01 | login | ✅ Full | login_test.go + login_insecure_test.go (1200+ LOC) | SSO, password, CA certs |
| TA-02 | logout | ✅ Full | logout_test.go | Token removal |
| TA-03 | status | ✅ Full | status_test.go | Auth status |
| TA-04 | teams | ✅ Full | teams_test.go | Team listing |
| TA-05 | set-team | ✅ Full | set_team_test.go (788 LOC) | Auth methods, permissions |
| TA-06 | destroy-team | ✅ Full | destroy_team_test.go | Confirmation |
| TA-07 | active-users | ✅ Full | active_users_test.go:18-56 | User listing + invalid --since error |
| TA-08 | userinfo | ✅ Full | userinfo_test.go | User info display |

**Summary:** 8/8 Full (100%)

---

### Section 5: Infrastructure Commands (5 requirements)

| ID | Requirement | Coverage | Test Location | Notes |
|----|------------|----------|---------------|-------|
| IC-01 | workers | ✅ Full | workers_test.go (466 LOC) | Listing, pagination |
| IC-02 | containers | ✅ Full | containers_test.go | Container listing |
| IC-03 | volumes | ✅ Full | volumes_test.go | Volume listing |
| IC-04 | hijack | ✅ Full | hijack_test.go (1164 LOC) | Container selection, team scoping |
| IC-05 | clear-task-cache | ✅ Full | clear_task_cache_test.go | Cache deletion |

**Summary:** 5/5 Full (100%)

---

### Section 6: Utility Commands (6 requirements)

| ID | Requirement | Coverage | Test Location | Notes |
|----|------------|----------|---------------|-------|
| UC-01 | targets | ✅ Full | targets_test.go | .flyrc listing |
| UC-02 | sync | ✅ Full | sync_test.go | Binary download |
| UC-03 | version | ✅ Full | version_test.go | Version print |
| UC-04 | curl | ✅ Full | curl_test.go | API proxy |
| UC-05 | wall messages | ✅ Full | set_wall_test.go, get_wall_test.go, clear_wall_test.go | Set/get/clear |
| UC-06 | schedule-job | ✅ Full | schedule_job_test.go:37-79 | Schedule success + not found error |

**Summary:** 6/6 Full (100%)

---

### Section 7: Job Management Commands (3 requirements)

| ID | Requirement | Coverage | Test Location | Notes |
|----|------------|----------|---------------|-------|
| JM-01 | jobs | ✅ Full | jobs_test.go | Job listing |
| JM-02 | pause/unpause-job | ✅ Full | pause_job_test.go, unpause_job_test.go | Toggle job state |
| JM-03 | paused-jobs | ✅ Full | paused_jobs_test.go:41-68 | Lists only paused, excludes unpaused |

**Summary:** 3/3 Full (100%)

---

## Overall Summary

| Section | Requirements | Full | Partial | None | Coverage |
|---------|-------------|------|---------|------|----------|
| 1. Pipeline Management | 16 | 16 | 0 | 0 | 100% |
| 2. Build & Execution | 6 | 6 | 0 | 0 | 100% |
| 3. Resource Management | 8 | 8 | 0 | 0 | 100% |
| 4. Team & Auth | 8 | 8 | 0 | 0 | 100% |
| 5. Infrastructure | 5 | 5 | 0 | 0 | 100% |
| 6. Utility | 6 | 6 | 0 | 0 | 100% |
| 7. Job Management | 3 | 3 | 0 | 0 | 100% |
| **TOTAL** | **52** | **52** | **0** | **0** | **100%** |

## Gap-Filling Summary

All 5 identified gaps have been filled with new integration test files:

### P1 Gaps — Fixed

- [x] **BE-06**: Added `fly/integration/rerun_build_test.go` — rerun success + not found error
- [x] **UC-06**: Added `fly/integration/schedule_job_test.go` — schedule success + not found error

### P2 Gaps — Fixed

- [x] **PM-04**: Added `fly/integration/paused_pipelines_test.go` — filters paused, excludes unpaused
- [x] **TA-07**: Added `fly/integration/active_users_test.go` — user listing + invalid date error
- [x] **JM-03**: Added `fly/integration/paused_jobs_test.go` — filters paused, excludes unpaused

### New Tests Added (10 specs across 5 files)

| File | New Specs | What They Test |
|------|-----------|---------------|
| `fly/integration/rerun_build_test.go` | 2 | Rerun build success, not found error |
| `fly/integration/schedule_job_test.go` | 2 | Schedule success, job not found |
| `fly/integration/paused_pipelines_test.go` | 2 | Paused filter, unpaused excluded |
| `fly/integration/active_users_test.go` | 2 | User listing, invalid date format |
| `fly/integration/paused_jobs_test.go` | 2 | Paused filter, unpaused excluded |

### Verification

- `ginkgo --focus="rerun-build|schedule-job|paused-pipelines|active-users|paused-jobs" ./fly/integration/` — 10 specs PASSED
