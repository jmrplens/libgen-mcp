#!/usr/bin/env node
// cli.js resolves the prebuilt libgen-mcp binary for the current platform and
// hands control to it. The binary ships inside a per-platform optional
// dependency (see the package's optionalDependencies); npm installs only the
// one whose os/cpu match, and this launcher runs it.
//
// It is a thin shim on purpose. The server speaks MCP over stdio, so stdio is
// inherited untouched and this file never writes to stdout — a stray byte there
// would corrupt the JSON-RPC stream. argv is forwarded verbatim, the
// environment is passed through unmodified (every LIBGEN_MCP_* variable is the
// server's to read), and the child's exit code and terminating signal are
// mirrored so `npx` callers and process supervisors see the real outcome.
//
// The one thing a shim cannot leave to the operating system is stopping the
// server. A client or a supervisor signals the process it started, which is
// this launcher, or npm under npx, and a signal sent to one process never
// reaches its children. So the launcher waits for the server in the event loop
// rather than blocking in spawnSync, where no handler of its own could run,
// and on POSIX:
//
//   - SIGTERM, SIGINT and SIGHUP are passed on to the server, which then runs
//     its own shutdown (and in HTTP mode its drain) instead of being orphaned
//     with its port still bound;
//   - under npm (npx, or an npm script) it also watches its parent. A signal
//     sent to npx stops at npm's shell: npm passes it to the `sh -c` it runs
//     this launcher through, the shell dies, and the launcher is reparented
//     without ever being signalled. Its parent changing is how it learns that,
//     and it then sends the server SIGTERM. Started any other way, the parent
//     is the client or a shell, and a server deliberately left running after
//     it (an HTTP server put in the background) is not stopped.
//
// On Windows neither applies, and the launcher only waits. There are no POSIX
// signals there: Ctrl+C and closing the console reach every process attached
// to the console, the server included, which shuts down on its own, whereas
// child.kill would end it outright before it could; and a process whose parent
// exits keeps that parent's id, so there is no change to watch.
"use strict";

const { spawn } = require("node:child_process");
const { writeSync } = require("node:fs");
const os = require("node:os");

// The signals passed on to the server, which decides what each does. A Ctrl+C
// in a terminal already reaches the server through the process group, so it
// sees that SIGINT twice; its handler starts the shutdown on the first.
const FORWARDED = process.platform === "win32" ? [] : ["SIGTERM", "SIGINT", "SIGHUP"];

// How often the parent is checked under npm. A second is the longest the
// server outlives npm's shell, and the check costs nothing: the timer is
// unref'd, so it never keeps the launcher alive by itself.
const PARENT_POLL_MS = 1000;

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
// It is for the refusals made before the server runs: once it does, the
// launcher writes with report instead.
function fail(message) {
  process.stderr.write(`libgen-mcp: ${message}\n`);
  process.exitCode = 1;
}

// report writes to file descriptor 2 directly. Opening process.stderr would
// switch the pipe the server shares into non-blocking mode under its feet.
function report(message) {
  try {
    writeSync(2, `libgen-mcp: ${message}\n`);
  } catch {
    // Nowhere left to say it; the exit status still carries the outcome.
  }
}

// mirror ends the launcher the way the server ended. A child killed by a
// signal reports a null code and a signal name, and the launcher re-raises it
// rather than masking a SIGTERM as exit 0. The handler that would pass that
// signal on goes first, or the launcher would catch its own signal and send it
// to a server that is already gone.
function mirror(code, signal) {
  if (signal) {
    process.removeAllListeners(signal);
    process.kill(process.pid, signal);
    // If the re-raise did not terminate us (signal ignored, or no default
    // disposition), fall back to the conventional 128+signal code so the caller
    // still sees the child's terminating signal rather than a bare failure.
    const signum = os.constants.signals[signal];
    process.exitCode = signum ? 128 + signum : 1;
    return;
  }
  process.exitCode = code === null ? 1 : code;
}

// watchParent sends the server SIGTERM once the launcher's parent changes,
// which under npm means npm's shell is gone. The parent is read once, now.
function watchParent(child) {
  const parent = process.ppid;
  const watch = setInterval(() => {
    if (process.ppid === parent) return;
    clearInterval(watch);
    child.kill("SIGTERM");
  }, PARENT_POLL_MS);
  watch.unref();
}

function run(binary) {
  // The handlers are installed before the server starts, so a signal that
  // arrives while it is starting is passed on rather than ending the launcher
  // with the server left behind. Node runs a handler on a later turn of the
  // event loop, by which time `child` is set.
  let child = null;
  for (const signal of FORWARDED) {
    process.on(signal, () => child.kill(signal));
  }

  child = spawn(binary, process.argv.slice(2), { stdio: "inherit" });

  // npm sets npm_lifecycle_event for whatever it runs, npx included ("npx"),
  // and runs it through a shell whose death is the only sign the launcher gets
  // that npm was told to stop.
  if (FORWARDED.length > 0 && process.env.npm_lifecycle_event) watchParent(child);

  child.on("error", (err) => {
    // Emitted when the binary could not be started, and, rarely, when a signal
    // could not be delivered to it. Only the first decides the outcome: a
    // server that is running still ends with an "exit" event, mirrored below.
    if (child.pid === undefined) {
      for (const signal of FORWARDED) process.removeAllListeners(signal);
      report(`failed to start the binary: ${err.message}`);
      process.exitCode = 1;
      return;
    }
    report(err.message);
  });

  // A binary that never started may still report an exit, with the error's
  // code; the outcome is the error's, set above.
  child.on("exit", (code, signal) => {
    if (child.pid !== undefined) mirror(code, signal);
  });
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

  run(binary);
}

main();
