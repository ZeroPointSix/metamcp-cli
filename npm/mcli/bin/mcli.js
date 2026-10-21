#!/usr/bin/env node
// Thin launcher: finds the platform package npm installed through
// optionalDependencies and runs its Go binary with the same arguments.
// There is deliberately no postinstall download: many environments run npm
// with --ignore-scripts.
"use strict";

const { spawnSync } = require("node:child_process");
const path = require("node:path");

const PLATFORMS = {
  "linux x64": "@zeropointsix/mcli-linux-x64",
  "linux arm64": "@zeropointsix/mcli-linux-arm64",
  "darwin x64": "@zeropointsix/mcli-darwin-x64",
  "darwin arm64": "@zeropointsix/mcli-darwin-arm64",
  "win32 x64": "@zeropointsix/mcli-win32-x64",
  "win32 arm64": "@zeropointsix/mcli-win32-arm64",
};

const RELEASES = "https://github.com/ZeroPointSix/metamcp-cli/releases";

function fail(message) {
  // Keep the stdout contract: exactly one JSON object.
  const error = {
    code: "E_INSTALL",
    message,
    hint: `Reinstall without --no-optional / --omit=optional, or download a binary from ${RELEASES} and put it on PATH.`,
    exit: 2,
  };
  process.stdout.write(JSON.stringify({ ok: false, error }, null, 2) + "\n");
  process.stderr.write(`mcli: ${message}\n`);
  process.exit(2);
}

function binaryPath() {
  if (process.env.MCLI_BINARY) return process.env.MCLI_BINARY;
  const key = `${process.platform} ${process.arch}`;
  const pkg = PLATFORMS[key];
  if (!pkg) fail(`unsupported platform ${key}`);
  const exe = process.platform === "win32" ? "mcli.exe" : "mcli";
  try {
    return require.resolve(`${pkg}/bin/${exe}`);
  } catch {
    fail(`platform package ${pkg} is not installed`);
  }
}

const bin = binaryPath();
const result = spawnSync(bin, process.argv.slice(2), { stdio: "inherit", windowsHide: true });
if (result.error) fail(`cannot run ${path.basename(bin)}: ${result.error.message}`);
if (result.signal) {
  process.kill(process.pid, result.signal);
} else {
  process.exit(result.status ?? 1);
}
