package db

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/concourse/concourse/agent/workflow"
)

// Consumers returns immutable workflow imports of one exact node version.
// The tuple cursor is necessary because one workflow revision can bind the
// same node version under multiple local instance names.
func (f *agentNodesFactory) Consumers(
	ctx context.Context,
	nodeName string,
	nodeVersion int,
	request workflow.NodeConsumerRequest,
) (workflow.NodeConsumerPage, error) {
	if ctx == nil {
		return workflow.NodeConsumerPage{}, fmt.Errorf("workflow: node consumer context is required")
	}
	if request.Limit <= 0 || request.Limit > workflow.MaxVersionPageSize ||
		request.Cursor.WorkflowDefinitionID < 0 ||
		(request.Cursor.WorkflowDefinitionID == 0 && request.Cursor.InstanceName != "") {
		return workflow.NodeConsumerPage{}, workflow.ErrInvalidNodeConsumerPage
	}

	rows, err := f.conn.QueryContext(ctx, `
		SELECT b.workflow_definition_id, d.name, d.version, d.live,
			b.instance_name, b.node_definition_id, b.node_name, b.node_version,
			b.node_content_hash, b.input_mapping, b.output_mapping, b.parameters
		FROM agent_workflow_node_bindings b
		JOIN agent_workflow_definitions d ON d.id = b.workflow_definition_id
			AND d.definition_kind = 'workflow'
		WHERE b.node_name = $1 AND b.node_version = $2
			AND ($3 = 0 OR b.workflow_definition_id < $3
				OR (b.workflow_definition_id = $3 AND b.instance_name > $4))
			AND (NOT $5 OR d.live)
		ORDER BY b.workflow_definition_id DESC, b.instance_name ASC
		LIMIT $6`, nodeName, nodeVersion, request.Cursor.WorkflowDefinitionID,
		request.Cursor.InstanceName, request.PromotedOnly, request.Limit+1)
	if err != nil {
		return workflow.NodeConsumerPage{}, err
	}
	defer rows.Close()

	page := workflow.NodeConsumerPage{Consumers: []workflow.NodeConsumer{}}
	for rows.Next() {
		consumer, err := scanNodeConsumer(rows)
		if err != nil {
			return workflow.NodeConsumerPage{}, err
		}
		page.Consumers = append(page.Consumers, consumer)
	}
	if err := rows.Err(); err != nil {
		return workflow.NodeConsumerPage{}, err
	}
	if len(page.Consumers) > request.Limit {
		page.Consumers = page.Consumers[:request.Limit]
		last := page.Consumers[len(page.Consumers)-1]
		page.NextCursor = workflow.NodeConsumerCursor{
			WorkflowDefinitionID: last.WorkflowDefinitionID,
			InstanceName:         last.Binding.InstanceName,
		}
	}
	return page, nil
}

// Bindings returns the exact durable expansion records for a workflow
// definition. It is deliberately keyed by immutable definition ID rather than
// workflow name/version so upgrade idempotency cannot race a later revision.
func (f *agentNodesFactory) Bindings(workflowDefinitionID int) ([]workflow.ResolvedNodeBinding, error) {
	rows, err := f.conn.Query(`
		SELECT instance_name, node_definition_id, node_name, node_version,
			node_content_hash, input_mapping, output_mapping, parameters
		FROM agent_workflow_node_bindings
		WHERE workflow_definition_id = $1
		ORDER BY instance_name ASC`, workflowDefinitionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	bindings := []workflow.ResolvedNodeBinding{}
	for rows.Next() {
		var binding workflow.ResolvedNodeBinding
		var input, output, parameters []byte
		if err := rows.Scan(
			&binding.InstanceName, &binding.NodeDefinitionID, &binding.NodeName,
			&binding.NodeVersion, &binding.NodeContentHash, &input, &output, &parameters,
		); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(input, &binding.InputMapping); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(output, &binding.OutputMapping); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(parameters, &binding.Parameters); err != nil {
			return nil, err
		}
		bindings = append(bindings, binding)
	}
	return bindings, rows.Err()
}

func scanNodeConsumer(rows interface{ Scan(...any) error }) (workflow.NodeConsumer, error) {
	var consumer workflow.NodeConsumer
	var input, output, parameters []byte
	if err := rows.Scan(
		&consumer.WorkflowDefinitionID, &consumer.WorkflowName, &consumer.WorkflowVersion, &consumer.Live,
		&consumer.Binding.InstanceName, &consumer.Binding.NodeDefinitionID, &consumer.Binding.NodeName,
		&consumer.Binding.NodeVersion, &consumer.Binding.NodeContentHash,
		&input, &output, &parameters,
	); err != nil {
		return workflow.NodeConsumer{}, err
	}
	if err := json.Unmarshal(input, &consumer.Binding.InputMapping); err != nil {
		return workflow.NodeConsumer{}, err
	}
	if err := json.Unmarshal(output, &consumer.Binding.OutputMapping); err != nil {
		return workflow.NodeConsumer{}, err
	}
	if err := json.Unmarshal(parameters, &consumer.Binding.Parameters); err != nil {
		return workflow.NodeConsumer{}, err
	}
	return consumer, nil
}
