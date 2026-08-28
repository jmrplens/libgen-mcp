// build-npm.mjs assembles the npm publish set from the release binaries.
//
// The npm distribution is one thin launcher package plus one package per
// platform that carries the matching prebuilt binary (the esbuild/biome model:
// npm installs only the package whose os/cpu fit, and no code runs at install
// time). This script is the single place the two vocabularies meet — Node's
// platform/arch names on the npm side, Go's GOOS/GOARCH on the asset side — so
// nothing downstream has to translate.
//
// Usage:
//   node scripts/build-npm.mjs --binaries <dir> --version <x.y.z> [--out <dir>]
//   node scripts/build-npm.mjs --sync-only --version <x.y.z>
//
// <dir> holds the release assets under their published names
// (libgen-mcp-linux-amd64, …). Output is one directory per package under
// <out> (default npm/packages), plus the main package's version and dependency
// pins rewritten in place under npm/libgen-mcp.
//
// --sync-only rewrites just the committed main package.json (version and the
// optionalDependency pins) and builds nothing. It needs no binaries, so the
// release version-stamp step can keep the checked-in file honest between
// releases without staging a whole distribution.

import { chmodSync, copyFileSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const repoRoot = join(dirname(fileURLToPath(import.meta.url)), "..");

// PLATFORMS is the whole distribution matrix. `key` is the npm suffix and the
// runtime lookup the launcher performs; `os`/`cpu` gate the install; `asset` is
// the release filename the binary is copied from. Keep this in lockstep with
// the launcher's supported set and the main package's optionalDependencies.
const PLATFORMS = [
  { key: "linux-x64", os: "linux", cpu: "x64", asset: "libgen-mcp-linux-amd64", exe: false },
  { key: "linux-arm64", os: "linux", cpu: "arm64", asset: "libgen-mcp-linux-arm64", exe: false },
  { key: "darwin-x64", os: "darwin", cpu: "x64", asset: "libgen-mcp-darwin-amd64", exe: false },
  { key: "darwin-arm64", os: "darwin", cpu: "arm64", asset: "libgen-mcp-darwin-arm64", exe: false },
  { key: "win32-x64", os: "win32", cpu: "x64", asset: "libgen-mcp-windows-amd64.exe", exe: true },
  { key: "win32-arm64", os: "win32", cpu: "arm64", asset: "libgen-mcp-windows-arm64.exe", exe: true },
];

function parseArgs(argv) {
  const out = { binaries: null, version: null, out: join(repoRoot, "npm", "packages"), syncOnly: false };
  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];
    if (arg === "--binaries") out.binaries = argv[++i];
    else if (arg === "--version") out.version = argv[++i];
    else if (arg === "--out") out.out = argv[++i];
    else if (arg === "--sync-only") out.syncOnly = true;
    else throw new Error(`unknown argument: ${arg}`);
  }
  if (!out.version) throw new Error("--version <x.y.z> is required");
  // Anchored at both ends: an unanchored test accepts "1.7.2invalid" and stamps
  // it into every manifest, which npm then rejects far from the cause.
  if (!/^\d+\.\d+\.\d+$/.test(out.version)) throw new Error(`--version must be semver, got ${out.version}`);
  if (!out.syncOnly && !out.binaries) throw new Error("--binaries <dir> is required (or pass --sync-only)");
  return out;
}

const mainRepository = {
  type: "git",
  url: "git+https://github.com/jmrplens/libgen-mcp.git",
};

// writePlatformPackage emits one per-platform package: the binary renamed to a
// stable name the launcher resolves, made executable so the bit survives into
// the tarball, and a package.json whose os/cpu confine the install to the
// platform it serves.
function writePlatformPackage(plat, version, binariesDir, outDir) {
  const dir = join(outDir, plat.key);
  rmSync(dir, { recursive: true, force: true });
  mkdirSync(dir, { recursive: true });

  const binaryName = plat.exe ? "libgen-mcp.exe" : "libgen-mcp";
  const src = join(binariesDir, plat.asset);
  const dst = join(dir, binaryName);
  copyFileSync(src, dst);
  // Explicit 0o755 after the copy: npm records the file mode in the tarball, so
  // a binary packed without the executable bit installs un-runnable on the
  // consumer's machine — and the source asset's mode depends on how it was
  // downloaded.
  chmodSync(dst, 0o755);

  const pkg = {
    name: `@jmrp.io/libgen-mcp-${plat.key}`,
    version,
    description: `libgen-mcp prebuilt binary for ${plat.os} ${plat.cpu}. Installed automatically as an optional dependency of @jmrp.io/libgen-mcp.`,
    license: "MIT",
    author: "José M. Requena Plens",
    homepage: "https://jmrp.io/docs/libgen-mcp",
    repository: mainRepository,
    os: [plat.os],
    cpu: [plat.cpu],
    files: [binaryName],
    preferUnplugged: true,
  };
  writeFileSync(join(dir, "package.json"), JSON.stringify(pkg, null, 2) + "\n");
  writeFileSync(
    join(dir, "README.md"),
    `# @jmrp.io/libgen-mcp-${plat.key}\n\n` +
      `The ${plat.os}/${plat.cpu} binary for ` +
      `[@jmrp.io/libgen-mcp](https://www.npmjs.com/package/@jmrp.io/libgen-mcp). ` +
      "You do not install this directly; it comes in as an optional dependency of the main package.\n",
  );
  return { name: pkg.name, dir };
}

// syncMainPackage rewrites the launcher package's own version and pins every
// optional dependency to the same version, so the whole set moves as one and a
// consumer never resolves a launcher against a mismatched binary package.
function syncMainPackage(version) {
  const dir = join(repoRoot, "npm", "libgen-mcp");
  const path = join(dir, "package.json");
  const pkg = JSON.parse(readFileSync(path, "utf8"));
  pkg.version = version;
  pkg.optionalDependencies = Object.fromEntries(
    PLATFORMS.map((p) => [`@jmrp.io/libgen-mcp-${p.key}`, version]),
  );
  writeFileSync(path, JSON.stringify(pkg, null, 2) + "\n");
  return { name: pkg.name, dir };
}

function main() {
  const args = parseArgs(process.argv.slice(2));

  if (args.syncOnly) {
    const mainPackage = syncMainPackage(args.version);
    process.stdout.write(`npm main package synced to v${args.version} (${mainPackage.name})\n`);
    return;
  }

  mkdirSync(args.out, { recursive: true });

  const platformPackages = PLATFORMS.map((p) =>
    writePlatformPackage(p, args.version, args.binaries, args.out),
  );
  const mainPackage = syncMainPackage(args.version);

  // Publish order matters: the platform packages must exist on the registry
  // before the launcher that lists them, or an install racing the publish
  // resolves optional dependencies that are not there yet.
  process.stdout.write(`npm distribution assembled for v${args.version}\n\n`);
  process.stdout.write("Publish in this order (platform packages first):\n");
  for (const p of platformPackages) process.stdout.write(`  ${p.dir}\n`);
  process.stdout.write(`  ${mainPackage.dir}   (${mainPackage.name})\n`);
}

main();
