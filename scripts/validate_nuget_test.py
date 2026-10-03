#!/usr/bin/env python3
"""Tests the archive-layout check in scripts/validate_nuget.py.

The release attests the seven .nupkg files before it pushes them, and nuget.org
then adds its repository signature, a .signature.p7s entry, to every package it
serves. A verifier finds the attestation by removing that entry the way NuGet
defines an unsigned package and hashing what is left, which gives back the
attested bytes only for an archive the signing leaves intact. So two things are
pinned here:

  * a package packed by scripts/build_nuget.py has that layout, and signing it
    and unsigning it again, with Info-ZIP's `zip -d` as the docs tell a user to
    and with the release's own verifier, gives back every byte;
  * check_signable_layout refuses each layout for which that would not hold,
    naming the entry at fault.

Run with `make check-verify-published`, or:

    python3 -m unittest discover -s scripts -p 'validate_nuget_test.py'
"""

import hashlib
import importlib.util
import io
import os
import shutil
import struct
import subprocess
import tempfile
import unittest
import zipfile

ROOT = os.path.abspath(os.path.join(os.path.dirname(os.path.abspath(__file__)), ".."))


def load(name):
    """load imports a script by path; scripts/ is not a package."""
    spec = importlib.util.spec_from_file_location(name, os.path.join(ROOT, "scripts", name + ".py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


build_nuget = load("build_nuget")
validate_nuget = load("validate_nuget")
vpp = load("verify_published_packages")

# The licence texts every package carries: the repository's LICENSE, and
# third-party notices opening with the generator's header.
NOTICES = b"Third-party notices for libgen-mcp\n\nstand-in notices\n"
LICENSES = build_nuget.read_licenses(build_nuget.LICENSE_FILES) + [(build_nuget.NOTICES, NOTICES)]


class Unseekable(io.RawIOBase):
    """A write-only stream that can say where it is but cannot seek, which is
    what makes zipfile write every entry with a trailing data descriptor."""

    def __init__(self):
        super().__init__()
        self.buffer = io.BytesIO()

    def writable(self):
        return True

    def write(self, data):
        return self.buffer.write(data)

    def tell(self):
        return self.buffer.tell()

    def seek(self, *args):
        raise OSError("not seekable")


def rewrite(blob, stream_data_descriptors=False, zip64_entries=(), comment=b""):
    """rewrite repacks an archive entry by entry, keeping names, modes and
    contents, in one of the layouts nuget.org's signing would not leave intact."""
    sink = Unseekable() if stream_data_descriptors else io.BytesIO()
    with zipfile.ZipFile(io.BytesIO(blob)) as src, zipfile.ZipFile(sink, "w") as dst:
        for info in src.infolist():
            # Read before writing: dst.open rewrites the ZipInfo it is given,
            # and src looks the entry up through that same object.
            data = src.read(info.filename)
            with dst.open(info, "w", force_zip64=info.filename in zip64_entries) as fh:
                fh.write(data)
        dst.comment = comment
    return sink.buffer.getvalue() if stream_data_descriptors else sink.getvalue()


def sign_like_nuget(blob):
    """sign_like_nuget appends a stored .signature.p7s the way nuget.org signs
    a package: a local entry after the last one and a central record after the
    last one, every byte before them unchanged (zipfile's append mode does
    exactly that)."""
    out = io.BytesIO(blob)
    with zipfile.ZipFile(out, "a") as zf:
        info = zipfile.ZipInfo(".signature.p7s", date_time=(2026, 1, 1, 0, 0, 0))
        info.compress_type = zipfile.ZIP_STORED
        zf.writestr(info, b"0\x82 nuget.org repository signature")
    return out.getvalue()


def problems_of(data, name="pkg.nupkg"):
    """problems_of runs the layout check over bytes and returns what it reports."""
    problems = []
    validate_nuget.check_signable_layout(bytes(data), name, problems)
    return problems


class PackedFixture(unittest.TestCase):
    """The pointer and one runtime package, packed by build_nuget.py itself."""

    VERSION = "1.0.0"
    RID = "linux-x64"

    def setUp(self):
        self.tmp = tempfile.mkdtemp(prefix="validate-nuget-test-")
        self.addCleanup(shutil.rmtree, self.tmp, ignore_errors=True)
        binary = os.path.join(self.tmp, "libgen-mcp-linux-amd64")
        with open(binary, "wb") as fh:
            fh.write(b"\x7fELF" + os.urandom(4096) + b"\0" * 65536)
        out = os.path.join(self.tmp, "out")
        os.makedirs(out)
        server_doc = build_nuget.mcp_server_json(
            {"name": build_nuget.SERVER_NAME, "packages": [
                {"registryType": "nuget", "identifier": build_nuget.PKG_ID, "version": "0"}]},
            self.VERSION,
        )
        names = [
            build_nuget.build_pointer(out, self.VERSION, "README " + build_nuget.MCP_NAME_TOKEN, b"\x89PNG",
                                      server_doc, LICENSES),
            build_nuget.build_rid_package(out, self.VERSION, "linux-amd64", self.RID, binary, LICENSES),
        ]
        self.packages = {}
        for name in names:
            with open(os.path.join(out, name), "rb") as fh:
                self.packages[name] = fh.read()


class PackedLayoutTest(PackedFixture):
    """What build_nuget.py writes is what nuget.org's signing leaves intact."""

    def test_packed_packages_pass(self):
        for name, blob in self.packages.items():
            with self.subTest(name):
                self.assertEqual(problems_of(blob, name), [])

    def test_the_verifier_gives_back_the_packed_bytes_after_signing(self):
        for name, blob in self.packages.items():
            with self.subTest(name):
                self.assertEqual(vpp.nuget_unsigned(sign_like_nuget(blob)), blob)

    def test_zip_delete_gives_back_the_packed_bytes_after_signing(self):
        """The command the installation guide gives a user: Info-ZIP's zip -d."""
        if shutil.which("zip") is None:
            if os.environ.get("CI"):
                self.fail("zip is not on PATH, and CI is expected to have it")
            self.skipTest("zip is not on PATH")
        for name, blob in self.packages.items():
            with self.subTest(name):
                path = os.path.join(self.tmp, "signed-" + name)
                with open(path, "wb") as fh:
                    fh.write(sign_like_nuget(blob))
                subprocess.run(["zip", "-q", "-d", path, ".signature.p7s"], check=True)
                with open(path, "rb") as fh:
                    got = fh.read()
                self.assertEqual(hashlib.sha256(got).hexdigest(), hashlib.sha256(blob).hexdigest())

    def test_each_layout_the_signing_would_rewrite_is_refused(self):
        pointer = "{}.{}.nupkg".format(build_nuget.PKG_ID, self.VERSION)
        runtime = "{}.{}.{}.nupkg".format(build_nuget.PKG_ID, self.RID, self.VERSION)
        binary_entry = "tools/any/{}/libgen-mcp".format(self.RID)
        cases = [
            ("the pointer is written with data descriptors",
             lambda: rewrite(self.packages[pointer], stream_data_descriptors=True), "data descriptor"),
            ("a binary is stored in zip64 form",
             lambda: rewrite(self.packages[runtime], zip64_entries=(binary_entry,)), "zip64 form"),
            ("a package carries an archive comment",
             lambda: rewrite(self.packages[runtime], comment=b"built by hand"), "archive comment"),
            ("a package is already signed", lambda: sign_like_nuget(self.packages[pointer]),
             "already carries .signature.p7s"),
        ]
        for name, tamper, want in cases:
            with self.subTest(name):
                problems = problems_of(tamper())
                self.assertTrue(problems, "nothing was reported")
                self.assertTrue(any(want in p for p in problems), problems)


def without_entry(blob, entry, replacement=None):
    """without_entry repacks a package without one entry, or with other bytes
    in its place."""
    out = io.BytesIO()
    with zipfile.ZipFile(io.BytesIO(blob)) as src, zipfile.ZipFile(out, "w") as dst:
        for info in src.infolist():
            if info.filename == entry:
                if replacement is None:
                    continue
                dst.writestr(info, replacement)
            else:
                dst.writestr(info, src.read(info.filename))
    return out.getvalue()


class LicenceFilesTest(PackedFixture):
    """Every package carries LICENSE and THIRD_PARTY_NOTICES at its root:
    the repository's licence byte for byte, and the notices the release
    verified."""

    def licence_problems(self, blob, digest=None):
        problems = []
        with zipfile.ZipFile(io.BytesIO(blob)) as zf:
            validate_nuget.check_licenses(zf, "pkg.nupkg", problems, digest)
        return problems

    def test_packed_packages_carry_both_texts(self):
        digest = hashlib.sha256(NOTICES).hexdigest()
        for name, blob in self.packages.items():
            with self.subTest(name):
                self.assertEqual(self.licence_problems(blob, digest), [])

    def test_each_missing_or_foreign_text_is_refused(self):
        blob = self.packages["{}.{}.nupkg".format(build_nuget.PKG_ID, self.VERSION)]
        cases = [
            ("no LICENSE", without_entry(blob, "LICENSE"), None, "no LICENSE at the package root"),
            ("another LICENSE", without_entry(blob, "LICENSE", b"other\n"), None,
             "LICENSE is not the repository's LICENSE"),
            ("no notices", without_entry(blob, "THIRD_PARTY_NOTICES"), None,
             "no THIRD_PARTY_NOTICES at the package root"),
            ("notices the generator did not write", without_entry(blob, "THIRD_PARTY_NOTICES", b"other\n"),
             None, "does not open with the generator's header"),
            ("notices other than the verified ones", blob, "0" * 64, "but the release's signed checksums.txt named"),
        ]
        for name, tampered, digest, want in cases:
            with self.subTest(name):
                problems = self.licence_problems(tampered, digest)
                self.assertTrue(any(want in p for p in problems), problems)


class ValidatePackagesLayoutTest(unittest.TestCase):
    """validate_packages runs the layout check over every package it is given,
    so a package the signing would rewrite fails the release before the push."""

    VERSION = "1.0.0"

    def setUp(self):
        self.tmp = tempfile.mkdtemp(prefix="validate-nuget-packages-")
        self.addCleanup(shutil.rmtree, self.tmp, ignore_errors=True)
        self.out = os.path.join(self.tmp, "dist")
        os.makedirs(self.out)
        digests = {}
        for plat_key, rid in build_nuget.RIDS.items():
            binary = os.path.join(self.tmp, "libgen-mcp-" + plat_key)
            with open(binary, "wb") as fh:
                fh.write(plat_key.encode() * 64)
            with open(binary, "rb") as fh:
                digests[plat_key] = hashlib.sha256(fh.read()).hexdigest()
            build_nuget.build_rid_package(self.out, self.VERSION, plat_key, rid, binary, LICENSES)
        server_doc = build_nuget.mcp_server_json(
            {"name": build_nuget.SERVER_NAME, "packages": [
                {"registryType": "nuget", "identifier": build_nuget.PKG_ID, "version": "0"}]},
            self.VERSION,
        )
        self.pointer = build_nuget.build_pointer(
            self.out, self.VERSION, "README " + build_nuget.MCP_NAME_TOKEN, None, server_doc, LICENSES)
        with open(os.path.join(self.out, validate_nuget.VERIFIED_MANIFEST), "w", encoding="utf-8") as fh:
            fh.write('{{"version": "{}", "verified": true, "binaries": {}}}'.format(
                self.VERSION, str(digests).replace("'", '"')))

    def layout_problems(self):
        """layout_problems runs validate_packages and keeps the layout findings."""
        problems = validate_nuget.validate_packages(self.out, self.VERSION)
        return [p for p in problems if "archive comment" in p or "data descriptor" in p]

    def test_packed_packages_report_no_layout_problem(self):
        self.assertEqual(self.layout_problems(), [])

    def test_a_package_with_an_archive_comment_is_reported(self):
        path = os.path.join(self.out, self.pointer)
        with open(path, "rb") as fh:
            blob = rewrite(fh.read(), comment=b"built by hand")
        with open(path, "wb") as fh:
            fh.write(blob)
        problems = self.layout_problems()
        self.assertEqual(len(problems), 1, problems)
        self.assertIn(self.pointer + ": 13 byte(s) of archive comment", problems[0])


class SignableLayoutTest(unittest.TestCase):
    """check_signable_layout over hand-damaged bytes, one record at a time.

    zipfile can only write well-formed archives; these reach the branches that
    judge a record zipfile would never produce, each named by the problem it
    must report.
    """

    def archive(self, names=("[Content_Types].xml", "tools/any/linux-x64/libgen-mcp")):
        """archive packs a few entries the way build_nuget.py does."""
        blob = io.BytesIO()
        with zipfile.ZipFile(blob, "w") as zf:
            for name in names:
                info = zipfile.ZipInfo(name, date_time=build_nuget.ZIP_DATE)
                info.compress_type = zipfile.ZIP_DEFLATED
                zf.writestr(info, name.encode() * 8)
        return bytearray(blob.getvalue())

    def offsets(self, data):
        """offsets returns the end record's offset and the central directory's."""
        eocd = bytes(data).rfind(validate_nuget.ZIP_EOCD)
        return eocd, struct.unpack_from("<I", data, eocd + 16)[0]

    def assert_one(self, data, want):
        """assert_one expects exactly one problem, carrying want."""
        problems = problems_of(data)
        self.assertEqual(len(problems), 1, problems)
        self.assertIn(want, problems[0])

    def test_an_archive_packed_like_build_nuget_passes(self):
        self.assertEqual(problems_of(self.archive()), [])

    def test_damaged_records_are_named(self):
        def no_end_record(data):
            data[-22:-18] = b"XXXX"

        def truncated_end_record(data):
            del data[-4:]

        def comment(data):
            data[-2:] = struct.pack("<H", 5)
            data.extend(b"hello")

        def zip64_locator(data):
            eocd, _ = self.offsets(data)
            data[eocd:eocd] = validate_nuget.ZIP64_EOCD_LOCATOR + b"\0" * 16

        def zip64_entry_count(data):
            eocd, _ = self.offsets(data)
            struct.pack_into("<H", data, eocd + 10, validate_nuget.ZIP64_SENTINEL_16)

        def zip64_directory_offset(data):
            eocd, _ = self.offsets(data)
            struct.pack_into("<I", data, eocd + 16, validate_nuget.ZIP64_SENTINEL_32)

        def directory_past_the_end(data):
            eocd, _ = self.offsets(data)
            struct.pack_into("<I", data, eocd + 16, eocd)

        def damaged_central_record(data):
            _, cd = self.offsets(data)
            data[cd:cd + 4] = b"XXXX"

        def local_offset_elsewhere(data):
            _, cd = self.offsets(data)
            struct.pack_into("<I", data, cd + 42, 1)

        def descriptor_in_the_local_header_only(data):
            struct.pack_into("<H", data, 6, validate_nuget.ZIP_DATA_DESCRIPTOR_FLAG)

        def descriptor_in_the_central_record_only(data):
            _, cd = self.offsets(data)
            struct.pack_into("<H", data, cd + 8, validate_nuget.ZIP_DATA_DESCRIPTOR_FLAG)

        def zip64_compressed_size(data):
            _, cd = self.offsets(data)
            struct.pack_into("<I", data, cd + 20, validate_nuget.ZIP64_SENTINEL_32)

        def zip64_uncompressed_size(data):
            _, cd = self.offsets(data)
            struct.pack_into("<I", data, cd + 24, validate_nuget.ZIP64_SENTINEL_32)

        cases = [
            ("no end record", no_end_record, "no end-of-central-directory record"),
            ("an end record cut short", truncated_end_record, "no end-of-central-directory record"),
            ("an archive comment", comment, "5 byte(s) of archive comment"),
            ("a zip64 end locator", zip64_locator, "ends in zip64 form"),
            ("a zip64 entry count", zip64_entry_count, "ends in zip64 form"),
            ("a zip64 directory offset", zip64_directory_offset, "ends in zip64 form"),
            ("a directory past the end record", directory_past_the_end, "runs past the end"),
            ("a damaged central record", damaged_central_record, "no central directory record at offset"),
            ("a local offset that names no local header", local_offset_elsewhere, "that is not one"),
            ("a data descriptor in the local header only", descriptor_in_the_local_header_only,
             "[Content_Types].xml is written with a data descriptor"),
            ("a data descriptor in the central record only", descriptor_in_the_central_record_only,
             "[Content_Types].xml is written with a data descriptor"),
            ("a zip64 compressed size", zip64_compressed_size, "[Content_Types].xml is stored in zip64 form"),
            ("a zip64 uncompressed size", zip64_uncompressed_size, "[Content_Types].xml is stored in zip64 form"),
        ]
        for name, damage, want in cases:
            with self.subTest(name):
                data = self.archive()
                damage(data)
                self.assert_one(data, want)

    def test_a_zip64_record_is_named_as_zip64_before_its_local_offset_is_read(self):
        """A zip64 record keeps its real local offset in its extra field, so a
        sentinel offset, or a zip64 extra field, is reported as zip64 and never
        sent to look for a header where none was promised."""
        sentinel = self.archive()
        _, cd = self.offsets(sentinel)
        struct.pack_into("<I", sentinel, cd + 42, validate_nuget.ZIP64_SENTINEL_32)

        # The first record's extra field becomes a zip64 one in place: four
        # bytes are carved out of the record's name length and turned into an
        # extra-field header with an empty body, so the offsets and every later
        # record stay where they were.
        extra = self.archive()
        _, cd = self.offsets(extra)
        name_len = struct.unpack_from("<H", extra, cd + 28)[0]
        struct.pack_into("<HH", extra, cd + 28, name_len - 4, 4)
        struct.pack_into("<HH", extra, cd + 46 + name_len - 4, validate_nuget.ZIP64_EXTRA_ID, 0)

        for name, data in (("a sentinel local offset", sentinel), ("a zip64 extra field", extra)):
            with self.subTest(name):
                problems = problems_of(data)
                self.assertEqual(len(problems), 1, problems)
                self.assertIn("is stored in zip64 form", problems[0])

    def test_a_zip64_extra_in_the_local_header_is_named(self):
        blob = io.BytesIO()
        with zipfile.ZipFile(blob, "w") as zf:
            with zf.open("tools/any/linux-x64/libgen-mcp", "w", force_zip64=True) as fh:
                fh.write(b"binary")
        self.assert_one(bytearray(blob.getvalue()), "libgen-mcp is stored in zip64 form")

    def test_a_signature_entry_is_named_in_any_case(self):
        for name in (".signature.p7s", ".SIGNATURE.P7S"):
            with self.subTest(name):
                self.assert_one(self.archive(("README.md", name)), "already carries " + name)

    def test_extra_field_ids_reads_every_header_and_stops_at_a_short_tail(self):
        extra = struct.pack("<HH", 0x5455, 2) + b"ab" + struct.pack("<HH", 0x0001, 0) + b"\x07"
        self.assertEqual(validate_nuget.extra_field_ids(extra), [0x5455, 0x0001])
        self.assertEqual(validate_nuget.extra_field_ids(b""), [])


if __name__ == "__main__":
    unittest.main()
