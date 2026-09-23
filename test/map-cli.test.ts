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
  const dir = mkdtempSync(join(tmpdir(), "graft-map-cli-"));
  const home = testHome(dir);
  mkdirSync(join(dir, "src", "ask"), { recursive: true });
  mkdirSync(join(dir, "tests"), { recursive: true });
  mkdirSync(join(dir, "docs"), { recursive: true });
  writeFileSync(
    join(dir, "src", "ask", "a.ts"),
    "export function alpha(): number {\n  return 1;\n}\n",
  );
  writeFileSync(
    join(dir, "src", "ask", "b.ts"),
    'import { alpha } from "./a.js";\nexport function beta(): number {\n  return alpha();\n}\n',
  );
  writeFileSync(join(dir, "tests", "t.ts"), "export function testThing(): number { return 1; }\n");
  writeFileSync(join(dir, "docs", "readme.py"), "def document():\n    return 1\n");
  execFileSync(process.execPath, ["--import", "tsx", "src/cli.ts", "build", dir], { stdio: "pipe", env: cliEnv(home) });
  return dir;
}

function builtGoCli(): string {
  const dir = mkdtempSync(join(tmpdir(), "graft-map-go-cli-"));
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

test("Go map CLI matches TypeScript JSON, exit codes, and diagnostics", () => {
  const dir = builtRepo();
  const binary = builtGoCli();
  const missingGraph = mkdtempSync(join(tmpdir(), "graft-map-missing-"));
  const cases = [
    { args: ["map", dir, "--json", "--no-refresh"], json: true },
    { args: ["map", dir, "--max-dirs", "1", "--json", "--no-refresh"], json: true },
    { args: ["map", dir, "--max-dirs", "0", "--json", "--no-refresh"], json: false },
    { args: ["map", dir, "--no-refresh"], json: false },
    { args: ["map", missingGraph, "--json", "--no-refresh"], json: true },
    { args: ["map", missingGraph, "--max-dirs", "0", "--json", "--no-refresh"], json: true },
  ];

  for (const [index, { args, json }] of cases.entries()) {
    const root = args[1];
    const home = testHome(root);
    const inputs = snapshotGoldenFiles(root);
    const typescript = runCli(args, home);
    const go = runGoCli(binary, args, home);
    if (process.env.GRAFT_CAPTURE_GOLDENS === "1") {
      captureGolden(`per-command/map-cli/${String(index + 1).padStart(3, "0")}`, {
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
    if (!json) {
      assert.equal(go.stdout, typescript.stdout, `human output mismatch for ${args.join(" ")}`);
      continue;
    }
    if (typescript.stdout === "") {
      assert.equal(go.stdout, "", `stdout mismatch for ${args.join(" ")}`);
      continue;
    }
    assert.deepEqual(JSON.parse(go.stdout), JSON.parse(typescript.stdout), `JSON mismatch for ${args.join(" ")}`);
  }
});
