#!/usr/bin/env python3
"""Tests scripts/audit-site-deps.sh (`make audit-site-deps`).

pnpm exits non-zero both when the site has an advisory rated high or higher and
when the npm advisory endpoint does not answer. The script tells the two apart
by what pnpm printed, and the test holds it to that in both directions: a
registry failure passes with a warning, and a report, a 4xx, or anything it
does not recognise still fails.

Each case runs the real script against a stand-in pnpm that prints one of the
captured outputs in scripts/testdata/audit-site-deps/ and exits with the status
the real pnpm 12.6.0 gave for it. The outputs were taken from pnpm itself: a
lockfile with real findings, a local server answering 503, a closed port and an
unroutable address.

Run with:

    python3 -m unittest discover -s scripts -p 'audit_site_deps_sh_test.py'
"""

import os
import shutil
import stat
import subprocess
import tempfile
import unittest

ROOT = os.path.abspath(os.path.join(os.path.dirname(os.path.abspath(__file__)), ".."))
SCRIPT = os.path.join(ROOT, "scripts", "audit-site-deps.sh")
FIXTURES = os.path.join(ROOT, "scripts", "testdata", "audit-site-deps")

WARNING = "the npm advisory endpoint did not answer"


def audit(fixture, status, github_actions=False):
    """Runs the script against a stand-in pnpm printing fixture and exiting status."""
    with tempfile.TemporaryDirectory() as tmp:
        calls = os.path.join(tmp, "calls")
        stub = os.path.join(tmp, "pnpm")
        with open(stub, "w", encoding="utf-8") as fh:
            fh.write('#!/usr/bin/env bash\nprintf "%s\\n" "$*" > "' + calls + '"\n'
                     'cat "' + os.path.join(FIXTURES, fixture) + '"\nexit ' + str(status) + "\n")
        os.chmod(stub, os.stat(stub).st_mode | stat.S_IXUSR)
        env = dict(os.environ, PNPM=stub, SITE_DIR=tmp)
        env.pop("GITHUB_ACTIONS", None)
        if github_actions:
            env["GITHUB_ACTIONS"] = "true"
        run = subprocess.run(["bash", SCRIPT], env=env, capture_output=True, text=True, check=False)
        with open(calls, encoding="utf-8") as fh:
            asked = fh.read()
    return run.returncode, run.stdout + run.stderr, asked


class AuditSiteDepsTest(unittest.TestCase):
    """The script fails on the tree and never on the registry."""

    def test_it_asks_pnpm_for_high_and_above(self):
        _, _, asked = audit("clean.txt", 0)
        self.assertEqual(asked, "audit --audit-level=high\n")

    def test_a_clean_audit_passes(self):
        status, output, _ = audit("clean.txt", 0)
        self.assertEqual(status, 0, output)
        self.assertNotIn(WARNING, output)

    def test_a_finding_fails(self):
        status, output, _ = audit("finding.txt", 1)
        self.assertEqual(status, 1, output)
        self.assertIn("GHSA-r5fr-rjxr-66jc", output)
        self.assertIn("FAIL: pnpm audit reports an advisory", output)

    def test_the_registry_not_answering_passes_with_a_warning(self):
        for fixture in ["status-503.txt", "status-503-wrapped.txt", "connection-refused.txt", "timed-out.txt"]:
            with self.subTest(fixture=fixture):
                status, output, _ = audit(fixture, 1)
                self.assertEqual(status, 0, output)
                self.assertIn("WARNING: " + WARNING, output)

    def test_the_warning_is_an_annotation_under_actions(self):
        status, output, _ = audit("status-503.txt", 1, github_actions=True)
        self.assertEqual(status, 0, output)
        self.assertIn("::warning title=Dependency audit did not run::" + WARNING, output)

    def test_a_4xx_is_a_finding_about_the_request(self):
        status, output, _ = audit("status-404.txt", 1)
        self.assertEqual(status, 1, output)
        self.assertIn("without a report or a recognisable registry failure", output)

    def test_a_report_is_never_excused_by_registry_words_beside_it(self):
        with tempfile.TemporaryDirectory() as tmp:
            mixed = os.path.join(tmp, "mixed.txt")
            with open(os.path.join(FIXTURES, "finding.txt"), encoding="utf-8") as fh:
                report = fh.read()
            with open(mixed, "w", encoding="utf-8") as fh:
                fh.write("socket hang up while fetching metadata, retried\n" + report)
            # The stub reads the fixture by absolute path, so the mixed file can
            # live outside the fixture directory.
            status, output, _ = audit(mixed, 1)
        self.assertEqual(status, 1, output)
        self.assertNotIn(WARNING, output)

    def test_a_coloured_report_is_still_a_finding(self):
        # pnpm colours the summary on a terminal: "\x1b[31m2\x1b[39m
        # vulnerabilities found" is the line a developer's run printed.
        with tempfile.TemporaryDirectory() as tmp:
            coloured = os.path.join(tmp, "coloured.txt")
            with open(coloured, "w", encoding="utf-8") as fh:
                fh.write("\x1b[31;1mhigh\x1b[0m advisory\n"
                         "\x1b[31m2\x1b[39m vulnerabilities found\n"
                         "Severity: \x1b[1m1 low\x1b[0m | \x1b[31;1m1 high\x1b[0m\n")
            status, output, _ = audit(coloured, 1)
        self.assertEqual(status, 1, output)
        self.assertIn("FAIL: pnpm audit reports an advisory", output)

    def test_an_unrecognised_failure_fails(self):
        with tempfile.TemporaryDirectory() as tmp:
            other = os.path.join(tmp, "other.txt")
            with open(other, "w", encoding="utf-8") as fh:
                fh.write("Error: ERR_PNPM_LOCKFILE_BROKEN\n")
            status, output, _ = audit(other, 1)
        self.assertEqual(status, 1, output)

    def test_script_passes_shellcheck(self):
        shellcheck = shutil.which("shellcheck")
        if shellcheck is None:
            if os.environ.get("CI"):
                self.fail("shellcheck is not installed, and CI must run this check rather than skip it")
            self.skipTest("shellcheck is not installed")
        result = subprocess.run([shellcheck, SCRIPT], capture_output=True, timeout=60, check=False)
        self.assertEqual(result.returncode, 0, result.stdout.decode() + result.stderr.decode())


if __name__ == "__main__":
    unittest.main()
