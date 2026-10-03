#!/usr/bin/env python3
"""Tests which signer scripts/fetch-release-assets.sh holds the .mcpb to.

The bundle is absent from checksums.txt, so its integrity comes from its
build-provenance attestation alone, and its hash goes into server.json. A check
that names only the repository accepts an attestation any workflow of the
repository minted, at any ref, so the script must hold it to the same signer the
cosign check over checksums.txt names: the release workflow at this release's
tag. The stand-in gh below refuses an attestation verify that does not name it.

Run with `make check-mcpb`, or:

    python3 -m unittest discover -s scripts -p 'fetch_release_assets_sh_test.py'
"""

import hashlib
import os
import shutil
import subprocess
import tarfile
import tempfile
import unittest

ROOT = os.path.abspath(os.path.join(os.path.dirname(os.path.abspath(__file__)), ".."))
SCRIPT = os.path.join(ROOT, "scripts", "fetch-release-assets.sh")
VERSION = "1.2.3"
REPO = "example/libgen-mcp"
SIGNER = "https://github.com/{}/.github/workflows/release.yml@refs/tags/v{}".format(REPO, VERSION)
BINARY = "libgen-mcp-linux-amd64"
BUNDLE = "libgen-mcp.mcpb"

GH = """#!/usr/bin/env bash
echo "gh $*" >> "$CALL_LOG"
if [ "$1 $2" = "release download" ]; then
  dir=""
  while [ $# -gt 0 ]; do
    if [ "$1" = "--dir" ]; then dir=$2; fi
    shift
  done
  cp "$RELEASE_DIR"/* "$dir"/
  exit 0
fi
if [ "$1 $2" = "attestation verify" ]; then
  case " $* " in
    *" --cert-identity $EXPECT_IDENTITY "*) exit 0 ;;
    *) echo "attestation verify names no release workflow signer: $*" >&2; exit 3 ;;
  esac
fi
echo "unexpected gh call: $*" >&2
exit 2
"""

COSIGN = """#!/usr/bin/env bash
echo "cosign $*" >> "$CALL_LOG"
exit 0
"""


class FetchReleaseAssetsBundleTest(unittest.TestCase):
    """The bundle's attestation is held to the release workflow at the tag."""

    def setUp(self):
        if shutil.which("sha256sum") is None:
            self.skipTest("sha256sum is not on PATH")
        self.tmp = tempfile.mkdtemp(prefix="fetch-release-assets-test-")
        self.addCleanup(shutil.rmtree, self.tmp, ignore_errors=True)
        self.bin = os.path.join(self.tmp, "bin")
        self.release = os.path.join(self.tmp, "release")
        self.dest = os.path.join(self.tmp, "dest")
        self.log = os.path.join(self.tmp, "calls.log")
        os.makedirs(self.bin)
        os.makedirs(self.release)
        for name, body in (("gh", GH), ("cosign", COSIGN)):
            path = os.path.join(self.bin, name)
            with open(path, "w", encoding="utf-8") as fh:
                fh.write(body)
            os.chmod(path, 0o755)
        binary = b"\x7fELF release binary"
        with open(os.path.join(self.release, BINARY), "wb") as fh:
            fh.write(binary)
        with open(os.path.join(self.release, "checksums.txt"), "w", encoding="utf-8") as fh:
            fh.write("{}  {}\n".format(hashlib.sha256(binary).hexdigest(), BINARY))
        with open(os.path.join(self.release, "checksums.txt.sigstore.json"), "w", encoding="utf-8") as fh:
            fh.write("{}\n")
        with open(os.path.join(self.release, BUNDLE), "wb") as fh:
            fh.write(b"PK bundle")

    def fetch(self, archive=None):
        """fetch runs the script against the stand-ins and returns the result."""
        env = dict(os.environ)
        env.update(
            PATH=self.bin + os.pathsep + env.get("PATH", ""),
            CALL_LOG=self.log,
            RELEASE_DIR=self.release,
            REPO=REPO,
            EXPECT_IDENTITY=SIGNER,
        )
        env.pop("REHEARSAL_ARCHIVE", None)
        if archive is not None:
            env["REHEARSAL_ARCHIVE"] = archive
        return subprocess.run(["bash", SCRIPT, VERSION, self.dest, BINARY, BUNDLE],
                              env=env, capture_output=True, text=True, check=False)

    def calls(self, tool):
        """calls returns the logged invocations of one stand-in."""
        if not os.path.exists(self.log):
            return []
        with open(self.log, encoding="utf-8") as fh:
            return [line.strip() for line in fh if line.startswith(tool + " ")]

    def test_the_bundle_is_held_to_the_signer_cosign_holds_checksums_to(self):
        result = self.fetch()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        attestations = [c for c in self.calls("gh") if c.startswith("gh attestation verify")]
        self.assertEqual(len(attestations), 1, attestations)
        self.assertIn("--cert-identity " + SIGNER, attestations[0])
        cosign = self.calls("cosign")
        self.assertEqual(len(cosign), 1, cosign)
        self.assertIn("--certificate-identity " + SIGNER, cosign[0])

    def test_a_rehearsal_asks_for_no_attestation(self):
        archive = os.path.join(self.tmp, "rehearsal-assets.tar")
        with tarfile.open(archive, "w") as tar:
            for name in ("checksums.txt", BINARY, BUNDLE):
                tar.add(os.path.join(self.release, name), arcname=name)
        result = self.fetch(archive=archive)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(self.calls("gh"), [])
        self.assertIn("its attestation is minted at release", result.stdout)


if __name__ == "__main__":
    unittest.main()
