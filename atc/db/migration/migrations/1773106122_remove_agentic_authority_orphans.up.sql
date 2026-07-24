UPDATE agent_principals
SET scopes = array_remove(scopes, 'questions:answer')
WHERE 'questions:answer' = ANY(scopes);

DELETE FROM agent_principals
WHERE name = 'legacy-publish' AND token_hash = '';
