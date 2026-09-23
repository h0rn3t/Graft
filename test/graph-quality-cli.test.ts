import { test } from "node:test";
import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { dirname } from "node:path";
import { captureGolden, snapshotGoldenFiles } from "./goldens.js";

function writeGraphFixture(invalid: boolean): string {
  const dir = mkdtempSync(join(tmpdir(), invalid ? "graft-quality-invalid-" : "graft-quality-valid-"));
  const nodes = invalid
    ? [
        { id: "a.ts#main", name: "main", kind: "function", path: "a.ts", span: "L1-L3", origin: "ast" },
        { id: "a.ts#main", name: " ", kind: "widget", path: "a.ts", span: "L9-L2", origin: "ast" },
      ]
    : [
        { id: "a.ts", name: "a.ts", kind: "file", path: "a.ts", span: "L1-L3", origin: "ast" },
        { id: "a.ts#main", name: "main", kind: "function", path: "a.ts", span: "L1-L3", origin: "ast" },
      ];
  const edges = invalid
    ? [
        { source: "a.ts#main", target: "a.ts#main", relation: "bogus", confidence: "guessed" },
        { source: "missing", target: "a.ts#main", relation: "references", confidence: "extracted" },
        { source: "a.ts#main", target: "ghost", relation: "calls", confidence: "extracted" },
        { source: "a.ts#main", target: "a.ts#main", relation: "calls", confidence: "extracted" },
      ]
    : [
        { source: "a.ts", target: "a.ts#main", relation: "contains", confidence: "extracted" },
        { source: "a.ts#main", target: "a.ts#main", relation: "calls", confidence: "extracted" },
        { source: "a.ts", target: "node:fs", relation: "imports", confidence: "inferred" },
      ];
  const graph = {
    meta: { version: 1, nodeCount: nodes.length, edgeCount: edges.length, languages: ["typescript"] },
    nodes,
    edges,
  };
  const path = join(dir, "wiring.json");
  writeFileSync(path, JSON.stringify(graph, null, 2) + "\n");
  return path;
}

function writeEmptyGraph(): string {
  const dir = mkdtempSync(join(tmpdir(), "graft-quality-empty-"));
  const path = join(dir, "wiring.json");
  writeFileSync(
    path,
    JSON.stringify({ meta: { version: 1, nodeCount: 0, edgeCount: 0, languages: [] }, nodes: [], edges: [] }, null, 2) + "\n",
  );
  return path;
}

function builtGoCli(): string {
  const dir = mkdtempSync(join(tmpdir(), "graft-quality-go-cli-"));
  const binary = join(dir, "graph-quality");
  execFileSync("go", ["build", "-o", binary, "./cmd/graph-quality"], { stdio: "pipe" });
  return binary;
}

function runTypeScript(args: string[]): { stdout: string; stderr: string; status: number } {
  const result = spawnSync(process.execPath, ["scripts/graph-quality.mjs", ...args], {
    cwd: process.cwd(),
    encoding: "utf8",
    env: { ...process.env, DO_NOT_TRACK: "1" },
  });
  return { stdout: result.stdout, stderr: result.stderr, status: result.status ?? 1 };
}

function runGo(binary: string, args: string[]): { stdout: string; stderr: string; status: number } {
  const result = spawnSync(binary, args, { cwd: process.cwd(), encoding: "utf8", env: { ...process.env, DO_NOT_TRACK: "1" } });
  return { stdout: result.stdout, stderr: result.stderr, status: result.status ?? 1 };
}

test("Go graph-quality matches the TypeScript report and CLI contract", () => {
  const valid = writeGraphFixture(false);
  const invalid = writeGraphFixture(true);
  const empty = writeEmptyGraph();
  const missing = mkdtempSync(join(tmpdir(), "graft-quality-missing-"));
  mkdirSync(join(missing, "graft"), { recursive: true });
  const binary = builtGoCli();
  const cases = [
    { args: [valid, "--json"], json: true },
    { args: [valid], json: false },
    { args: [empty, "--json"], json: true },
    { args: [empty], json: false },
    { args: [invalid, "--json"], json: true },
    { args: [invalid, "--json", "--strict"], json: true },
    { args: [invalid, "--strict"], json: false },
    { args: [missing], json: false },
  ];

  for (const [index, { args, json }] of cases.entries()) {
    const root = args[0].endsWith(".json") ? dirname(args[0]) : args[0];
    const inputs = snapshotGoldenFiles(root);
    const typescript = runTypeScript(args);
    const go = runGo(binary, args);
    if (process.env.GRAFT_CAPTURE_GOLDENS === "1") {
      captureGolden(`per-command/graph-quality-cli/${String(index + 1).padStart(3, "0")}`, {
        args,
        env: { DO_NOT_TRACK: "1" },
        inputs,
        status: typescript.status,
        stdout: typescript.stdout,
        stderr: typescript.stderr,
        files: {},
        normalize: { [root]: "<REPO>" },
      });
    }
    assert.equal(go.status, typescript.status, `status mismatch for ${args.join(" ")}`);
    assert.equal(go.stderr, typescript.stderr, `stderr mismatch for ${args.join(" ")}`);
    if (json && typescript.stdout !== "") {
      assert.deepEqual(JSON.parse(go.stdout), JSON.parse(typescript.stdout), `JSON mismatch for ${args.join(" ")}`);
    } else {
      assert.equal(go.stdout, typescript.stdout, `stdout mismatch for ${args.join(" ")}`);
    }
  }
});
