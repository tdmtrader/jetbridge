ALTER TABLE agent_workflow_definitions
    ADD CONSTRAINT agent_workflow_definitions_id_kind_key UNIQUE (id, definition_kind),
    ADD CONSTRAINT agent_workflow_definitions_node_identity_key UNIQUE (id, definition_kind, name, version, content_hash);

CREATE TABLE agent_workflow_node_bindings (
    workflow_definition_id INTEGER NOT NULL,
    workflow_definition_kind TEXT NOT NULL DEFAULT 'workflow' CHECK (workflow_definition_kind = 'workflow'),
    instance_name TEXT NOT NULL,
    node_definition_id INTEGER NOT NULL,
    node_definition_kind TEXT NOT NULL DEFAULT 'node' CHECK (node_definition_kind = 'node'),
    node_name TEXT NOT NULL,
    node_version INTEGER NOT NULL,
    node_content_hash TEXT NOT NULL,
    input_mapping JSONB NOT NULL,
    output_mapping JSONB NOT NULL,
    parameters JSONB NOT NULL,
    PRIMARY KEY (workflow_definition_id, instance_name),
    FOREIGN KEY (workflow_definition_id, workflow_definition_kind)
        REFERENCES agent_workflow_definitions (id, definition_kind),
    FOREIGN KEY (node_definition_id, node_definition_kind, node_name, node_version, node_content_hash)
        REFERENCES agent_workflow_definitions (id, definition_kind, name, version, content_hash)
);

CREATE INDEX agent_workflow_node_bindings_node_workflow
    ON agent_workflow_node_bindings (node_definition_id, workflow_definition_id);
CREATE INDEX agent_workflow_node_bindings_node_version
    ON agent_workflow_node_bindings (node_name, node_version);
