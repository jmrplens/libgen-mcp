#!/usr/bin/env python3
"""Tests scripts/check-pr-description.sh (`make check-pr-description`).

This repository squash merges, so a pull request's title and body become the
commit message on main, where no later commit can take them back out. The gate
refuses three things there: a command that makes GitHub skip workflows, a block
a review bot injected, and an assistant's attribution.

The bodies live in scripts/testdata/pr-description/, one file per case:
everything under refused/ must fail with the rule named beside it below, and
everything under accepted/ must pass. A fixture that is added without a rule
fails the suite rather than going unjudged. The cases that are not a body on
its own (the title, the three places the text can come from, a body in CRLF,
a byte invalid in UTF-8) are written inline.

Run with:

    python3 -m unittest discover -s scripts -p 'check_pr_description_sh_test.py'
"""

import os
import shutil
import stat
import subprocess
import tempfile
import unittest

ROOT = os.path.abspath(os.path.join(os.path.dirname(os.path.abspath(__file__)), ".."))
SCRIPT = os.path.join(ROOT, "scripts", "check-pr-description.sh")
FIXTURES = os.path.join(ROOT, "scripts", "testdata", "pr-description")

SKIP = "carries a command that makes GitHub skip workflows"
INJECTED = "carries a block a review bot generated"
ATTRIBUTION = "attributes the change to an assistant"
OK = "OK: no skip command, no injected block and no assistant attribution."

# Each refused fixture, with the rule it must fail and the line it must name.
REFUSED = {
    "skip-ci-in-backticks.txt": (SKIP, "body:3:"),
    "skip-actions-spaced-mixed-case.txt": (SKIP, "body:3:"),
    "no-ci-in-a-code-fence.txt": (SKIP, "body:4:"),
    "skip-checks-trailer.txt": (SKIP, "body:3:"),
    "coderabbit-summary.txt": (INJECTED, "body:5:"),
    "cubic-summary.txt": (INJECTED, "body:10:"),
    "walkthrough-markers.txt": (INJECTED, "body:3:"),
    "co-authored-by-a-model.txt": (ATTRIBUTION, "body:3:"),
    "co-authored-by-copilot.txt": (ATTRIBUTION, "body:3:"),
    "generated-with-footer.txt": (ATTRIBUTION, "body:5:"),
    "generated-by-ai.txt": (ATTRIBUTION, "body:3:"),
    "session-link.txt": (ATTRIBUTION, "body:3:"),
    "chatgpt-share-link.txt": (ATTRIBUTION, "body:2:"),
}


def require_tool(test, name):
    """Returns the path of a tool, failing under CI and skipping elsewhere when it is missing."""
    path = shutil.which(name)
    if path is None:
        # A skip keeps the job green, so on a runner without the tool this
        # check would stop running and nobody would be told. GitHub sets CI on
        # every step; there a missing tool is a failure.
        if os.environ.get("CI"):
            test.fail(name + " is not installed, and CI must run this check rather than skip it")
        test.skipTest(name + " is not installed")
    return path


def clean_env(**extra):
    """Returns the environment with every input of the script removed, then extra added."""
    env = dict(os.environ)
    for name in ("PR_TITLE_FILE", "PR_BODY_FILE", "PR_TITLE", "PR_BODY", "PR_NUMBER", "PR_SOURCE_LABEL"):
        env.pop(name, None)
    env.update(extra)
    return env


def run_script(env):
    """Runs the gate under env, returning (status, stdout and stderr together)."""
    run = subprocess.run(["bash", SCRIPT], env=env, capture_output=True, check=False)
    return run.returncode, (run.stdout + run.stderr).decode("utf-8", "replace")


def judge_files(title, body_path):
    """Runs the gate over a title and a body file, returning (status, output)."""
    with tempfile.TemporaryDirectory() as tmp:
        title_file = os.path.join(tmp, "title")
        with open(title_file, "w", encoding="utf-8") as fh:
            fh.write(title)
        return run_script(clean_env(PR_TITLE_FILE=title_file, PR_BODY_FILE=body_path))


def judge(title, body):
    """Runs the gate over a title and a body handed over in the environment, as CI does."""
    return run_script(clean_env(PR_TITLE=title, PR_BODY=body, PR_SOURCE_LABEL="the test"))


class FixtureTest(unittest.TestCase):
    """Every body in the fixture tree is judged the way its directory says."""

    def test_every_refused_fixture_has_a_rule(self):
        present = set(os.listdir(os.path.join(FIXTURES, "refused")))
        self.assertEqual(present, set(REFUSED), "a refused fixture and the REFUSED table disagree")

    def test_refused_fixtures_fail_with_their_rule_and_line(self):
        for name, (rule, location) in sorted(REFUSED.items()):
            with self.subTest(fixture=name):
                status, output = judge_files("A title", os.path.join(FIXTURES, "refused", name))
                self.assertEqual(status, 1, output)
                self.assertIn(rule, output)
                self.assertIn(location, output)
                self.assertIn("Judging the pull request title and body (files on disk).", output)

    def test_accepted_fixtures_pass(self):
        directory = os.path.join(FIXTURES, "accepted")
        names = sorted(os.listdir(directory))
        self.assertTrue(names, "no accepted fixture, so nothing proves the gate lets a description through")
        for name in names:
            with self.subTest(fixture=name):
                status, output = judge_files("fix(download): stop retrying a declined source",
                                             os.path.join(directory, name))
                self.assertEqual(status, 0, output)
                self.assertIn(OK, output)


class InlineTest(unittest.TestCase):
    """The cases that are more than a body on its own."""

    def test_the_environment_is_the_source_ci_uses(self):
        status, output = judge("A title", "Plain prose.\n")
        self.assertEqual(status, 0, output)
        self.assertIn("(the test)", output)

    def test_an_empty_body_passes(self):
        status, output = judge("A title", "")
        self.assertEqual(status, 0, output)

    def test_a_skip_command_in_the_title_fails(self):
        status, output = judge("chore: regenerate the pages [ci skip]", "body\n")
        self.assertEqual(status, 1, output)
        self.assertIn("title: chore: regenerate the pages [ci skip]", output)
        self.assertIn(SKIP, output)

    def test_attribution_in_the_title_fails(self):
        status, output = judge("Generated with Claude Code", "body\n")
        self.assertEqual(status, 1, output)
        self.assertIn("title: Generated with Claude Code", output)
        self.assertIn(ATTRIBUTION, output)

    def test_every_skip_command_fails(self):
        for command in ["[skip ci]", "[ci skip]", "[no ci]", "[skip actions]", "[actions skip]"]:
            with self.subTest(command=command):
                status, output = judge("A title", "first\nquoting " + command + "\n")
                self.assertEqual(status, 1, output)
                self.assertIn("body:2:quoting " + command, output)

    def test_a_line_carrying_two_forms_is_reported_once(self):
        status, output = judge("A title", "skip-checks: true [skip ci]\n")
        self.assertEqual(status, 1, output)
        self.assertEqual(output.count("body:1:"), 1, output)

    def test_every_finding_is_reported_in_one_run(self):
        status, output = judge(
            "A title",
            "commits with [skip ci]\n## Summary by CodeRabbit\n\nCo-Authored-By: Claude <noreply@anthropic.com>\n",
        )
        self.assertEqual(status, 1, output)
        self.assertIn(SKIP, output)
        self.assertIn(INJECTED, output)
        self.assertIn(ATTRIBUTION, output)

    def test_crlf_line_endings_are_judged_and_printed_without_the_carriage_return(self):
        status, output = judge("A title", "first line\r\n\r\nskip-checks: true\r\n")
        self.assertEqual(status, 1, output)
        self.assertIn("body:3:skip-checks: true\n", output)
        self.assertNotIn("\r", output)

    def test_a_byte_invalid_in_utf8_does_not_hide_the_line(self):
        # grep would call the input binary and print nothing on stdout, so the
        # gate would pass a line it never read; -a keeps it text.
        with tempfile.TemporaryDirectory() as tmp:
            body_file = os.path.join(tmp, "body")
            with open(body_file, "wb") as fh:
                fh.write(b"x\ncaf\xe9 commits with [skip ci]\n")
            status, output = run_script(clean_env(PR_BODY_FILE=body_file, LC_ALL="C.UTF-8"))
        self.assertEqual(status, 1, output)
        self.assertIn("body:2:", output)

    def test_an_argument_is_refused_rather_than_ignored(self):
        run = subprocess.run(["bash", SCRIPT, "unexpected"], env=clean_env(PR_TITLE="t", PR_BODY="b"),
                             capture_output=True, check=False)
        self.assertEqual(run.returncode, 2)
        self.assertIn(b"Usage:", run.stderr)

    def test_the_description_is_read_through_gh_when_nothing_else_is_given(self):
        require_tool(self, "jq")
        with tempfile.TemporaryDirectory() as tmp:
            payload = os.path.join(tmp, "payload.json")
            with open(payload, "w", encoding="utf-8") as fh:
                fh.write('{"title": "A title", "body": "first line\\r\\nthe fixture commits with [skip ci]\\r\\n"}')
            # A stand-in gh that answers the one call the script makes, and
            # records what it was asked so the test can hold the script to it.
            calls = os.path.join(tmp, "calls")
            stub = os.path.join(tmp, "gh")
            with open(stub, "w", encoding="utf-8") as fh:
                fh.write('#!/usr/bin/env bash\nprintf "%s\\n" "$*" >> "' + calls + '"\n'
                         '[[ "$1" == "api" ]] && exec cat "' + payload + '"\nexit 1\n')
            os.chmod(stub, os.stat(stub).st_mode | stat.S_IXUSR)
            env = clean_env(PATH=tmp + os.pathsep + os.environ.get("PATH", ""),
                            PR_NUMBER="7", GITHUB_REPOSITORY="owner/repo")
            status, output = run_script(env)
            with open(calls, encoding="utf-8") as fh:
                asked = fh.read()
        self.assertEqual(asked, "api repos/owner/repo/pulls/7\n")
        self.assertIn("owner/repo#7, read from the API just now", output)
        self.assertEqual(status, 1, output)
        self.assertIn("body:2:the fixture commits with [skip ci]", output)

    def test_script_passes_shellcheck(self):
        shellcheck = require_tool(self, "shellcheck")
        result = subprocess.run([shellcheck, SCRIPT], capture_output=True, timeout=60, check=False)
        self.assertEqual(result.returncode, 0, result.stdout.decode() + result.stderr.decode())


if __name__ == "__main__":
    unittest.main()
