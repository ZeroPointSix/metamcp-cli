#!/usr/bin/env node
// Builds the npm packages for a release:
//
//   node npm/build.mjs <version> [outDir]
//
// Cross-compiles the Go binary for every target into one platform package per
// target (with "os"/"cpu" so npm installs only the matching one) and stamps the
// version into the launcher package. Publish with:
//
//   for d in <outDir>/*; do npm publish "$d" --access public; done
//
// (platform packages first, launcher last; the loop order already does that
// because "mcli" sorts after "mcli-*".)
import { execFileSync } from "node:child_process";
import { cpSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const root = path.dirname(here);
const version = (process.argv[2] || "").replace(/^v/, "");
const out = path.resolve(process.argv[3] || path.join(here, "dist"));
if (!/^\d+\.\d+\.\d+(-[\w.]+)?$/.test(version)) {
  console.error("usage: node npm/build.mjs <semver> [outDir]");
  process.exit(2);
}

const TARGETS = [
  { goos: "linux", goarch: "amd64", os: "linux", cpu: "x64" },
  { goos: "linux", goarch: "arm64", os: "linux", cpu: "arm64" },
  { goos: "darwin", goarch: "amd64", os: "darwin", cpu: "x64" },
  { goos: "darwin", goarch: "arm64", os: "darwin", cpu: "arm64" },
  { goos: "windows", goarch: "amd64", os: "win32", cpu: "x64" },
  { goos: "windows", goarch: "arm64", os: "win32", cpu: "arm64" },
];

rmSync(out, { recursive: true, force: true });
const launcher = JSON.parse(readFileSync(path.join(here, "mcli", "package.json"), "utf8"));
launcher.version = version;
launcher.optionalDependencies = {};

for (const t of TARGETS) {
  const name = `@zeropointsix/mcli-${t.os}-${t.cpu}`;
  const dir = path.join(out, `mcli-${t.os}-${t.cpu}`);
  const exe = t.goos === "windows" ? "mcli.exe" : "mcli";
  mkdirSync(path.join(dir, "bin"), { recursive: true });
  console.log(`building ${name}`);
  execFileSync(
    "go",
    [
      "build",
      "-trimpath",
      "-ldflags",
      `-s -w -X github.com/ZeroPointSix/metamcp-cli/internal/version.Version=v${version}`,
      "-o",
      path.join(dir, "bin", exe),
      "./cmd/mcli",
    ],
    { cwd: root, stdio: "inherit", env: { ...process.env, CGO_ENABLED: "0", GOOS: t.goos, GOARCH: t.goarch } },
  );
  writeFileSync(
    path.join(dir, "package.json"),
    JSON.stringify(
      {
        name,
        version,
        description: `mcli binary for ${t.os}-${t.cpu}`,
        license: launcher.license,
        repository: launcher.repository,
        os: [t.os],
        cpu: [t.cpu],
        files: ["bin"],
        preferUnplugged: true,
      },
      null,
      2,
    ) + "\n",
  );
  launcher.optionalDependencies[name] = version;
}

const launcherDir = path.join(out, "mcli");
mkdirSync(path.join(launcherDir, "bin"), { recursive: true });
cpSync(path.join(here, "mcli", "bin", "mcli.js"), path.join(launcherDir, "bin", "mcli.js"));
cpSync(path.join(root, "README.md"), path.join(launcherDir, "README.md"));
writeFileSync(path.join(launcherDir, "package.json"), JSON.stringify(launcher, null, 2) + "\n");
console.log(`packages written to ${out}`);
