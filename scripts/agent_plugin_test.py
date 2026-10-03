#!/usr/bin/env python3
"""Tests the Agent Plugins package at the repository root: plugin.json and mcp.json.

An Agent Plugins 1.0 host reads plugin.json at the plugin root and mcp.json
beside it (specification section 6.1), and a strict one disables the whole
MCP component when mcp.json breaks a top-level rule (section 7.2.2). mcp.json
once shipped without the required `$schema`, without a per-server `type`, and
with every `env` value written `${NAME:-default}`, which the specification
(section 9.2) requires a host to pass through literally. Passed literally,
`LIBGEN_MCP_TIMEOUT` reads `${LIBGEN_MCP_TIMEOUT:-10s}` and the server refuses
to start, because a value that is set but unparseable is an error here. So
these cases validate both files against the published schemas, vendored under
scripts/testdata/agent-plugins/1.0.0 so the check stays offline (a canonical
schema identifier is never reassigned, section 10.1), and then check what the
schemas cannot say: placeholder use, the command form, and the package the
entry starts.

Run with:

    python3 -m unittest discover -s scripts -p 'agent_plugin_test.py'
"""

import json
import os
import re
import unittest

try:
    import jsonschema
except ImportError:  # pragma: no cover - reported by the test below
    jsonschema = None

ROOT = os.path.abspath(os.path.join(os.path.dirname(os.path.abspath(__file__)), ".."))
SCHEMA_DIR = os.path.join(ROOT, "scripts", "testdata", "agent-plugins", "1.0.0")
PLUGIN_JSON = os.path.join(ROOT, "plugin.json")
MCP_JSON = os.path.join(ROOT, "mcp.json")
OPEN_PLUGINS_JSON = os.path.join(ROOT, ".plugin", "plugin.json")
NPM_LAUNCHER = os.path.join(ROOT, "npm", "libgen-mcp", "package.json")

PLUGIN_SCHEMA_ID = "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json"
MCP_SCHEMA_ID = "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json"

# The two placeholders a host expands (section 9.2). Anything else shaped like
# one reaches the server as literal text.
PLACEHOLDER = re.compile(r"\$\{([^}]*)\}")
EXPANDED = {"PLUGIN_ROOT", "PLUGIN_DATA"}


def load(path):
    """Returns the parsed JSON document at path."""
    with open(path, encoding="utf-8") as fh:
        return json.load(fh)


def stray_placeholders(server):
    """Returns every placeholder-shaped string in a stdio entry's args, env
    values and cwd that a conformant host would leave literal."""
    values = list(server.get("args", []))
    values += list(server.get("env", {}).values())
    if "cwd" in server:
        values.append(server["cwd"])
    return [v for v in values if any(name not in EXPANDED for name in PLACEHOLDER.findall(v))]


def schema_errors(schema_path, document):
    """Returns the validation messages for document against the vendored schema."""
    validator = jsonschema.Draft202012Validator(load(schema_path))
    return [e.message for e in validator.iter_errors(document)]


class AgentPluginTest(unittest.TestCase):
    """Reads the committed plugin.json and mcp.json."""

    @classmethod
    def setUpClass(cls):
        cls.plugin = load(PLUGIN_JSON)
        cls.mcp = load(MCP_JSON)

    def setUp(self):
        if jsonschema is None:
            # A skip would keep the job green with nothing checked, so a
            # missing validator is a failure wherever these cases run.
            self.fail("python3 -m pip install jsonschema: the schema cases need it")

    def test_vendored_schemas_are_the_canonical_ones(self):
        self.assertEqual(load(os.path.join(SCHEMA_DIR, "plugin.schema.json"))["$id"], PLUGIN_SCHEMA_ID)
        self.assertEqual(load(os.path.join(SCHEMA_DIR, "mcp.schema.json"))["$id"], MCP_SCHEMA_ID)

    def test_plugin_json_validates_against_the_published_schema(self):
        self.assertEqual(schema_errors(os.path.join(SCHEMA_DIR, "plugin.schema.json"), self.plugin), [])

    def test_mcp_json_validates_against_the_published_schema(self):
        self.assertEqual(schema_errors(os.path.join(SCHEMA_DIR, "mcp.schema.json"), self.mcp), [])

    def test_both_files_target_the_same_specification_version(self):
        # Section 10.1: a mismatch invalidates the MCP configuration.
        self.assertEqual(self.plugin["$schema"].rsplit("/", 2)[-2], self.mcp["$schema"].rsplit("/", 2)[-2])

    def test_no_value_carries_a_placeholder_the_host_leaves_literal(self):
        for name, server in self.mcp["mcpServers"].items():
            with self.subTest(server=name):
                self.assertEqual(stray_placeholders(server), [])

    def test_no_env_entry_names_a_reserved_variable(self):
        for name, server in self.mcp["mcpServers"].items():
            with self.subTest(server=name):
                self.assertFalse({"PLUGIN_ROOT", "PLUGIN_DATA"} & set(server.get("env", {})))

    def test_the_entry_starts_the_npm_launcher(self):
        # A native process, so the files download saves land on the host and
        # read can open them, which a container started with --rm cannot give.
        server = self.mcp["mcpServers"]["libgen"]
        self.assertEqual(server["type"], "stdio")
        self.assertEqual(server["command"], "npx")
        self.assertEqual(server["args"], ["-y", load(NPM_LAUNCHER)["name"]])

    def test_the_command_is_one_bare_token(self):
        # Section 7.2.1: a bare executable name or a ./ path, never a shell line.
        for name, server in self.mcp["mcpServers"].items():
            with self.subTest(server=name):
                command = server["command"]
                self.assertNotRegex(command, r"\s")
                self.assertTrue(command.startswith("./") or "/" not in command, command)

    def test_the_open_plugins_manifest_points_at_the_same_file(self):
        self.assertEqual(load(OPEN_PLUGINS_JSON)["mcpServers"], "./mcp.json")


class AgentPluginRegressionTest(unittest.TestCase):
    """Holds the checks above to the shape mcp.json used to ship."""

    OLD = {
        "mcpServers": {
            "libgen": {
                "command": "docker",
                "args": ["run", "-i", "--rm", "-e", "LIBGEN_MCP_TIMEOUT", "ghcr.io/jmrplens/libgen-mcp:latest"],
                "env": {"LIBGEN_MCP_TIMEOUT": "${LIBGEN_MCP_TIMEOUT:-10s}"},
            }
        }
    }

    def setUp(self):
        if jsonschema is None:
            self.fail("python3 -m pip install jsonschema: the schema cases need it")

    def test_the_old_file_fails_the_schema(self):
        errors = schema_errors(os.path.join(SCHEMA_DIR, "mcp.schema.json"), self.OLD)
        self.assertIn("'$schema' is a required property", errors)
        self.assertEqual(len(errors), 2, errors)

    def test_the_old_env_values_are_reported_as_literal(self):
        self.assertEqual(stray_placeholders(self.OLD["mcpServers"]["libgen"]), ["${LIBGEN_MCP_TIMEOUT:-10s}"])

    def test_plugin_placeholders_are_not_reported(self):
        server = {"args": ["${PLUGIN_DATA}/x"], "env": {"A": "${PLUGIN_ROOT}"}, "cwd": "${PLUGIN_ROOT}"}
        self.assertEqual(stray_placeholders(server), [])


if __name__ == "__main__":
    unittest.main()
