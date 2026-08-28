#!/usr/bin/env node
// cli.js resolves the prebuilt libgen-mcp binary for the current platform and
// hands control to it. The binary ships inside a per-platform optional
// dependency (see the package's optionalDependencies); npm installs only the
// one whose os/cpu match, and this launcher execs it.
//
// It is a thin shim on purpose. The server speaks MCP over stdio, so stdio is
// inherited untouched and this file never writes to stdout — a stray byte there
// would corrupt the JSON-RPC stream. argv is forwarded verbatim, the
// environment is passed through unmodified (every LIBGEN_MCP_* variable is the
// server's to read), and the child's exit code and terminating signal are
// mirrored so `npx` callers and process supervisors see the real outcome.
"use strict";

const { spawnSync } = require("node:child_process");
const os = require("node:os");

// platformKey maps Node's platform/arch names to the per-platform package
// suffix. The suffixes follow Node's vocabulary (win32, x64), not Go's
// (windows, amd64); the release generator translates when it builds each
// package, so the two never have to agree at runtime.
function platformKey() {
  const supported = {
    "linux-x64": true,
    "linux-arm64": true,
    "darwin-x64": true,
    "darwin-arm64": true,
    "win32-x64": true,
    "win32-arm64": true,
  };
  const key = `${process.platform}-${process.arch}`;
  return supported[key] ? key : null;
}

function binaryName() {
  return process.platform === "win32" ? "libgen-mcp.exe" : "libgen-mcp";
}

// resolveBinary finds the binary inside the matching per-platform package.
// require.resolve walks the same node_modules the launcher was loaded from, so
// it finds the dependency whether the install is flat, nested, hoisted, or run
// through npx's throwaway prefix.
function resolveBinary(key) {
  const pkg = `@jmrp.io/libgen-mcp-${key}`;
  try {
    return require.resolve(`${pkg}/${binaryName()}`);
  } catch {
    return null;
  }
}

// fail reports a diagnostic and marks the run failed. It sets `exitCode` rather
// than calling process.exit(), which would drop a stderr write that has not
// flushed yet — writes to a pipe are asynchronous, and the message explaining
// why the launcher gave up is the one least worth losing. Callers return
// immediately after; the process then exits on its own once stderr has drained.
function fail(message) {
  process.stderr.write(`libgen-mcp: ${message}\n`);
  process.exitCode = 1;
}

function main() {
  const key = platformKey();
  if (!key) {
    fail(
      `unsupported platform ${process.platform}/${process.arch}. ` +
        "Prebuilt binaries exist for linux, macOS and Windows on x64 and arm64; " +
        "for anything else, build from source or use a released binary directly " +
        "(https://github.com/jmrplens/libgen-mcp/releases).",
    );
    return;
  }

  const binary = resolveBinary(key);
  if (!binary) {
    // The platform is supported but its package is absent. The usual cause is
    // an install that skipped optional dependencies (npm install --no-optional,
    // or a lockfile pinned on a different OS), not a broken release.
    fail(
      `the @jmrp.io/libgen-mcp-${key} package is not installed. ` +
        "It is an optional dependency that carries the binary for this platform; " +
        "reinstall without --no-optional, or delete node_modules and the lockfile " +
        "and install again.",
    );
    return;
  }

  const result = spawnSync(binary, process.argv.slice(2), { stdio: "inherit" });

  if (result.error) {
    fail(`failed to start the binary: ${result.error.message}`);
    return;
  }
  // A child killed by a signal reports null status and a signal name. Re-raise
  // it so the launcher dies the same way rather than masking a SIGINT as exit 0.
  if (result.signal) {
    process.kill(process.pid, result.signal);
    // If the re-raise did not terminate us (signal ignored, or no default
    // disposition), fall back to the conventional 128+signal code so the caller
    // still sees the child's terminating signal rather than a bare failure.
    const signum = os.constants.signals[result.signal];
    process.exitCode = signum ? 128 + signum : 1;
    return;
  }
  process.exitCode = result.status === null ? 1 : result.status;
}

main();
