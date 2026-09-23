import { test } from "node:test";
import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { captureGolden, snapshotGoldenFiles } from "./goldens.js";

function cliEnv(home: string): NodeJS.ProcessEnv {
  const env: NodeJS.ProcessEnv = { ...process.env, HOME: home, USERPROFILE: home, DO_NOT_TRACK: "1", GRAFT_NO_REFRESH: "1", COLUMNS: "80" };
  for (const name of ["GRAFT_DIR", "GRAFT_BRAIN_TOKEN", "GRAFT_BRAIN_ID", "CI", "GITHUB_ACTIONS"]) delete env[name];
  return env;
}

function testHome(root: string): string {
  const home = join(root, "home");
  mkdirSync(join(home, ".graft"), { recursive: true });
  writeFileSync(join(home, ".graft", "update-check.json"), JSON.stringify({ latest: "0.0.0", checkedAt: 9_999_999_999_999 }, null, 2));
  return home;
}

function builtRepo(): string {
  const dir = mkdtempSync(join(tmpdir(), "graft-grep-cli-"));
  const home = testHome(dir);
  mkdirSync(join(dir, "src"), { recursive: true });
  writeFileSync(
    join(dir, "src", "a.ts"),
    [
      'console.log("NEEDLE module");',
      "",
      "export function root(): void {",
      '  console.log("NEEDLE root");',
      "}",
      "",
    ].join("\n"),
  );
  writeFileSync(
    join(dir, "src", "b.ts"),
    [
      "export function other(): void {",
      '  console.log("NEEDLE other");',
      "}",
      "",
    ].join("\n"),
  );
  execFileSync(process.execPath, ["--import", "tsx", "src/cli.ts", "build", dir], { stdio: "pipe", env: cliEnv(home) });
  return dir;
}

function builtGoCli(): string {
  const dir = mkdtempSync(join(tmpdir(), "graft-grep-go-cli-"));
  const binary = join(dir, "graft");
  execFileSync("go", ["build", "-o", binary, "./cmd/graft"], { stdio: "pipe" });
  return binary;
}

function runCli(args: string[], home: string): { stdout: string; stderr: string; status: number } {
  const result = spawnSync(process.execPath, ["--import", "tsx", "src/cli.ts", ...args], {
    encoding: "utf8",
    env: cliEnv(home),
  });
  return { stdout: result.stdout, stderr: result.stderr, status: result.status ?? 1 };
}

function runGoCli(binary: string, args: string[], home: string): { stdout: string; stderr: string; status: number } {
  const result = spawnSync(binary, args, { cwd: process.cwd(), encoding: "utf8", env: cliEnv(home) });
  return { stdout: result.stdout, stderr: result.stderr, status: result.status ?? 1 };
}

test("Go grep CLI matches TypeScript JSON, exit codes, and diagnostics", () => {
  const dir = builtRepo();
  const binary = builtGoCli();
  const missingGraph = mkdtempSync(join(tmpdir(), "graft-grep-missing-"));
  const cases = [
    ["grep", "NEEDLE", dir, "--json", "--no-refresh"],
    ["grep", "needle", dir, "--ignore-case", "--fixed", "--in", "src/a.ts", "--json", "--no-refresh"],
    ["grep", "ABSENT", dir, "--json", "--no-refresh"],
    ["grep", "[", dir, "--json", "--no-refresh"],
    ["grep", "NEEDLE", missingGraph, "--json", "--no-refresh"],
  ];

  for (const [index, args] of cases.entries()) {
    const root = args[2];
    const home = testHome(root);
    const inputs = snapshotGoldenFiles(root);
    const typescript = runCli(args, home);
    const go = runGoCli(binary, args, home);
    if (process.env.GRAFT_CAPTURE_GOLDENS === "1") {
      captureGolden(`per-command/grep-cli/${String(index + 1).padStart(3, "0")}`, {
        args,
        env: { DO_NOT_TRACK: "1", GRAFT_NO_REFRESH: "1", COLUMNS: "80" },
        inputs,
        status: typescript.status,
        stdout: typescript.stdout,
        stderr: typescript.stderr,
        files: {},
        normalize: { [root]: "<REPO>", [home]: "<HOME>" },
      });
    }
    assert.equal(go.status, typescript.status, `status mismatch for ${args.join(" ")}`);
    assert.equal(go.stderr, typescript.stderr, `stderr mismatch for ${args.join(" ")}`);
    if (typescript.stdout === "") {
      assert.equal(go.stdout, "", `stdout mismatch for ${args.join(" ")}`);
      continue;
    }
    assert.deepEqual(JSON.parse(go.stdout), JSON.parse(typescript.stdout), `JSON mismatch for ${args.join(" ")}`);
  }
});
