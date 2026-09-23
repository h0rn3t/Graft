/**
 * The published `graft` launcher (bin/graft.js): Go-scoped invocations run the
 * packaged native binary, and only `build --deep`, `viz` and
 * `blast --export-viz` — or a platform without a binary — reach the TypeScript
 * CLI.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import { chmodSync, copyFileSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { nativeBinaryName, routesToTypeScript } from "../bin/graft.js";
import { tmpRepo } from "./helpers.js";

test("only deep build and visualization invocations route to TypeScript", () => {
  const cases: Array<[string[], boolean]> = [
    [["build", "--deep"], true],
    [["build", ".", "--deep"], true],
    [["--dir", "ctx", "build", "--deep"], true],
    [["--api-key=k", "build", "-e", ".ts", "--deep"], true],
    [["viz"], true],
    [["viz", "--export", "out"], true],
    [["--model", "m", "viz"], true],
    [["blast", "--export-viz", "out"], true],
    [["blast", "--base", "main", "--export-viz=out"], true],
    [[], false],
    [["--version"], false],
    [["--help"], false],
    [["build"], false],
    [["build", "--no-reuse", "."], false],
    [["build", "--", "--deep"], false],
    [["--dir", "viz", "ask", "x"], false],
    [["ask", "viz"], false],
    [["ask", "--deep"], false],
    [["blast", "--format", "json"], false],
    [["mcp"], false],
    [["init", "--dry-run"], false],
    [["brain", "status"], false],
  ];
  for (const [args, want] of cases) assert.equal(routesToTypeScript(args), want, `graft ${args.join(" ")}`);
});

test("native binary names carry the platform, the architecture and .exe on Windows", () => {
  assert.equal(nativeBinaryName("darwin", "arm64"), "graft-darwin-arm64");
  assert.equal(nativeBinaryName("linux", "x64"), "graft-linux-x64");
  assert.equal(nativeBinaryName("win32", "x64"), "graft-win32-x64.exe");
});

test("the launcher runs the packaged binary, and TypeScript for its routes or a missing binary", { skip: process.platform === "win32" }, async () => {
  const pkg = tmpRepo("launcher");
  mkdirSync(join(pkg, "bin"));
  mkdirSync(join(pkg, "dist"));
  copyFileSync("bin/graft.js", join(pkg, "bin", "graft.js"));
  writeFileSync(join(pkg, "package.json"), JSON.stringify({ type: "module" }));
  writeFileSync(join(pkg, "dist", "cli.js"), 'console.log(["ts", ...process.argv.slice(2)].join(" "));\n');
  const binary = join(pkg, "bin", nativeBinaryName());
  writeFileSync(binary, '#!/bin/sh\necho "go $*"\necho "err" >&2\nexit 3\n');
  chmodSync(binary, 0o755);

  const run = (...args: string[]) => spawnSync(process.execPath, [join(pkg, "bin", "graft.js"), ...args], { encoding: "utf8" });

  const native = run("ask", "store get");
  assert.equal(native.stdout, "go ask store get\n");
  assert.equal(native.stderr, "err\n");
  assert.equal(native.status, 3, "the binary's exit status passes through");

  assert.equal(run("build", "--deep").stdout, "ts build --deep\n");
  assert.equal(run("viz").stdout, "ts viz\n");
  assert.equal(run("blast", "--export-viz", "out").stdout, "ts blast --export-viz out\n");

  // A host stops an MCP server by signalling the launcher, which must pass it on.
  writeFileSync(binary, "#!/bin/sh\ntrap 'echo stopped; exit 7' TERM\necho ready\nwhile :; do sleep 0.05; done\n");
  const stopped = await new Promise<{ code: number | null; out: string }>((done) => {
    const child = spawn(process.execPath, [join(pkg, "bin", "graft.js"), "mcp"], { stdio: ["ignore", "pipe", "inherit"] });
    let out = "";
    child.stdout.on("data", (chunk) => {
      out += chunk;
      if (out === "ready\n") child.kill("SIGTERM");
    });
    child.on("exit", (code) => done({ code, out }));
  });
  assert.deepEqual(stopped, { code: 7, out: "ready\nstopped\n" });

  rmSync(binary);
  const fallback = run("ask", "x");
  assert.equal(fallback.stdout, "ts ask x\n");
  assert.equal(fallback.status, 0);
});
