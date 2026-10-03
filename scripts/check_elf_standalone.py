#!/usr/bin/env python3
"""Hold every linux release binary to being standalone: ELF64, no PT_INTERP.

The release binaries are built CGO_ENABLED=0 without -buildmode=pie, so none
names an ELF interpreter and each runs on glibc, on musl, in a distroless image
and on scratch. PIE takes that away: it makes a Go binary dynamically linked,
and the linker writes a PT_INTERP naming the *build* host's loader, which is
how a published linux/arm64 image once could not exec at all and how v1.7.2's
linux/amd64 asset came to ask for /lib64/ld-linux-x86-64.so.2.

Three guards already held the rule, each for one channel (the Dockerfile, the
npm validator and the PyPI validator), and each by grepping the bytes for a
loader name. Nothing held the loose release assets, the Claude Desktop bundle,
the NuGet packages or the Homebrew formula, which all carry the same binaries.
This check runs once, in the GoReleaser job right after GoReleaser, over every
ELF file under dist/, and every channel downstream inherits it.

It parses the ELF rather than searching it for a string. A program header is
what the kernel reads: PT_INTERP is the segment that makes execve hand the
process to a loader, so its absence is the property itself, where a grep for
"ld-linux" is a proxy that a renamed loader, a musl path or a string that
happens to appear in the data would each fool in one direction or the other.

Every regular file whose first four bytes are the ELF magic is checked, and
each must be ELF64 (the release targets are amd64 and arm64; a 32-bit ELF is a
target nobody declared) with no PT_INTERP among its program headers. A
directory holding no ELF file at all fails too, because a check that found
nothing to look at reads exactly like one that passed.

--expect N makes the count part of the check: the release passes 2, for the
linux/amd64 and linux/arm64 targets .goreleaser.yml builds, so a dropped linux
target fails instead of leaving one binary to pass alone.

Standard library only. Usage:
    python3 scripts/check_elf_standalone.py [--expect N] [DIR ...]    (default: dist)

Exit status: 0 when every ELF file is standalone (and there are N of them), 1
when one is not, none was found or the count is wrong, 2 for a command line it
cannot read or a directory that does not exist.
"""

import os
import struct
import sys

ELF_MAGIC = b"\x7fELF"
ELFCLASS64 = 2
ELFDATA2LSB, ELFDATA2MSB = 1, 2
PT_INTERP = 3
# e_phnum holds this when the real count does not fit, and the count is then
# the sh_info of section header 0 (the ELF gABI's extended numbering).
PN_XNUM = 0xFFFF

MACHINES = {0x3E: "x86-64", 0xB7: "aarch64", 0x28: "arm", 0x03: "386"}


class NotStandalone(Exception):
    """A file that is an ELF and is not a standalone 64-bit one."""


def check_elf(f):
    """check_elf reads one ELF file from the start and returns its machine's
    name, or raises NotStandalone saying why it is not standalone."""
    ident = f.read(16)
    if len(ident) < 16:
        raise NotStandalone("truncated ELF identification")
    if ident[4] != ELFCLASS64:
        raise NotStandalone("ELF class {}, not ELF64".format(ident[4]))
    if ident[5] == ELFDATA2LSB:
        order = "<"
    elif ident[5] == ELFDATA2MSB:
        order = ">"
    else:
        raise NotStandalone("unknown ELF data encoding {}".format(ident[5]))

    rest = f.read(64 - 16)
    if len(rest) < 48:
        raise NotStandalone("truncated ELF header")
    # Elf64_Ehdr after e_ident: e_type, e_machine, e_version, e_entry,
    # e_phoff, e_shoff, e_flags, e_ehsize, e_phentsize, e_phnum, e_shentsize,
    # e_shnum, e_shstrndx.
    (_, machine, _, _, phoff, shoff, _, _, phentsize, phnum, shentsize, _, _) = struct.unpack(
        order + "HHIQQQIHHHHHH", rest)
    if phnum == PN_XNUM:
        f.seek(shoff + 44)  # Elf64_Shdr.sh_info of section 0
        raw = f.read(4)
        if len(raw) < 4 or shentsize < 48:
            raise NotStandalone("extended program header count unreadable")
        phnum = struct.unpack(order + "I", raw)[0]
    if phnum and phentsize < 56:
        raise NotStandalone("program header entry size {} is smaller than Elf64_Phdr".format(phentsize))

    for i in range(phnum):
        f.seek(phoff + i * phentsize)
        header = f.read(56)
        if len(header) < 56:
            raise NotStandalone("truncated program header {}".format(i))
        # Elf64_Phdr: p_type, p_flags, p_offset, p_vaddr, p_paddr, p_filesz,
        # p_memsz, p_align.
        p_type, _, p_offset, _, _, p_filesz, _, _ = struct.unpack(order + "IIQQQQQQ", header)
        if p_type == PT_INTERP:
            f.seek(p_offset)
            interp = f.read(min(p_filesz, 256)).split(b"\0", 1)[0].decode("latin-1")
            raise NotStandalone("has a PT_INTERP program header requesting {!r}".format(interp))
    return MACHINES.get(machine, "machine 0x{:x}".format(machine))


def elf_files(root):
    """elf_files yields every regular file under root that opens with the ELF
    magic, in a stable order. Symbolic links are not followed: GoReleaser
    writes none, and following one could leave the directory being checked."""
    for directory, dirs, files in os.walk(root):
        dirs.sort()
        for name in sorted(files):
            path = os.path.join(directory, name)
            if os.path.islink(path) or not os.path.isfile(path):
                continue
            with open(path, "rb") as f:
                if f.read(4) == ELF_MAGIC:
                    yield path


def check_tree(roots, out=sys.stdout, err=sys.stderr, expect=None):
    """check_tree checks every ELF file under each root and returns the exit
    status. With expect, the number of ELF files found must be exactly that,
    so a release that dropped a linux target fails rather than passing on the
    binaries it still has."""
    for root in roots:
        if not os.path.isdir(root):
            print("check_elf_standalone: {} is not a directory".format(root), file=err)
            return 2
    checked, failed = 0, 0
    for root in roots:
        for path in elf_files(root):
            checked += 1
            with open(path, "rb") as f:
                try:
                    machine = check_elf(f)
                except NotStandalone as reason:
                    failed += 1
                    print("FAIL {}: {}; it is not standalone, and cannot exec where that is missing".format(path, reason), file=err)
                    continue
            print("ok   {}: ELF64 {}, no PT_INTERP".format(path, machine), file=out)
    if checked == 0:
        print("check_elf_standalone: no ELF file under {}, so nothing was checked".format(", ".join(roots)), file=err)
        return 1
    if failed:
        print("check_elf_standalone: {} of {} ELF files are not standalone".format(failed, checked), file=err)
        return 1
    if expect is not None and checked != expect:
        print("check_elf_standalone: found {} ELF files under {}, expected {}".format(
            checked, ", ".join(roots), expect), file=err)
        return 1
    print("check_elf_standalone: all {} ELF files are standalone".format(checked), file=out)
    return 0


USAGE = "usage: check_elf_standalone.py [--expect N] [DIR ...]    (default: dist)"


def parse_expect(argv):
    """parse_expect takes a leading --expect N off argv and returns (N, the
    rest), N being None when it is absent. A value that is not a positive
    integer raises ValueError."""
    if argv[:1] != ["--expect"]:
        return None, argv
    if len(argv) < 2 or not argv[1].isdigit() or int(argv[1]) < 1:
        raise ValueError("--expect takes a positive integer")
    return int(argv[1]), argv[2:]


def main(argv, out=sys.stdout, err=sys.stderr):
    if argv and argv[0] in ("-h", "--help"):
        print(USAGE, file=out)
        return 0
    try:
        expect, roots = parse_expect(argv)
    except ValueError as reason:
        print("check_elf_standalone: {}\n{}".format(reason, USAGE), file=err)
        return 2
    if any(arg.startswith("-") for arg in roots):
        print(USAGE, file=err)
        return 2
    return check_tree(roots or ["dist"], out, err, expect)


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
