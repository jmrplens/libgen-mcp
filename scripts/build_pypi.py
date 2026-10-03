#!/usr/bin/env python3
"""Assemble the PyPI distribution from released binaries.

Builds one platform wheel per release binary, the uv/ruff model adapted to
Python packaging: each wheel carries the native libgen-mcp binary, and pip
selects the wheel whose platform tag matches the host. Nothing is compiled
here and nothing is downloaded at install time.

Standard library only, so the release job and a contributor's machine need a
Python interpreter and nothing else.

Usage:
    python3 scripts/build_pypi.py --binaries dist --version 1.7.3 [--out pypi/dist]

The binaries directory must hold the GoReleaser release assets under their
exact release names (libgen-mcp-<os>-<arch>[.exe]) together with the release's
own checksums.txt, which is cosign-signed at build time. Every binary is
checked against it before it goes into a wheel, because a published PyPI file
name is burned forever: deleting a release does not free it. Pass
--allow-unverified only when there is genuinely no manifest (never in CI).

Wheels land in --out (default pypi/dist), which is wiped first so a rebuild
cannot mix versions.

The licence is declared the way PEP 639 and core metadata 2.4 define it: an
SPDX License-Expression, and a License-File for each licence text the wheel
carries under its .dist-info/licenses/ directory. The legacy License field and
the License :: classifier are left out on purpose, since the specification
makes License and License-Expression mutually exclusive and PyPI refuses an
upload carrying both.

The texts are the repository's LICENSE and THIRD_PARTY_NOTICES, the license,
notice and patent texts of every module the binaries link, which the release
generates beside the binaries (cmd/gen_third_party_notices) and lists in
checksums.txt. The notices are read from the binaries directory and verified
like a binary, and a release without them builds no wheel.
"""

import argparse
import base64
import hashlib
import json
import os
import re
import shutil
import struct
import sys
import zipfile

# libgen-mcp is free on PyPI, so the distribution, the import package and the
# command are the same name. That is worth noticing rather than copying from
# elsewhere: when a project has to ship under an author-prefixed distribution
# name it needs a console-script wrapper so `uvx <dist-name>` finds something
# to run. Here it does not — see the entry-points note in build_wheel.
DIST_NAME = "libgen-mcp"
NORM_NAME = DIST_NAME.replace("-", "_")
PKG_NAME = "libgen_mcp"
COMMAND = "libgen-mcp"

# Release-asset name fragments mapped to wheel platform tags.
#
# The linux wheels carry musllinux tags **as well as** manylinux ones, in one
# compressed tag set, and that is a property of the binary rather than a guess:
# the release binaries are built without -buildmode=pie, so they name no ELF
# interpreter and demand no libc symbol at all (validate_pypi.py checks both on
# the archived bytes). The same file therefore installs on Debian and on Alpine.
# A PIE build could not make this claim, which is why the sibling project's
# wheels are manylinux-only.
#
# The macOS tags name the oldest macOS the binary starts on, and that is the Go
# toolchain's decision, not ours: the linker writes it into the binary's
# LC_BUILD_VERSION load command as `minos`, and dyld refuses to start a binary
# on a system older than that. Go 1.27 writes 13.0 for both darwin/amd64 and
# darwin/arm64. A tag below it is worse than a missing wheel, because pip on the
# older system installs the wheel and the command then cannot start, so
# main() reads minos from each darwin binary and refuses one that disagrees
# with MACOS_MINIMUM. A toolchain bump that raises the floor therefore stops
# the build here instead of publishing a wheel tagged for systems it does not
# run on. Wheel tags carry major and minor only, which is all pip compares.
MACOS_MINIMUM = (13, 0)
MACOS_TAG = "macosx_{}_{}".format(*MACOS_MINIMUM)

PLATFORMS = {
    "linux-amd64": "manylinux_2_17_x86_64.manylinux2014_x86_64.musllinux_1_1_x86_64",
    "linux-arm64": "manylinux_2_17_aarch64.manylinux2014_aarch64.musllinux_1_1_aarch64",
    "darwin-amd64": MACOS_TAG + "_x86_64",
    "darwin-arm64": MACOS_TAG + "_arm64",
    "windows-amd64": "win_amd64",
    "windows-arm64": "win_arm64",
}

# Deterministic zip entry timestamp (zip's epoch): rebuilding the same inputs
# yields byte-identical wheels, so a hash that changes means the input changed.
ZIP_DATE = (1980, 1, 1, 0, 0, 0)

ROOT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..")

# The licence texts every wheel carries from the repository, by the name they
# take under .dist-info/licenses/ (the License-File value) and where they are
# read from. The third-party notices join them from the release assets.
LICENSE_FILES = [("LICENSE", os.path.join(ROOT, "LICENSE"))]

# The release asset holding the third-party notices, and its License-File value.
NOTICES = "THIRD_PARTY_NOTICES"

LAUNCHER = '''\
"""Locator for the libgen-mcp binary installed by this wheel.

The binary itself ships in the wheel's .data/scripts directory, so the
installer places it on the scripts path (bin/ or Scripts/) with the
executable bit set and it IS the `libgen-mcp` command; no Python runs in
that path. This module exists for `python -m libgen_mcp` and for
programmatic lookup, the same shape uv's find_uv_bin takes.
"""

import os
import subprocess
import sys
import sysconfig

__version__ = "{version}"

_BIN = "libgen-mcp.exe" if os.name == "nt" else "libgen-mcp"


def find_binary():
    """Return the installed binary's path, searching the scripts
    directories of the running environment."""
    candidates = [
        sysconfig.get_path("scripts"),
        os.path.join(sys.prefix, "Scripts" if os.name == "nt" else "bin"),
        os.path.dirname(sys.executable),
    ]
    for scripts_dir in candidates:
        if not scripts_dir:
            continue
        path = os.path.join(scripts_dir, _BIN)
        if os.path.isfile(path):
            return path
    raise FileNotFoundError(
        "libgen-mcp binary not found next to " + sys.executable)


def main():
    path = find_binary()
    argv = [path] + sys.argv[1:]
    if os.name == "nt":
        try:
            raise SystemExit(subprocess.call(argv))
        except KeyboardInterrupt:
            raise SystemExit(130) from None
    os.execv(path, argv)
'''

MAIN_MODULE = '''\
from libgen_mcp import main

if __name__ == "__main__":
    main()
'''

SUMMARY = (
    "MCP server for federated search, citation and reading of books and papers "
    "across Library Genesis and open-access sources (native Go binary)"
)


def build_metadata(version, readme, license_files):
    headers = [
        ("Metadata-Version", "2.4"),
        ("Name", DIST_NAME),
        ("Version", version),
        ("Summary", SUMMARY),
        ("Author", "jmrplens"),
        ("License-Expression", "MIT"),
    ]
    headers += [("License-File", name) for name in license_files]
    headers += [
        ("Project-URL", "Homepage, https://github.com/jmrplens/libgen-mcp"),
        ("Project-URL", "Documentation, https://jmrp.io/docs/libgen-mcp/"),
        ("Project-URL", "Repository, https://github.com/jmrplens/libgen-mcp"),
        ("Project-URL", "Changelog, https://github.com/jmrplens/libgen-mcp/releases"),
        # "Issues" and "Security" are labels the well-known project URLs
        # specification names, so PyPI renders them as the issue tracker and
        # the security policy rather than as two more links.
        ("Project-URL", "Issues, https://github.com/jmrplens/libgen-mcp/issues"),
        ("Project-URL", "Security, https://github.com/jmrplens/libgen-mcp/security/policy"),
        ("Classifier", "Development Status :: 5 - Production/Stable"),
        ("Classifier", "Intended Audience :: Developers"),
        ("Classifier", "Intended Audience :: Science/Research"),
        ("Classifier", "Topic :: Software Development"),
        ("Classifier", "Programming Language :: Go"),
        ("Requires-Python", ">=3.9"),
        ("Description-Content-Type", "text/markdown"),
    ]
    lines = ["{}: {}".format(k, v) for k, v in headers]
    return "\n".join(lines) + "\n\n" + readme


def wheel_file(tag):
    return (
        "Wheel-Version: 1.0\n"
        "Generator: libgen-mcp build_pypi.py\n"
        "Root-Is-Purelib: false\n"
        "Tag: py3-none-{}\n".format(tag)
    )


def read_checksums(binaries_dir):
    """Parse `<sha256>  <name>` lines from the release's checksums.txt.

    Returns None when the file is absent, so the caller decides whether an
    unverified build is acceptable.
    """
    path = os.path.join(binaries_dir, "checksums.txt")
    if not os.path.isfile(path):
        return None
    entries = {}
    with open(path, encoding="utf-8") as fh:
        for line in fh:
            parts = line.replace("\r", "").strip().split(None, 1)
            if len(parts) == 2 and re.fullmatch(r"[0-9a-f]{64}", parts[0]):
                entries[parts[1].lstrip("*")] = parts[0]
    return entries


def verify_asset(path, checksums):
    """Abort unless the release asset at path matches the digest the signed
    manifest names, and return that digest."""
    name = os.path.basename(path)
    want = checksums.get(name)
    if want is None:
        sys.exit("build_pypi: {} is not listed in checksums.txt".format(name))
    with open(path, "rb") as fh:
        got = hashlib.sha256(fh.read()).hexdigest()
    if got != want:
        sys.exit("build_pypi: {} is sha256 {}, but checksums.txt says {}".format(name, got, want))
    return got


# Mach-O load commands that carry the minimum macOS version.
MACHO_MAGIC_64 = 0xFEEDFACF
LC_VERSION_MIN_MACOSX = 0x24
LC_BUILD_VERSION = 0x32
PLATFORM_MACOS = 1


def macho_minimum_macos(data):
    """Return the minimum macOS a thin 64-bit Mach-O binary declares, as
    (major, minor, patch), or None when the bytes declare none.

    LC_BUILD_VERSION is what current linkers write; LC_VERSION_MIN_MACOSX is
    its predecessor, read too so an older binary is not reported as having no
    floor. The version is packed as xxxx.yy.zz in one 32-bit word.
    """
    if len(data) < 32 or struct.unpack_from("<I", data, 0)[0] != MACHO_MAGIC_64:
        return None
    ncmds = struct.unpack_from("<I", data, 16)[0]
    off = 32
    for _ in range(ncmds):
        if off + 16 > len(data):
            return None
        cmd, size = struct.unpack_from("<II", data, off)
        if cmd == LC_BUILD_VERSION:
            platform, version = struct.unpack_from("<II", data, off + 8)
            if platform == PLATFORM_MACOS:
                return (version >> 16, (version >> 8) & 0xFF, version & 0xFF)
        elif cmd == LC_VERSION_MIN_MACOSX:
            version = struct.unpack_from("<I", data, off + 8)[0]
            return (version >> 16, (version >> 8) & 0xFF, version & 0xFF)
        if size < 8:
            return None
        off += size
    return None


def check_macos_minimum(binary_path):
    """Abort unless the darwin binary at binary_path starts on exactly the
    macOS its wheel tag names (MACOS_MINIMUM)."""
    with open(binary_path, "rb") as fh:
        minos = macho_minimum_macos(fh.read())
    if minos is None:
        sys.exit("build_pypi: {} declares no minimum macOS version, so its wheel "
                 "tag cannot be derived".format(binary_path))
    if minos != MACOS_MINIMUM + (0,):
        sys.exit(
            "build_pypi: {} needs macOS {}, but its wheel would be tagged {}: "
            "set MACOS_MINIMUM to what the binary declares".format(
                binary_path, ".".join(str(n) for n in minos), MACOS_TAG)
        )


def record_hash(data):
    digest = hashlib.sha256(data).digest()
    return "sha256=" + base64.urlsafe_b64encode(digest).decode("ascii").rstrip("=")


def add_file(zf, records, arcname, data, executable=False):
    if isinstance(data, str):
        data = data.encode("utf-8")
    info = zipfile.ZipInfo(arcname, date_time=ZIP_DATE)
    # S_IFREG matters: pip's zip_item_is_executable requires S_ISREG(mode)
    # before it honours the 0o111 bits, so permission bits alone install the
    # binary without +x and the command dies with PermissionError.
    mode = 0o100755 if executable else 0o100644
    info.external_attr = mode << 16
    info.compress_type = zipfile.ZIP_DEFLATED
    zf.writestr(info, data)
    records.append("{},{},{}".format(arcname, record_hash(data), len(data)))


def read_licenses(files):
    """Read each (name, path) of `files` into (name, bytes), refusing a missing
    one: a wheel that declares a License-File it does not carry is one PyPI and
    every metadata reader treat as malformed."""
    texts = []
    for name, path in files:
        if not os.path.isfile(path):
            sys.exit("build_pypi: licence file {} not found at {}".format(name, path))
        with open(path, "rb") as fh:
            texts.append((name, fh.read()))
    return texts


def build_wheel(out_dir, version, plat_key, tag, binary_path, readme, licenses):
    dist_info = "{}-{}.dist-info".format(NORM_NAME, version)
    wheel_name = "{}-{}-py3-none-{}.whl".format(NORM_NAME, version, tag)
    wheel_path = os.path.join(out_dir, wheel_name)

    with open(binary_path, "rb") as fh:
        binary = fh.read()
    bin_name = COMMAND + ".exe" if plat_key.startswith("windows") else COMMAND

    # The binary lives in .data/scripts, which the wheel spec obliges the
    # installer to place on the environment's scripts path with the executable
    # bit set. A binary inside the package directory does not get that
    # guarantee: pip installs it without +x and the launcher dies with
    # PermissionError, which is why uv and ruff ship theirs this way.
    data_scripts = "{}-{}.data/scripts".format(NORM_NAME, version)

    records = []
    with zipfile.ZipFile(wheel_path, "w") as zf:
        add_file(zf, records, PKG_NAME + "/__init__.py", LAUNCHER.format(version=version))
        add_file(zf, records, PKG_NAME + "/__main__.py", MAIN_MODULE)
        add_file(zf, records, data_scripts + "/" + bin_name, binary, executable=True)
        add_file(zf, records, dist_info + "/METADATA",
                 build_metadata(version, readme, [name for name, _ in licenses]))
        add_file(zf, records, dist_info + "/WHEEL", wheel_file(tag))
        # The wheel specification puts every License-File under
        # .dist-info/licenses/, keeping the path the metadata names.
        for name, text in licenses:
            add_file(zf, records, dist_info + "/licenses/" + name, text)
        # No entry_points.txt, deliberately. A console script here would be
        # named `libgen-mcp` — the distribution name — and that is the same
        # name as the binary the .data/scripts entry installs into bin/: two
        # files, one path, and whichever the installer writes last wins. A
        # project shipping under an author-prefixed distribution name needs the
        # wrapper so `uvx <dist-name>` resolves; this one does not, because the
        # binary already carries the name uvx and pipx look for.
        record_name = dist_info + "/RECORD"
        records.append("{},,".format(record_name))
        record_data = "\n".join(records) + "\n"
        info = zipfile.ZipInfo(record_name, date_time=ZIP_DATE)
        # A regular file like every other entry: 0o644 alone left the type
        # bits out, which unzip -Z shows as "?rw-r--r--".
        info.external_attr = 0o100644 << 16
        info.compress_type = zipfile.ZIP_DEFLATED
        zf.writestr(info, record_data)
    return wheel_name


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binaries", required=True, help="directory holding the release binaries")
    parser.add_argument("--version", required=True, help="release version, e.g. 1.7.3")
    parser.add_argument("--out", default="pypi/dist", help="wheelhouse output directory")
    parser.add_argument(
        "--allow-unverified",
        action="store_true",
        help="build without a checksums.txt manifest (never in CI)",
    )
    args = parser.parse_args()

    if not re.fullmatch(r"\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?", args.version):
        sys.exit("build_pypi: version {!r} does not look like a release version".format(args.version))

    readme_path = os.path.join(ROOT, "pypi", "README.md")
    with open(readme_path, encoding="utf-8") as fh:
        readme = fh.read()
    if "mcp-name: io.github.jmrplens/libgen-mcp" not in readme:
        sys.exit("build_pypi: pypi/README.md lost the mcp-name ownership token the MCP Registry validates")
    licenses = read_licenses(LICENSE_FILES)

    checksums = read_checksums(args.binaries)
    if checksums is None and not args.allow_unverified:
        sys.exit(
            "build_pypi: {} not found — refusing to package unverified binaries "
            "(pass --allow-unverified to override)".format(os.path.join(args.binaries, "checksums.txt"))
        )
    if checksums is None:
        sys.stderr.write("WARNING: --allow-unverified — wheels are being built without a checksum manifest\n")

    notices_path = os.path.join(args.binaries, NOTICES)
    if not os.path.isfile(notices_path):
        sys.exit(
            "build_pypi: {} not found: the release generates it beside the binaries "
            "(cmd/gen_third_party_notices), and every wheel carries it".format(notices_path)
        )
    notices = read_licenses([(NOTICES, notices_path)])
    if checksums is not None:
        notices_digest = verify_asset(notices_path, checksums)
    else:
        notices_digest = hashlib.sha256(notices[0][1]).hexdigest()
    licenses += notices

    if os.path.isdir(args.out):
        shutil.rmtree(args.out)
    os.makedirs(args.out)

    built = []
    digests = {}
    for plat_key, tag in PLATFORMS.items():
        suffix = ".exe" if plat_key.startswith("windows") else ""
        binary_path = os.path.join(args.binaries, "{}-{}{}".format(COMMAND, plat_key, suffix))
        if not os.path.isfile(binary_path):
            sys.exit("build_pypi: missing release binary {}".format(binary_path))
        if checksums is not None:
            digests[plat_key] = verify_asset(binary_path, checksums)
        if plat_key.startswith("darwin"):
            check_macos_minimum(binary_path)
        built.append(build_wheel(args.out, args.version, plat_key, tag, binary_path, readme, licenses))

    # Record what was verified so validate_pypi.py can confirm the wheels still
    # carry those exact bytes. Written beside the wheels, never inside one.
    with open(os.path.join(args.out, "verified-binaries.json"), "w", encoding="utf-8") as fh:
        json.dump(
            {"version": args.version, "verified": checksums is not None, "binaries": digests,
             "notices": notices_digest},
            fh, indent=2,
        )
        fh.write("\n")

    for name in built:
        print("built", os.path.join(args.out, name))
    print("build_pypi: {} wheels for version {}".format(len(built), args.version))


if __name__ == "__main__":
    main()
