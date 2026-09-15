#!/usr/bin/env python3
"""Verify redacted saved evidence; performs no login, model call or network I/O."""
import hashlib
import json
import sys
from pathlib import Path

root = Path(sys.argv[1]).resolve()
data = json.loads((root/"evidence.json").read_text())
assert not data.get("failure"), data.get("failure")
assert data["only_fixture_enabled"] and not data["model_inference"]
assert data["fixture_cleanup"] and data["local_test_credential_logout_exit"] == 0
events = data["http_events"]
assert events
authorizations = [e for e in events if e["path"] == "/mcp/oauth/authorize"]
assert len(authorizations) == 2
assert all(e["redirect_uri"] == data["exact_callback"] and e["pkce_method"] == "S256" and e["resource_matches"] for e in authorizations)
assert all("register" not in e["path"] for e in events)
tokens = [e for e in events if e["path"] == "/mcp/oauth/token"]
assert all(e["resource_matches"] and e["client_id"] == data["client_id"] for e in tokens)
codes = [e for e in tokens if e["grant_type"] == "authorization_code"]
assert [e["granted_scope"] for e in codes] == ["read", "read pipelines:write"]
assert all(e["status"] == 200 and e["refresh_present"] for e in codes)
renewals = [e for e in tokens if e["grant_type"] == "refresh_token" and e["status"] == 200]
assert len(renewals) == 2 and all(e["refresh_rotated"] for e in renewals)
assert any(e["grant_type"] == "refresh_token" and e["status"] == 400 and e["oauth_error"] == "invalid_grant" for e in tokens)
initializes = [e for e in events if e.get("rpc_method") == "initialize" and e["status"] == 200]
assert initializes and all(e["requested_protocol"] == e["negotiated_protocol"] == "2025-06-18" for e in initializes)
calls = [e for e in events if e.get("rpc_method") == "tools/call"]
assert len(calls) == 7 and all(e["operation"] == "pipeline_get" for e in calls)
assert sum(e["status"] == 200 and not e["tool_error"] for e in calls) == 4
assert sum(e["status"] == 401 for e in calls) == 3
steps = {s["step"]:s for s in data["steps"] if s["step"] != "login"}
for label in ("read_only_group_call", "expired_access_group_call", "rotated_credential_process_restart_read", "new_consent_group_call"):
    result = steps[label]["response"]["result"]["structuredContent"]
    assert result["operation"] == "pipeline_get"
    assert result["result"]["team"] == "auth-team" and result["result"]["pipeline"] == "private"
assert "error" in steps["revoked_group_call"]["response"]
assert steps["server_grant_revocation"]["revoked"] == steps["final_server_grant_revocation"]["revoked"] == 1
pipeline_lists = []
for event in events:
    if event.get("rpc_method") == "tools/list" and event["status"] == 200:
        pipeline = next(t for t in event["tools"] if t["name"] == "pipeline")
        schema = pipeline["inputSchema"]
        assert schema["type"] == "object" and "request" in schema["required"]
        operations = {b["properties"]["operation"]["const"] for b in schema["properties"]["request"]["oneOf"]}
        assert "pipeline_get" in operations
        pipeline_lists.append((operations,pipeline["annotations"].get("readOnlyHint",False)))
assert pipeline_lists
assert any(readonly and "pipeline_pause" not in operations for operations,readonly in pipeline_lists)
assert any(not readonly and {"pipeline_pause","pipeline_config_set"} <= operations for operations,readonly in pipeline_lists)
for name,digest in data["source_sha256"].items():
    assert hashlib.sha256((root/"source"/Path(name).name).read_bytes()).hexdigest() == digest
verification = {"real_oauth_login":True,"exact_callback":True,"profile":"2025-06-18","direct_group_reads":4,
                "automatic_401_refreshes":2,"refresh_after_process_restart":True,"revocation_rejected":True,
                "new_consent_relisted":True,"mixed_group_annotation_observed":True,
                "no_model_inference":True,"frozen_source_hashes_match":True,"cleanup_verified":True}
verifier_source = Path(__file__).read_bytes()
verification["verifier_sha256"] = hashlib.sha256(verifier_source).hexdigest()
(root/"verification-source.py").write_bytes(verifier_source)
(root/"verification.json").write_text(json.dumps(verification,indent=2)+"\n")
print(json.dumps(verification))
