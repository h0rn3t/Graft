import { test } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

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
  try {
    const stdout = execFileSync(process.execPath, ["scripts/graph-quality.mjs", ...args], {
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

function runGo(binary: string, args: string[]): { stdout: string; stderr: string; status: number } {
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

  for (const { args, json } of cases) {
    const typescript = runTypeScript(args);
    const go = runGo(binary, args);
    assert.equal(go.status, typescript.status, `status mismatch for ${args.join(" ")}`);
    assert.equal(go.stderr, typescript.stderr, `stderr mismatch for ${args.join(" ")}`);
    if (json && typescript.stdout !== "") {
      assert.deepEqual(JSON.parse(go.stdout), JSON.parse(typescript.stdout), `JSON mismatch for ${args.join(" ")}`);
    } else {
      assert.equal(go.stdout, typescript.stdout, `stdout mismatch for ${args.join(" ")}`);
    }
  }
});