#!/usr/bin/env python3
"""Tests scripts/check_elf_standalone.py.

The two cases that matter are built by the real toolchain rather than written
by hand: the fixture program built the way the release builds it
(CGO_ENABLED=0, no -buildmode=pie) must be accepted, and the same program built
with -buildmode=pie, which is the regression this check exists for, must be
refused for its PT_INTERP. The rest are ELF files written here byte by byte, to
reach the shapes the toolchain does not produce on demand: a big-endian file,
the extended program header count, and files cut short.

Run with `make check-elf-standalone`, or:

    python3 -m unittest discover -s scripts -p 'check_elf_standalone_test.py'
"""

import importlib.util
import io
import os
import shutil
import struct
import subprocess
import tempfile
import unittest

ROOT = os.path.abspath(os.path.join(os.path.dirname(os.path.abspath(__file__)), ".."))

spec = importlib.util.spec_from_file_location(
    "check_elf_standalone", os.path.join(ROOT, "scripts", "check_elf_standalone.py"))
check = importlib.util.module_from_spec(spec)
spec.loader.exec_module(check)

GO = shutil.which("go")


def build_fixture(directory, name, goarch="amd64", pie=False):
    """build_fixture builds a program with no dependencies for linux/goarch and
    returns the binary's path."""
    module = os.path.join(directory, "fixture-src")
    if not os.path.isdir(module):
        os.makedirs(module)
        with open(os.path.join(module, "go.mod"), "w") as f:
            f.write("module example.com/fixture\n\ngo 1.27\n")
        with open(os.path.join(module, "main.go"), "w") as f:
            f.write("package main\n\nfunc main() {}\n")
    out = os.path.join(directory, name)
    args = [GO, "build", "-trimpath", "-o", out]
    if pie:
        args.append("-buildmode=pie")
    env = dict(os.environ, CGO_ENABLED="0", GOOS="linux", GOARCH=goarch, GOFLAGS="")
    subprocess.run(args + ["."], cwd=module, env=env, check=True, capture_output=True)
    return out


def elf64(order="<", phdrs=(), machine=0x3E, xnum=None, interp=b"/lib/ld-musl-x86_64.so.1\0"):
    """elf64 writes a minimal ELF64 file: a header, the given program header
    types, and, when one of them is PT_INTERP, the path it points at. xnum
    sets the extended program header count through section header 0."""
    phoff = 64
    count = len(phdrs)
    shoff = phoff + 56 * count
    data_off = shoff + (64 if xnum is not None else 0)
    ident = b"\x7fELF" + bytes([2, 1 if order == "<" else 2, 1]) + bytes(9)
    header = ident + struct.pack(
        order + "HHIQQQIHHHHHH", 2, machine, 1, 0, phoff, shoff if xnum is not None else 0, 0,
        64, 56, check.PN_XNUM if xnum is not None else count, 64 if xnum is not None else 0,
        1 if xnum is not None else 0, 0)
    body = b""
    for p_type in phdrs:
        body += struct.pack(order + "IIQQQQQQ", p_type, 4, data_off, 0, 0, len(interp), len(interp), 1)
    if xnum is not None:
        body += struct.pack(order + "IIQQQQIIQQ", 0, 0, 0, 0, 0, 0, 0, xnum, 0, 0)
    return header + body + interp


def run_tree(*roots):
    """run_tree runs the check over roots and returns its status and output."""
    out, err = io.StringIO(), io.StringIO()
    status = check.check_tree(list(roots), out, err)
    return status, out.getvalue(), err.getvalue()


@unittest.skipIf(GO is None, "needs the go toolchain to build the fixtures")
class RealBuilds(unittest.TestCase):
    """The toolchain's own output, built the release's way and the PIE way."""

    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.mkdtemp()
        cls.static_amd64 = build_fixture(cls.tmp, "static-amd64")
        cls.static_arm64 = build_fixture(cls.tmp, "static-arm64", goarch="arm64")
        cls.pie_amd64 = build_fixture(cls.tmp, "pie-amd64", pie=True)
        cls.pie_arm64 = build_fixture(cls.tmp, "pie-arm64", goarch="arm64", pie=True)
        cls.static_386 = build_fixture(cls.tmp, "static-386", goarch="386")

    @classmethod
    def tearDownClass(cls):
        shutil.rmtree(cls.tmp)

    def tree(self, *binaries):
        """tree copies binaries into a directory of their own and returns it."""
        directory = tempfile.mkdtemp(dir=self.tmp)
        for binary in binaries:
            shutil.copy(binary, directory)
        return directory

    def test_the_release_build_is_accepted(self):
        directory = self.tree(self.static_amd64, self.static_arm64)
        with open(os.path.join(directory, "checksums.txt"), "w") as f:
            f.write("not an ELF file, and not checked\n")
        status, out, err = run_tree(directory)
        self.assertEqual(status, 0, err)
        self.assertIn("ELF64 x86-64, no PT_INTERP", out)
        self.assertIn("ELF64 aarch64, no PT_INTERP", out)
        self.assertIn("all 2 ELF files are standalone", out)

    def test_a_pie_build_is_refused_for_its_interpreter(self):
        for name, pie in (("amd64", self.pie_amd64), ("arm64", self.pie_arm64)):
            with self.subTest(goarch=name):
                status, _, err = run_tree(self.tree(self.static_amd64, pie))
                self.assertEqual(status, 1)
                self.assertIn("pie-" + name + ": has a PT_INTERP program header requesting '/", err)
                self.assertIn("1 of 2 ELF files are not standalone", err)

    def test_a_32_bit_build_is_refused(self):
        status, _, err = run_tree(self.tree(self.static_386))
        self.assertEqual(status, 1)
        self.assertIn("ELF class 1, not ELF64", err)

    def test_nested_directories_are_walked(self):
        directory = self.tree()
        nested = os.path.join(directory, "libgen-mcp_linux_amd64_v1")
        os.makedirs(nested)
        shutil.copy(self.pie_amd64, nested)
        status, _, err = run_tree(directory)
        self.assertEqual(status, 1)
        self.assertIn(os.path.join("libgen-mcp_linux_amd64_v1", "pie-amd64"), err)


class WrittenFiles(unittest.TestCase):
    """Shapes the toolchain does not produce on demand."""

    def setUp(self):
        self.directory = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.directory)

    def write(self, name, data):
        with open(os.path.join(self.directory, name), "wb") as f:
            f.write(data)

    def test_each_byte_order_is_read(self):
        for order in ("<", ">"):
            with self.subTest(order=order):
                self.assertEqual(check.check_elf(io.BytesIO(elf64(order, phdrs=(1, 6)))), "x86-64")
                with self.assertRaisesRegex(check.NotStandalone, "requesting '/lib/ld-musl-x86_64.so.1'"):
                    check.check_elf(io.BytesIO(elf64(order, phdrs=(6, check.PT_INTERP, 1))))

    def test_the_extended_program_header_count_is_followed(self):
        with self.assertRaisesRegex(check.NotStandalone, "PT_INTERP"):
            check.check_elf(io.BytesIO(elf64(phdrs=(1, check.PT_INTERP), xnum=2)))
        self.assertEqual(check.check_elf(io.BytesIO(elf64(phdrs=(1, 1), xnum=2))), "x86-64")

    def test_an_unknown_machine_is_named_by_number(self):
        self.assertEqual(check.check_elf(io.BytesIO(elf64(machine=0xF3))), "machine 0xf3")

    def test_a_file_that_cannot_be_read_as_elf64_is_refused(self):
        good = elf64(phdrs=(1,))
        for name, data, want in (
            ("identification", good[:10], "truncated ELF identification"),
            ("encoding", good[:5] + b"\x07" + good[6:], "unknown ELF data encoding 7"),
            ("header", good[:40], "truncated ELF header"),
            ("program header", good[:64 + 20], "truncated program header 0"),
            ("entry size", good[:54] + struct.pack("<H", 32) + good[56:], "smaller than Elf64_Phdr"),
            ("extended count", elf64(phdrs=(1,), xnum=1)[:64 + 56 + 10], "extended program header count unreadable"),
        ):
            with self.subTest(name=name):
                with self.assertRaisesRegex(check.NotStandalone, want):
                    check.check_elf(io.BytesIO(data))

    def test_a_directory_with_no_elf_file_fails(self):
        self.write("checksums.txt", b"nothing here is a binary\n")
        status, _, err = run_tree(self.directory)
        self.assertEqual(status, 1)
        self.assertIn("so nothing was checked", err)

    def test_a_symbolic_link_is_not_followed(self):
        outside = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, outside)
        with open(os.path.join(outside, "pie"), "wb") as f:
            f.write(elf64(phdrs=(check.PT_INTERP,)))
        os.symlink(os.path.join(outside, "pie"), os.path.join(self.directory, "link"))
        self.write("static", elf64(phdrs=(1,)))
        status, out, err = run_tree(self.directory)
        self.assertEqual(status, 0, err)
        self.assertNotIn("link", out)

    def test_a_missing_directory_is_a_usage_error(self):
        status, _, err = run_tree(os.path.join(self.directory, "absent"))
        self.assertEqual(status, 2)
        self.assertIn("is not a directory", err)

    def test_the_command_line(self):
        self.write("static", elf64(phdrs=(1,)))
        for argv, want in ((["-h"], 0), (["--nope"], 2), ([self.directory], 0)):
            with self.subTest(argv=argv):
                self.assertEqual(check.main(argv, io.StringIO(), io.StringIO()), want)


if __name__ == "__main__":
    unittest.main()
