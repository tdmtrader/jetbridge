DROP TRIGGER IF EXISTS agent_run_attempts_insertion ON agent_run_attempts;
DROP TRIGGER IF EXISTS agent_run_attempts_transition ON agent_run_attempts;
DROP FUNCTION IF EXISTS enforce_agent_run_attempt_insertion();
DROP FUNCTION IF EXISTS enforce_agent_run_attempt_transition();
DROP TABLE IF EXISTS agent_run_attempt_fence_tokens;
DROP TABLE IF EXISTS agent_run_attempts;
