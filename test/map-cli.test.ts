import { test } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

function builtRepo(): string {
  const dir = mkdtempSync(join(tmpdir(), "graft-map-cli-"));
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
  execFileSync(process.execPath, ["--import", "tsx", "src/cli.ts", "build", dir], { stdio: "pipe" });
  return dir;
}

function builtGoCli(): string {
  const dir = mkdtempSync(join(tmpdir(), "graft-map-go-cli-"));
  const binary = join(dir, "graft");
  execFileSync("go", ["build", "-o", binary, "./cmd/graft"], { stdio: "pipe" });
  return binary;
}

function runCli(args: string[]): { stdout: string; stderr: string; status: number } {
  try {
    const stdout = execFileSync(process.execPath, ["--import", "tsx", "src/cli.ts", ...args], {
      encoding: "utf8",
      stdio: ["ignore", "pipe", "pipe"],
    });
    return { stdout, stderr: "", status: 0 };
  } catch (err) {
    const error = err as { stdout?: string; stderr?: string; status?: number };
    return { stdout: error.stdout ?? "", stderr: error.stderr ?? "", status: error.status ?? 1 };
  }
}

function runGoCli(binary: string, args: string[]): { stdout: string; stderr: string; status: number } {
  try {
    const stdout = execFileSync(binary, args, {
      cwd: process.cwd(),
      encoding: "utf8",
      stdio: ["ignore", "pipe", "pipe"],
    });
    return { stdout, stderr: "", status: 0 };
  } catch (err) {
    const error = err as { stdout?: string; stderr?: string; status?: number };
    return { stdout: error.stdout ?? "", stderr: error.stderr ?? "", status: error.status ?? 1 };
  }
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

  for (const { args, json } of cases) {
    const typescript = runCli(args);
    const go = runGoCli(binary, args);
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