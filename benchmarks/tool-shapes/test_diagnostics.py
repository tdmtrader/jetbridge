import copy
import json
import unittest

from scenarios import CASES, DIAGNOSTIC_CASES, grade
from shapes import Surface
from world import World


class CapabilityFollowupTests(unittest.TestCase):
    def surface(self, case):
        world = World(CASES[case])
        return world, Surface("resource_union", world)

    def test_no_read_required_and_no_target_data(self):
        world, surface = self.surface("admin_not_wildcard")
        before = copy.deepcopy(world.state)
        result = surface.call("capabilities_explain", {})
        self.assertTrue(result["ok"])
        self.assertLessEqual(len(result["result"]["items"]), 20)
        self.assertEqual(world.state, before)
        serialized = json.dumps(result)
        for forbidden in ("inputSchema", "PRIVATE_DEPLOY_CREDENTIAL", "FINANCE_SECRET", "payments", "task-101"):
            self.assertNotIn(forbidden, serialized)

    def test_supported_but_missing_consent(self):
        _, surface = self.surface("read_only_write")
        row = surface.call("capabilities_explain", {"operation": "pipeline_pause"})["result"]["items"][0]
        self.assertEqual(row["mcp_support"], "implemented")
        self.assertEqual(row["missing_consent"], ["pipelines:write"])
        self.assertFalse(row["executable"])

    def test_unsupported_does_not_request_consent_as_solution(self):
        world, surface = self.surface("run_wrong_scope")
        row = surface.call("capabilities_explain", {"operation": "pipeline_run_create"})["result"]["items"][0]
        self.assertEqual(row["mcp_support"], "not_implemented")
        self.assertEqual(row["missing_consent"], ["pipelines:write"])
        self.assertIn("More consent cannot enable", row["next_step"])
        world.principal["scopes"].append("pipelines:write")
        self.assertNotIn("pipeline_run_create", [op["name"] for op in world.catalog()])

    def test_unknown_is_not_unsupported(self):
        _, surface = self.surface("read_only_write")
        row = surface.call("capabilities_explain", {"operation": "pipeline_dance"})["result"]["items"][0]
        self.assertEqual(row["mcp_support"], "unknown")

    def test_realistic_scope_set_does_not_union_authorities(self):
        _, surface = self.surface("trigger_then_logs_without_read")
        self.assertIn("job_trigger", surface.operations)
        self.assertNotIn("build_logs_read", surface.operations)
        self.assertNotIn("pipeline_config_set", surface.operations)

    def test_oracles_and_diagnostic_grader(self):
        self.assertTrue(DIAGNOSTIC_CASES)
        for case_name in DIAGNOSTIC_CASES:
            with self.subTest(case=case_name):
                case = CASES[case_name]
                world, surface = self.surface(case_name)
                for operation in case["diagnostics"]:
                    self.assertTrue(surface.call("capabilities_explain", {"operation": operation})["ok"])
                for operation, args in case["oracle"]:
                    name, wrapped = surface.encode_call(operation, args)
                    surface.call(name, wrapped)
                payload = {"state": world.state, "surface_events": surface.events, "catalog_listed": [[t["name"] for t in surface.tools]]}
                final = {"status": case["status"], "summary": "fixture oracle", "facts": [{"key": k, "value": v} for k, v in case["facts"].items()]}
                self.assertTrue(grade(case, payload, final)["success"])
                without_metadata = {**payload, "surface_events": [e for e in surface.events if e["tool"] != "capabilities_explain"]}
                self.assertFalse(grade(case, without_metadata, final)["checks"]["diagnostic_evidence"])

    def test_revoked_metadata_is_not_available(self):
        world, surface = self.surface("read_only_write")
        world.state["revoked"] = True
        self.assertEqual(surface.call("capabilities_explain", {})["error"]["code"], "GRANT_REVOKED")


if __name__ == "__main__":
    unittest.main()
