# Using MCP Tools in Forge Skills

Complete guide to discovering and using MCP tools effectively in Forge project skills.

## Overview

Once an MCP server is configured in `.mcp.json`, its tools become available for use. Discover available tools and their schemas using `/mcp`.

## Tool Discovery

### Using /mcp

Run the `/mcp` command to see:
- All available MCP servers
- Tools provided by each server
- Tool schemas and descriptions
- Full tool names

### Understanding Tool Names

MCP tools are automatically namespaced to prevent conflicts. The exact naming depends on the AI CLI being used. Use `/mcp` to discover the full tool names available in your environment.

## Tool Call Patterns

### Pattern 1: Simple Tool Call

Single tool call with validation:

```markdown
Steps:
1. Validate user provided required fields
2. Call the MCP tool with validated data
3. Check for errors
4. Display confirmation
```

### Pattern 2: Sequential Tools

Chain multiple tool calls:

```markdown
Steps:
1. Search for existing items using the search tool
2. If not found, create new using the create tool
3. Add metadata using the update tool
4. Return final item ID
```

### Pattern 3: Batch Operations

Multiple calls with same tool:

```markdown
Steps:
1. Get list of items to process
2. For each item:
   - Call the update tool
   - Track success/failure
3. Report results summary
```

### Pattern 4: Error Handling

Graceful error handling:

```markdown
Steps:
1. Try to call the MCP tool
2. If error (rate limit, network, etc.):
   - Wait and retry (max 3 attempts)
   - If still failing, inform user
   - Suggest checking configuration
3. On success, process data
```

## Tool Parameters

### Understanding Tool Schemas

Each MCP tool has a schema defining its parameters. View with `/mcp`.

**Example schema:**
```json
{
  "name": "create_task",
  "description": "Create a new task",
  "inputSchema": {
    "type": "object",
    "properties": {
      "name": {
        "type": "string",
        "description": "Task title"
      },
      "notes": {
        "type": "string",
        "description": "Task description"
      },
      "workspace": {
        "type": "string",
        "description": "Workspace GID"
      }
    },
    "required": ["name", "workspace"]
  }
}
```

### Parameter Validation

**In skills, validate before calling:**

```markdown
Steps:
1. Check required parameters:
   - Title is not empty
   - Workspace ID is provided
   - Due date is valid format (YYYY-MM-DD)
2. If validation fails, ask user to provide missing data
3. If validation passes, call MCP tool
4. Handle tool errors gracefully
```

## Response Handling

### Success Responses

```markdown
Steps:
1. Call MCP tool
2. On success:
   - Extract relevant data from response
   - Format for user display
   - Provide confirmation message
   - Include relevant links or IDs
```

### Error Responses

```markdown
Steps:
1. Call MCP tool
2. On error:
   - Check error type (auth, rate limit, validation, etc.)
   - Provide helpful error message
   - Suggest remediation steps
   - Don't expose internal error details to user
```

### Partial Success

```markdown
Steps:
1. Batch operation with multiple MCP calls
2. Track successes and failures separately
3. Report summary:
   - "Successfully processed 8 of 10 items"
   - "Failed items: [item1, item2] due to [reason]"
   - Suggest retry or manual intervention
```

## Performance Optimization

### Batching Requests

**Good: Single query with filters**
```markdown
Steps:
1. Call the search tool with filters:
   - project_id: "123"
   - status: "active"
   - limit: 100
2. Process all results
```

**Avoid: Many individual queries**
```markdown
Steps:
1. For each item ID:
   - Call the get tool
   - Process item
```

### Caching Results

```markdown
Steps:
1. Call expensive MCP operation
2. Store results in variable for reuse
3. Use cached results for subsequent operations
4. Only re-fetch if data changes
```

### Parallel Tool Calls

When tools don't depend on each other, call in parallel:

```markdown
Steps:
1. Make parallel calls (the AI handles this automatically):
   - Get project data
   - Get user data
   - Get tag data
2. Wait for all to complete
3. Combine results
```

## Integration Best Practices

### User Experience

**Provide feedback:**
```markdown
Steps:
1. Inform user: "Searching tasks..."
2. Call the search tool
3. Show progress: "Found 15 tasks, analyzing..."
4. Present results
```

**Handle long operations:**
```markdown
Steps:
1. Warn user: "This may take a minute..."
2. Break into smaller steps with updates
3. Show incremental progress
4. Final summary when complete
```

### Error Messages

**Good error messages:**
```
"Could not create task. Please check:
1. You're logged into the service
2. You have access to workspace 'Engineering'
3. The project 'Q1 Goals' exists"
```

**Poor error messages:**
```
"Error: MCP tool returned 403"
```

### Documentation

**Document MCP tool usage in skills:**
```markdown
## MCP Tools Used

This skill uses the following MCP tools:
- **search_tasks**: Search for tasks matching criteria
- **create_task**: Create new task with details
- **update_task**: Update existing task properties

Ensure you're authenticated before running this skill.
```

## Testing Tool Usage

### Local Testing

1. **Configure MCP server** in `.mcp.json`
2. **Verify tools available** with `/mcp`
3. **Test skill** that uses tools
4. **Check debug output** for connection issues

### Test Scenarios

**Test successful calls:**
```markdown
Steps:
1. Create test data in external service
2. Run skill that queries this data
3. Verify correct results returned
```

**Test error cases:**
```markdown
Steps:
1. Test with missing authentication
2. Test with invalid parameters
3. Test with non-existent resources
4. Verify graceful error handling
```

**Test edge cases:**
```markdown
Steps:
1. Test with empty results
2. Test with maximum results
3. Test with special characters
4. Test with concurrent access
```

## Common Patterns

### Pattern: CRUD Operations

```markdown
# Item Management

## Create
Use the create tool with required fields...

## Read
Use the read tool with item ID...

## Update
Use the update tool with item ID and changes...

## Delete
Use the delete tool with item ID (ask for confirmation first)...
```

### Pattern: Search and Process

```markdown
Steps:
1. **Search**: Use the search tool with filters
2. **Filter**: Apply additional local filtering if needed
3. **Transform**: Process each result
4. **Present**: Format and display to user
```

### Pattern: Multi-Step Workflow

```markdown
Steps:
1. **Setup**: Gather all required information
2. **Validate**: Check data completeness
3. **Execute**: Chain of MCP tool calls:
   - Create parent resource
   - Create child resources
   - Link resources together
   - Add metadata
4. **Verify**: Confirm all steps succeeded
5. **Report**: Provide summary to user
```

## Troubleshooting

### Tools Not Available

**Check:**
- MCP server configured correctly in `.mcp.json`
- Server connected (check `/mcp`)
- Tool names match exactly (case-sensitive)
- Restart CLI after config changes

### Tool Calls Failing

**Check:**
- Authentication is valid
- Parameters match tool schema
- Required parameters provided
- Check debug logs

### Performance Issues

**Check:**
- Batching queries instead of individual calls
- Caching results when appropriate
- Not making unnecessary tool calls
- Parallel calls when possible

## Conclusion

Effective MCP tool usage requires:
1. **Understanding tool schemas** via `/mcp`
2. **Handling errors gracefully**
3. **Optimizing performance** with batching and caching
4. **Providing good UX** with feedback and clear errors
5. **Testing thoroughly** before deployment

Follow these patterns for robust MCP tool integration in your Forge skills.
