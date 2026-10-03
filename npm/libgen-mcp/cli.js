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
// The server is told to stop at most once. It starts its graceful shutdown on
// the first SIGINT or SIGTERM and restores the default action at once, so a
// second one ends it outright, drain and all. That is the operator's escape
// hatch when the binary is run directly, and a launcher that relayed every
// signal it saw would pull it on the operator's behalf: a Ctrl+C reaches the
// server through the terminal's process group as well as through this
// launcher, and the parent watch could add a third. So only the first
// terminating signal is acted on, the watch stays quiet once it has been, and
// a SIGINT or SIGHUP is not relayed at all when stdin is a terminal, since
// that is the terminal's own signal and has already reached the server. What
// the launcher cannot see is a signal sent to every process at once by other
// means (a process-group kill outside a terminal, or systemd's default
// KillMode=control-group): the server then receives the launcher's copy too.
// Run the binary directly there, or use KillMode=mixed.
//
// On Windows neither applies, and the launcher only waits. There are no POSIX
// signals there: Ctrl+C and closing the console reach every process attached
// to the console, the server included, which shuts down on its own, whereas
// child.kill would end it outright before it could; and a process whose parent
// exits keeps that parent's id, so there is no change to watch.
"use strict";

// Read before anything else, because npm's shell can die while Node is still
// booting: a parent read any later may already be the process that reaped the
// launcher, and a watch comparing against that would never fire.
const PARENT_AT_START = process.ppid;

const { spawn } = require("node:child_process");
const { writeSync } = require("node:fs");
const os = require("node:os");
const tty = require("node:tty");

// The signals passed on to the server, which decides what each does. Only the
// first of them that arrives is acted on (see above).
const FORWARDED = process.platform === "win32" ? [] : ["SIGTERM", "SIGINT", "SIGHUP"];

// The signals a terminal sends to its whole foreground process group, the
// server included, so relaying them from a terminal would deliver them twice.
const TERMINAL_SIGNALS = new Set(["SIGINT", "SIGHUP"]);

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

// parentGone says whether npm's shell is no longer the launcher's parent: the
// parent differs from the one read at startup, or the launcher already belongs
// to init, which happens when the shell died before even that first read.
function parentGone() {
  return process.ppid !== PARENT_AT_START || process.ppid === 1;
}

// watchParent asks for the server to stop once npm's shell is gone, at once
// when it went while the launcher was starting, and otherwise on the first
// check that finds it gone.
//
// Either way the request waits for the end of the current turn of the event
// loop. A signal is handed to its listener in the turn's I/O phase, which comes
// after its timers, so a launcher stalled across a Ctrl+C and the death of the
// shell that Ctrl+C also reached would otherwise run the overdue check first,
// and send the server a SIGTERM on top of the SIGINT it is already draining
// on. Deferred with setImmediate, the request runs after that phase, by which
// time any signal that had already arrived has been acted on and the request
// finds there is nothing left to do.
function watchParent(stop) {
  const stopAfterPendingSignals = () => setImmediate(() => stop("SIGTERM"));
  if (parentGone()) {
    stopAfterPendingSignals();
    return;
  }
  const watch = setInterval(() => {
    if (!parentGone()) return;
    clearInterval(watch);
    stopAfterPendingSignals();
  }, PARENT_POLL_MS);
  watch.unref();
}

function run(binary) {
  // The handlers are installed before the server starts, so a signal that
  // arrives while it is starting is passed on rather than ending the launcher
  // with the server left behind. Node runs a handler on a later turn of the
  // event loop, by which time `child` is set.
  let child = null;
  // The first terminating signal, from wherever it came, is the only one
  // acted on: a second would end the server's graceful shutdown outright.
  let told = false;
  const fromTerminal = tty.isatty(0);
  const stop = (signal, received = false) => {
    if (told) return;
    told = true;
    // A terminal sent this one to the server too; relaying it would make two.
    if (received && fromTerminal && TERMINAL_SIGNALS.has(signal)) return;
    child.kill(signal);
  };
  for (const signal of FORWARDED) {
    process.on(signal, () => stop(signal, true));
  }

  child = spawn(binary, process.argv.slice(2), { stdio: "inherit" });

  // npm sets npm_lifecycle_event for whatever it runs, npx included ("npx"),
  // and runs it through a shell whose death is the only sign the launcher gets
  // that npm was told to stop.
  if (FORWARDED.length > 0 && process.env.npm_lifecycle_event) watchParent(stop);

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
