#!/usr/bin/env python3
"""Tests scripts/smoke-test-image.sh against a stand-in docker.

The smoke test starts the image on each platform it claims and checks two
things: the binary starts and reports the release's version, and its build
information records that version through -X main.version, which -trimpath
would leave out while --version still answered right. Each refusal is
exercised here with a stand-in for docker first on PATH: `--version` prints
the version FAKE_VERSION names, and `--entrypoint /bin/cat` prints the file
FAKE_BINARY names, or fails the way cat does when the variable is empty.

Run with:

    python3 -m unittest discover -s scripts -p 'smoke_test_image_sh_test.py'
"""

import os
import shutil
import subprocess
import tempfile
import unittest

ROOT = os.path.abspath(os.path.join(os.path.dirname(os.path.abspath(__file__)), ".."))
SCRIPT = os.path.join(ROOT, "scripts", "smoke-test-image.sh")
VERSION = "9.8.7"
IMAGE = "libgen-mcp:smoke-amd64"
PLATFORM = "linux/amd64"

DOCKER = """#!/usr/bin/env bash
entrypoint="" last=""
for arg; do
  last="$arg"
done
while [ $# -gt 0 ]; do
  case "$1" in
    --entrypoint) entrypoint="$2"; shift 2 ;;
    *) shift ;;
  esac
done
if [ -z "$entrypoint" ]; then
  [ "$last" = "--version" ] || { echo "unexpected docker call" >&2; exit 2; }
  echo "libgen-mcp $FAKE_VERSION (commit 0123456)"
  exit 0
fi
case "$last" in
  /usr/local/bin/libgen-mcp) file="$FAKE_BINARY" ;;
  *) echo "unexpected path $last" >&2; exit 2 ;;
esac
if [ -z "$file" ]; then
  echo "cat: can't open '$last': No such file or directory" >&2
  exit 1
fi
cat "$file"
"""


def binary(settings):
    """A stand-in for the image's binary: machine code around the build
    information block Go writes into every binary, one setting per line, the
    way `go version -m` prints it. A build with -trimpath records
    `-trimpath=true` and no -ldflags line at all."""
    block = "".join("build\t" + line + "\n" for line in settings)
    return (
        b"\x7fELF\x02\x01\x01\x00" + bytes(range(256)) * 64
        + b"\npath\tgithub.com/jmrplens/libgen-mcp/v2/cmd/server\n"
        + b"mod\tgithub.com/jmrplens/libgen-mcp/v2\t(devel)\t\n"
        + block.encode() + bytes(range(255, -1, -1)) * 64
    )


def ldflags(version):
    """The -ldflags setting of the Dockerfile's build of one version."""
    return '-ldflags="-s -w -X main.version=' + version + ' -X main.commit=0123456"'


class SmokeTestImageTest(unittest.TestCase):
    """Runs the real script with a stand-in docker."""

    def setUp(self):
        if shutil.which("bash") is None:
            # GitHub sets CI on every step; there a missing tool is a failure.
            if os.environ.get("CI"):
                self.fail("the script needs bash, and CI must run these cases rather than skip them")
            self.skipTest("the script needs bash")
        self.work = tempfile.mkdtemp(prefix="smoke-test-image-")
        self.addCleanup(shutil.rmtree, self.work, True)
        self.bin = os.path.join(self.work, "bin")
        os.makedirs(self.bin)
        docker = os.path.join(self.bin, "docker")
        with open(docker, "w", encoding="utf-8") as fh:
            fh.write(DOCKER)
        os.chmod(docker, 0o755)
        self.binary = self.write_bytes(
            "binary", binary(["-buildmode=exe", "-compiler=gc", ldflags(VERSION), "CGO_ENABLED=0"]))

    def write_bytes(self, name, data):
        path = os.path.join(self.work, name)
        with open(path, "wb") as fh:
            fh.write(data)
        return path

    def smoke(self, version=VERSION, binary_file=None):
        env = dict(os.environ)
        env.update(
            PATH=self.bin + os.pathsep + env.get("PATH", ""),
            FAKE_VERSION=version,
            FAKE_BINARY=self.binary if binary_file is None else binary_file,
        )
        return subprocess.run(
            ["bash", SCRIPT, VERSION, IMAGE + "=" + PLATFORM],
            env=env, capture_output=True, text=True, timeout=60, check=False,
        )

    def test_an_image_recording_its_version_passes(self):
        result = self.smoke()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("build information records -X main.version=9.8.7", result.stdout)
        self.assertIn("record it in their build information", result.stdout)

    def test_refuses_each_departure(self):
        not_recorded = "has a binary whose build information does not record -X main.version=9.8.7"
        cases = [
            ("another version", dict(version="1.0.0"), "started but printed"),
            ("no binary to read", dict(binary_file=""),
             "carries no /usr/local/bin/libgen-mcp to read"),
            ("a binary built with -trimpath",
             dict(binary_file=self.write_bytes(
                 "trimpath", binary(["-buildmode=exe", "-compiler=gc", "-trimpath=true", "CGO_ENABLED=0"]))),
             not_recorded),
            ("a binary recording another version",
             dict(binary_file=self.write_bytes("other", binary([ldflags("1.0.0")]))), not_recorded),
            ("a binary recording a version that only begins with it",
             dict(binary_file=self.write_bytes("longer", binary([ldflags("9.8.70")]))), not_recorded),
        ]
        for name, change, want in cases:
            with self.subTest(name):
                result = self.smoke(**change)
                self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
                self.assertIn(want, result.stderr)
                self.assertIn("1 image(s) failed the smoke test", result.stderr)


if __name__ == "__main__":
    unittest.main()
