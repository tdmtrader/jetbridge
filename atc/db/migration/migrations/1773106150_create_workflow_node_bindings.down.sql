DROP TABLE agent_workflow_node_bindings;

ALTER TABLE agent_workflow_definitions
    DROP CONSTRAINT agent_workflow_definitions_node_identity_key,
    DROP CONSTRAINT agent_workflow_definitions_id_kind_key;
