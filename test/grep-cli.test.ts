import { test } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

function builtRepo(): string {
  const dir = mkdtempSync(join(tmpdir(), "graft-grep-cli-"));
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
  execFileSync(process.execPath, ["--import", "tsx", "src/cli.ts", "build", dir], { stdio: "pipe" });
  return dir;
}

function builtGoCli(): string {
  const dir = mkdtempSync(join(tmpdir(), "graft-grep-go-cli-"));
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

  for (const args of cases) {
    const typescript = runCli(args);
    const go = runGoCli(binary, args);
    assert.equal(go.status, typescript.status, `status mismatch for ${args.join(" ")}`);
    assert.equal(go.stderr, typescript.stderr, `stderr mismatch for ${args.join(" ")}`);
    if (typescript.stdout === "") {
      assert.equal(go.stdout, "", `stdout mismatch for ${args.join(" ")}`);
      continue;
    }
    assert.deepEqual(JSON.parse(go.stdout), JSON.parse(typescript.stdout), `JSON mismatch for ${args.join(" ")}`);
  }
});