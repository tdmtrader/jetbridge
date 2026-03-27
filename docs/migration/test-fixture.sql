-- test-fixture.sql
--
-- Simulates a realistic Concourse v8.0.1 database with sample data for
-- testing the JetBridge migration runbook.
--
-- Usage:
--   1. Start a fresh Concourse v8.0.1 (or create an empty DB)
--   2. Run `concourse migrate --migrate-to-latest-version` with the v8.0.1 binary
--   3. Insert this fixture data: psql -d concourse -f test-fixture.sql
--   4. Run the JetBridge migration: concourse migrate --migrate-to-latest-version
--   5. Run the validation queries from the runbook
--
-- This fixture creates:
--   - 2 teams
--   - 3 pipelines (2 active, 1 archived)
--   - 5 jobs
--   - 20 builds (mix of statuses)
--   - 3 resources with resource configs and versions
--   - 2 Garden workers with containers and volumes (stale data)
--   - Worker cache entries (stale data)
--   - Component records with interval/last_ran/paused (columns dropped by JetBridge)

-- ============================================================================
-- Teams
-- ============================================================================
INSERT INTO teams (id, name, auth, nonce, legacy_auth)
VALUES
  (1, 'main', '{"owner":{"users":["local:admin"]}}', NULL, NULL),
  (2, 'dev-team', '{"owner":{"users":["local:dev"]}}', NULL, NULL)
ON CONFLICT (id) DO NOTHING;

SELECT setval('teams_id_seq', 2);

-- ============================================================================
-- Pipelines
-- ============================================================================
INSERT INTO pipelines (id, name, team_id, paused, public, archived, parent_job_id, parent_build_id, nonce, instance_vars)
VALUES
  (1, 'ci', 1, false, true, false, NULL, NULL, NULL, NULL),
  (2, 'release', 1, false, false, false, NULL, NULL, NULL, NULL),
  (3, 'old-pipeline', 2, true, false, true, NULL, NULL, NULL, NULL)
ON CONFLICT (id) DO NOTHING;

SELECT setval('pipelines_id_seq', 3);

-- ============================================================================
-- Resources
-- ============================================================================
INSERT INTO resource_config_scopes (id, resource_config_id, last_check_start_time, last_check_end_time)
VALUES
  (1, NULL, now() - interval '1 hour', now() - interval '55 minutes'),
  (2, NULL, now() - interval '2 hours', now() - interval '115 minutes'),
  (3, NULL, now() - interval '3 hours', now() - interval '175 minutes')
ON CONFLICT (id) DO NOTHING;

SELECT setval('resource_config_scopes_id_seq', 3);

INSERT INTO resources (id, name, type, pipeline_id, config, active, nonce, resource_config_id, resource_config_scope_id)
VALUES
  (1, 'source-code', 'git', 1, '{"source":{"uri":"https://github.com/example/repo.git","branch":"main"}}', true, NULL, NULL, 1),
  (2, 'docker-image', 'registry-image', 1, '{"source":{"repository":"example/app"}}', true, NULL, NULL, 2),
  (3, 'timer', 'time', 2, '{"source":{"interval":"10m"}}', true, NULL, NULL, 3)
ON CONFLICT (id) DO NOTHING;

SELECT setval('resources_id_seq', 3);

-- ============================================================================
-- Resource Config Versions (will be affected by md5→sha256 migration)
-- ============================================================================
INSERT INTO resource_config_versions (id, resource_config_scope_id, version, version_md5, check_order, metadata)
VALUES
  (1, 1, '{"ref":"abc123"}', md5('{"ref":"abc123"}'), 1, NULL),
  (2, 1, '{"ref":"def456"}', md5('{"ref":"def456"}'), 2, NULL),
  (3, 1, '{"ref":"ghi789"}', md5('{"ref":"ghi789"}'), 3, NULL),
  (4, 2, '{"digest":"sha256:aaa"}', md5('{"digest":"sha256:aaa"}'), 1, NULL),
  (5, 2, '{"digest":"sha256:bbb"}', md5('{"digest":"sha256:bbb"}'), 2, NULL),
  (6, 3, '{"time":"2026-03-27T10:00:00Z"}', md5('{"time":"2026-03-27T10:00:00Z"}'), 1, NULL)
ON CONFLICT (id) DO NOTHING;

SELECT setval('resource_config_versions_id_seq', 6);

-- ============================================================================
-- Jobs
-- ============================================================================
INSERT INTO jobs (id, name, pipeline_id, config, active, paused, nonce, interruptible, inputs_determined, has_new_inputs, schedule_requested, max_in_flight)
VALUES
  (1, 'unit-tests', 1, '{"plan":[{"task":"test"}]}', true, false, NULL, false, true, false, '2026-03-27 00:00:00+00', 1),
  (2, 'build-image', 1, '{"plan":[{"task":"build"}]}', true, false, NULL, false, true, false, '2026-03-27 00:00:00+00', 1),
  (3, 'deploy-staging', 1, '{"plan":[{"task":"deploy"}]}', true, false, NULL, true, true, false, '2026-03-27 00:00:00+00', 1),
  (4, 'release-cut', 2, '{"plan":[{"task":"release"}]}', true, false, NULL, false, true, false, '2026-03-27 00:00:00+00', 1),
  (5, 'old-job', 3, '{"plan":[{"task":"old"}]}', false, true, NULL, false, false, false, '2026-03-27 00:00:00+00', 1)
ON CONFLICT (id) DO NOTHING;

SELECT setval('jobs_id_seq', 5);

-- ============================================================================
-- Builds (mix of statuses to verify preservation)
-- ============================================================================
INSERT INTO builds (id, name, job_id, team_id, status, scheduled, inputs_ready, start_time, end_time, create_time, pipeline_id, schema, private_plan, public_plan, drained, aborted, completed)
VALUES
  (1, '1', 1, 1, 'succeeded', true, true, now()-interval '10 days', now()-interval '10 days'+interval '3 minutes', now()-interval '10 days', 1, 'exec.v2', '{}', '{}', false, false, true),
  (2, '2', 1, 1, 'succeeded', true, true, now()-interval '9 days', now()-interval '9 days'+interval '2 minutes', now()-interval '9 days', 1, 'exec.v2', '{}', '{}', false, false, true),
  (3, '3', 1, 1, 'failed', true, true, now()-interval '8 days', now()-interval '8 days'+interval '4 minutes', now()-interval '8 days', 1, 'exec.v2', '{}', '{}', false, false, true),
  (4, '4', 1, 1, 'succeeded', true, true, now()-interval '7 days', now()-interval '7 days'+interval '2 minutes', now()-interval '7 days', 1, 'exec.v2', '{}', '{}', false, false, true),
  (5, '5', 1, 1, 'errored', true, true, now()-interval '6 days', now()-interval '6 days'+interval '1 minute', now()-interval '6 days', 1, 'exec.v2', '{}', '{}', false, false, true),
  (6, '1', 2, 1, 'succeeded', true, true, now()-interval '10 days', now()-interval '10 days'+interval '5 minutes', now()-interval '10 days', 1, 'exec.v2', '{}', '{}', false, false, true),
  (7, '2', 2, 1, 'succeeded', true, true, now()-interval '8 days', now()-interval '8 days'+interval '6 minutes', now()-interval '8 days', 1, 'exec.v2', '{}', '{}', false, false, true),
  (8, '3', 2, 1, 'failed', true, true, now()-interval '6 days', now()-interval '6 days'+interval '4 minutes', now()-interval '6 days', 1, 'exec.v2', '{}', '{}', false, false, true),
  (9, '1', 3, 1, 'succeeded', true, true, now()-interval '9 days', now()-interval '9 days'+interval '2 minutes', now()-interval '9 days', 1, 'exec.v2', '{}', '{}', false, false, true),
  (10, '2', 3, 1, 'succeeded', true, true, now()-interval '7 days', now()-interval '7 days'+interval '3 minutes', now()-interval '7 days', 1, 'exec.v2', '{}', '{}', false, false, true),
  (11, '3', 3, 1, 'aborted', true, true, now()-interval '5 days', now()-interval '5 days'+interval '1 minute', now()-interval '5 days', 1, 'exec.v2', '{}', '{}', false, true, true),
  (12, '1', 4, 1, 'succeeded', true, true, now()-interval '5 days', now()-interval '5 days'+interval '10 minutes', now()-interval '5 days', 2, 'exec.v2', '{}', '{}', false, false, true),
  (13, '1', 5, 2, 'succeeded', true, true, now()-interval '30 days', now()-interval '30 days'+interval '5 minutes', now()-interval '30 days', 3, 'exec.v2', '{}', '{}', false, false, true),
  (14, '2', 5, 2, 'failed', true, true, now()-interval '29 days', now()-interval '29 days'+interval '3 minutes', now()-interval '29 days', 3, 'exec.v2', '{}', '{}', false, false, true)
ON CONFLICT (id) DO NOTHING;

SELECT setval('builds_id_seq', 14);

-- ============================================================================
-- Garden Workers (stale — will be cleaned up after migration)
-- ============================================================================
INSERT INTO workers (name, addr, state, baggageclaim_url, active_containers, active_volumes, platform, tags, team_id, start_time, expires, version, http_proxy_url, https_proxy_url, no_proxy, ephemeral)
VALUES
  ('garden-worker-1', '10.0.0.10:7777', 'running', 'http://10.0.0.10:7788', 5, 12, 'linux', NULL, NULL, now()-interval '30 days', now()-interval '1 day', '2.4', NULL, NULL, NULL, false),
  ('garden-worker-2', '10.0.0.11:7777', 'stalled', 'http://10.0.0.11:7788', 0, 3, 'linux', NULL, NULL, now()-interval '60 days', now()-interval '30 days', '2.4', NULL, NULL, NULL, false)
ON CONFLICT (name) DO NOTHING;

-- ============================================================================
-- Containers (referencing Garden workers — stale)
-- ============================================================================
INSERT INTO containers (id, handle, worker_name, build_id, plan_id, pipeline_id, job_id, state, hijacked, discontinued, meta_type, meta_step_name, team_id)
VALUES
  (1, 'container-aaa', 'garden-worker-1', 4, 'plan-1', 1, 1, 'created', false, false, 'task', 'test', 1),
  (2, 'container-bbb', 'garden-worker-1', 6, 'plan-1', 1, 2, 'destroying', false, true, 'task', 'build', 1),
  (3, 'container-ccc', 'garden-worker-2', 9, 'plan-1', 1, 3, 'created', false, false, 'task', 'deploy', 1)
ON CONFLICT (id) DO NOTHING;

SELECT setval('containers_id_seq', 3);

-- ============================================================================
-- Volumes (referencing Garden workers — stale)
-- ============================================================================
INSERT INTO volumes (id, handle, worker_name, state, team_id)
VALUES
  (1, 'volume-aaa', 'garden-worker-1', 'created', 1),
  (2, 'volume-bbb', 'garden-worker-1', 'created', 1),
  (3, 'volume-ccc', 'garden-worker-2', 'destroying', 1)
ON CONFLICT (id) DO NOTHING;

SELECT setval('volumes_id_seq', 3);

-- ============================================================================
-- Worker Base Resource Types (stale)
-- ============================================================================
INSERT INTO base_resource_types (id, name)
VALUES (1, 'registry-image'), (2, 'git'), (3, 'time')
ON CONFLICT (id) DO NOTHING;

SELECT setval('base_resource_types_id_seq', 3);

INSERT INTO worker_base_resource_types (id, worker_name, base_resource_type_id, image, version)
VALUES
  (1, 'garden-worker-1', 1, '/opt/resource-types/registry-image', '1.0'),
  (2, 'garden-worker-1', 2, '/opt/resource-types/git', '1.0'),
  (3, 'garden-worker-2', 1, '/opt/resource-types/registry-image', '1.0')
ON CONFLICT (id) DO NOTHING;

SELECT setval('worker_base_resource_types_id_seq', 3);

-- ============================================================================
-- Components (will have interval/last_ran/paused columns dropped)
-- ============================================================================
-- These columns exist in v8.0.1 but are dropped by JetBridge migrations
-- Insert sample component data to verify the column drops work correctly
INSERT INTO components (id, name, interval, last_ran, paused)
VALUES
  (1, 'scheduler', '10s', now()-interval '5 seconds', false),
  (2, 'scanner', '10s', now()-interval '3 seconds', false),
  (3, 'build_tracker', '10s', now()-interval '1 second', false),
  (4, 'collector', '30s', now()-interval '15 seconds', false)
ON CONFLICT (id) DO UPDATE SET
  interval = EXCLUDED.interval,
  last_ran = EXCLUDED.last_ran,
  paused = EXCLUDED.paused;

-- ============================================================================
-- Summary: Expected Row Counts After Migration
-- ============================================================================
-- teams:                    2
-- pipelines:                3
-- jobs:                     5
-- builds:                  14
-- resources:                3
-- resource_config_versions: 6
-- workers:                  2 (stale, to be cleaned)
-- containers:               3 (stale, to be cleaned)
-- volumes:                  3 (stale, to be cleaned)
-- components:               4 (interval/last_ran/paused columns removed)
