#!/usr/bin/env python3
"""Tests the macOS wheel tag in scripts/build_pypi.py and scripts/validate_pypi.py.

pip installs a macosx_<major>_<minor> wheel on any macOS at or above the tag,
and dyld refuses to start a binary on a macOS below the minimum it declares in
its LC_BUILD_VERSION load command. So a tag below the binary's floor is a
wheel that installs and then cannot run, which is what the 11.0 tag was for a
Go toolchain that writes 13.0. Pinned here:

  * both Mach-O readers, the builder's and the validator's, read the floor
    from the load commands a linker writes, and decline what is not a thin
    64-bit macOS binary;
  * the real toolchain's darwin/amd64 and darwin/arm64 builds declare exactly
    MACOS_MINIMUM, so a toolchain bump that moves the floor fails here before
    a release does (skipped without Go off CI, failed without it on CI);
  * the builder refuses a binary whose floor disagrees with the tag, and the
    validator refuses a wheel whose tag disagrees with its binary;
  * the builder's, the validator's and the published-package verifier's
    tables name the same macOS tags.

Run with `make check-pypi`, or:

    python3 -m unittest discover -s scripts -p 'build_pypi_test.py'
"""

import importlib.util
import os
import shutil
import struct
import subprocess
import tempfile
import unittest

ROOT = os.path.abspath(os.path.join(os.path.dirname(os.path.abspath(__file__)), ".."))


def load(name):
    """load imports a script by path; scripts/ is not a package."""
    spec = importlib.util.spec_from_file_location(name, os.path.join(ROOT, "scripts", name + ".py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


build_pypi = load("build_pypi")
validate_pypi = load("validate_pypi")
vpp = load("verify_published_packages")

GO = shutil.which("go")
VERSION = "9.9.9"
NOTICES = validate_pypi.NOTICES_HEADER + b"\nstand-in notices\n"


def packed(major, minor, patch=0):
    """packed encodes a version the way Mach-O does, xxxx.yy.zz in one word."""
    return (major << 16) | (minor << 8) | patch


def macho(commands, cputype=0x0100000C, pad_to=0):
    """macho writes a thin 64-bit Mach-O header followed by the given load
    commands, each a (cmd, payload) pair, padded with zeros to pad_to bytes."""
    body = b""
    for cmd, payload in commands:
        body += struct.pack("<II", cmd, 8 + len(payload)) + payload
    header = struct.pack("<IIIIIIII", 0xFEEDFACF, cputype, 0, 2, len(commands), len(body), 0, 0)
    data = header + body
    return data + bytes(max(0, pad_to - len(data)))


def build_version(major, minor, patch=0, platform=1):
    """build_version is an LC_BUILD_VERSION command for the given platform."""
    return (0x32, struct.pack("<IIII", platform, packed(major, minor, patch), packed(26, 0), 0))


def version_min(major, minor):
    """version_min is the older LC_VERSION_MIN_MACOSX command."""
    return (0x24, struct.pack("<II", packed(major, minor), packed(26, 0)))


# name -> (bytes, the floor both readers must report)
FIXTURES = {
    "build version 13.0": (macho([(0x19, bytes(64)), build_version(13, 0)]), (13, 0, 0)),
    "build version 14.2.1": (macho([build_version(14, 2, 1)]), (14, 2, 1)),
    "version min 10.15": (macho([version_min(10, 15)]), (10, 15, 0)),
    "iOS build version": (macho([build_version(16, 0, platform=2)]), None),
    "no version command": (macho([(0x19, bytes(64))]), None),
    "ELF": (b"\x7fELF" + bytes(60), None),
    "truncated": (macho([build_version(13, 0)])[:40], None),
    "empty": (b"", None),
}


class MachOReaderTest(unittest.TestCase):
    """Both readers report the floor a linker writes, and nothing otherwise."""

    def test_both_readers_agree_with_the_fixture(self):
        for name, (data, want) in FIXTURES.items():
            with self.subTest(name):
                self.assertEqual(build_pypi.macho_minimum_macos(data), want)
                self.assertEqual(validate_pypi.macos_minimum(data), want)


class ToolchainFloorTest(unittest.TestCase):
    """The toolchain this repository builds with declares MACOS_MINIMUM."""

    def test_darwin_builds_declare_the_tagged_minimum(self):
        if GO is None:
            if os.environ.get("CI"):
                self.fail("go is not on PATH, and on CI this case must run")
            self.skipTest("go is not on PATH")
        with tempfile.TemporaryDirectory() as tmp:
            module = os.path.join(tmp, "fixture-src")
            os.makedirs(module)
            with open(os.path.join(module, "go.mod"), "w") as f:
                f.write("module example.com/fixture\n\ngo 1.27\n")
            with open(os.path.join(module, "main.go"), "w") as f:
                f.write("package main\n\nfunc main() {}\n")
            for goarch in ("amd64", "arm64"):
                with self.subTest(goarch):
                    out = os.path.join(tmp, "fixture-darwin-" + goarch)
                    env = dict(os.environ, CGO_ENABLED="0", GOOS="darwin", GOARCH=goarch, GOFLAGS="")
                    subprocess.run([GO, "build", "-trimpath", "-ldflags", "-s -w", "-o", out, "."],
                                   cwd=module, env=env, check=True, capture_output=True)
                    with open(out, "rb") as fh:
                        minos = build_pypi.macho_minimum_macos(fh.read())
                    self.assertEqual(minos, build_pypi.MACOS_MINIMUM + (0,),
                                     "the toolchain moved the macOS floor: update MACOS_MINIMUM, "
                                     "the tag tables and the docs together")


class BuilderRefusalTest(unittest.TestCase):
    """build_pypi.py packs a darwin binary only under its own floor."""

    def write(self, tmp, data):
        path = os.path.join(tmp, "libgen-mcp-darwin-arm64")
        with open(path, "wb") as fh:
            fh.write(data)
        return path

    def test_a_binary_at_the_tagged_floor_is_accepted(self):
        with tempfile.TemporaryDirectory() as tmp:
            major, minor = build_pypi.MACOS_MINIMUM
            build_pypi.check_macos_minimum(self.write(tmp, macho([build_version(major, minor)])))

    def test_a_binary_off_the_tagged_floor_is_refused(self):
        cases = {
            "an older floor": macho([build_version(11, 0)]),
            "a newer floor": macho([build_version(14, 0)]),
            "a patch release": macho([build_version(build_pypi.MACOS_MINIMUM[0], build_pypi.MACOS_MINIMUM[1], 1)]),
            "no floor at all": macho([(0x19, bytes(64))]),
        }
        for name, data in cases.items():
            with self.subTest(name):
                with tempfile.TemporaryDirectory() as tmp:
                    with self.assertRaises(SystemExit) as raised:
                        build_pypi.check_macos_minimum(self.write(tmp, data))
                    self.assertIn("macOS", str(raised.exception))

    def test_the_darwin_tags_name_the_floor(self):
        major, minor = build_pypi.MACOS_MINIMUM
        self.assertEqual(build_pypi.PLATFORMS["darwin-amd64"], "macosx_{}_{}_x86_64".format(major, minor))
        self.assertEqual(build_pypi.PLATFORMS["darwin-arm64"], "macosx_{}_{}_arm64".format(major, minor))


class ValidatorTest(unittest.TestCase):
    """validate_pypi.py holds a macOS wheel's tag to its binary's floor."""

    def setUp(self):
        validate_pypi.failures.clear()
        self.addCleanup(validate_pypi.failures.clear)
        with open(os.path.join(ROOT, "pypi", "README.md"), encoding="utf-8") as fh:
            self.readme = fh.read()
        self.licenses = build_pypi.read_licenses(build_pypi.LICENSE_FILES) + [(build_pypi.NOTICES, NOTICES)]

    def validate(self, tag, data):
        """validate packs data into a wheel tagged tag and returns the failures."""
        with tempfile.TemporaryDirectory() as tmp:
            binary = os.path.join(tmp, "libgen-mcp-darwin-arm64")
            with open(binary, "wb") as fh:
                fh.write(data)
            out = os.path.join(tmp, "dist")
            os.makedirs(out)
            name = build_pypi.build_wheel(out, VERSION, "darwin-arm64", tag, binary, self.readme, self.licenses)
            validate_pypi.validate_wheel(os.path.join(out, name), VERSION, tag)
        return list(validate_pypi.failures)

    def test_a_tag_naming_the_binary_floor_passes(self):
        data = macho([build_version(13, 0)], pad_to=validate_pypi.MIN_BINARY_BYTES)
        self.assertEqual(self.validate("macosx_13_0_arm64", data), [])

    def test_a_tag_below_the_binary_floor_is_refused(self):
        data = macho([build_version(13, 0)], pad_to=validate_pypi.MIN_BINARY_BYTES)
        problems = self.validate("macosx_11_0_arm64", data)
        self.assertEqual(len(problems), 1, problems)
        self.assertIn("tagged for macOS 11.0, but the embedded binary needs macOS 13.0.0", problems[0])

    def test_a_binary_with_no_floor_is_refused(self):
        data = macho([(0x19, bytes(64))], pad_to=validate_pypi.MIN_BINARY_BYTES)
        problems = self.validate("macosx_13_0_arm64", data)
        self.assertEqual(len(problems), 1, problems)
        self.assertIn("cannot compare", problems[0])


class TablesTest(unittest.TestCase):
    """The three tables that spell the macOS tags agree."""

    def test_every_built_tag_is_expected_and_verifiable(self):
        for plat_key, tag in build_pypi.PLATFORMS.items():
            with self.subTest(plat_key):
                self.assertIn(tag, validate_pypi.EXPECTED_TAGS)
                self.assertEqual(validate_pypi.TAG_PLAT_KEYS[tag], plat_key)
                filename = "libgen_mcp-{}-py3-none-{}.whl".format(VERSION, tag)
                asset = next((a for frag, a in vpp.WHEEL_ASSETS.items() if frag in filename), None)
                suffix = ".exe" if plat_key.startswith("windows") else ""
                self.assertEqual(asset, "libgen-mcp-{}{}".format(plat_key, suffix))

    def test_the_verifier_still_reads_wheels_published_under_the_old_tag(self):
        for arch, asset in (("x86_64", "libgen-mcp-darwin-amd64"), ("arm64", "libgen-mcp-darwin-arm64")):
            with self.subTest(arch):
                filename = "libgen_mcp-2.1.0-py3-none-macosx_11_0_{}.whl".format(arch)
                self.assertEqual(next(a for frag, a in vpp.WHEEL_ASSETS.items() if frag in filename), asset)


if __name__ == "__main__":
    unittest.main()
