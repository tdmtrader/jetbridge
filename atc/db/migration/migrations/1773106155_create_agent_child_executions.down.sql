DROP TRIGGER agent_child_executions_transition ON agent_child_executions;
DROP FUNCTION enforce_agent_child_execution_transition();
DROP TABLE agent_child_execution_events;
DROP TABLE agent_child_executions;
ALTER TABLE agent_snapshots
    DROP CONSTRAINT agent_snapshots_id_team_key;
