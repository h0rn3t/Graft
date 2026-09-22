import { test } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

type CLIResult = { stdout: string; stderr: string; status: number };

function askFixture(): string {
  const dir = mkdtempSync(join(tmpdir(), "graft-ask-cli-"));
  mkdirSync(join(dir, "graft", ".graph"), { recursive: true });
  mkdirSync(join(dir, "src"), { recursive: true });
  const rootSource = 'export function root(): string {\n  return "needle";\n}\n';
  const callerSource = 'export function caller(): string {\n  return root();\n}\n';
  writeFileSync(join(dir, "src", "root.ts"), rootSource);
  writeFileSync(join(dir, "src", "caller.ts"), callerSource);

  const node = (id: string, name: string, kind: string, path: string, signature: string | null, bodyText?: string) => ({
    id,
    name,
    kind,
    path,
    span: "L1-L3",
    signature,
    exported: true,
    origin: "ast",
    body_hash: "",
    chars: Buffer.byteLength(path.endsWith("root.ts") ? rootSource : callerSource),
    ...(bodyText === undefined ? {} : { body_text: bodyText }),
    summary_state: "pending",
    summary: null,
    crux: null,
  });
  const graph = {
    meta: { version: 1, nodeCount: 4, edgeCount: 1, languages: ["ts"] },
    nodes: [
      node("src/root.ts", "root.ts", "file", "src/root.ts", null),
      node("src/root.ts#root", "root", "function", "src/root.ts", "root(): string", "return needle"),
      node("src/caller.ts", "caller.ts", "file", "src/caller.ts", null),
      node("src/caller.ts#caller", "caller", "function", "src/caller.ts", "caller(): string", "return root"),
    ],
    edges: [{ source: "src/caller.ts#caller", target: "src/root.ts#root", relation: "calls", confidence: "extracted" }],
  };
  writeFileSync(join(dir, "graft", ".graph", "wiring.json"), JSON.stringify(graph));
  return dir;
}

function builtGoCLI(): string {
  const dir = mkdtempSync(join(tmpdir(), "graft-ask-go-cli-"));
  const binary = join(dir, "graft");
  execFileSync("go", ["build", "-o", binary, "./cmd/graft"], { stdio: "pipe" });
  return binary;
}

function workspaceFixture(): string {
  const parent = mkdtempSync(join(tmpdir(), "graft-ask-workspace-"));
  mkdirSync(join(parent, "graft"), { recursive: true });
  writeFileSync(
    join(parent, "graft", "workspace.json"),
    JSON.stringify({ version: 1, children: ["repoA", "repoB"] }) + "\n",
  );
  for (const [child, file, name] of [
    ["repoA", "a.ts", "alphaHandler"],
    ["repoB", "b.ts", "betaHandler"],
  ]) {
    const graphDir = join(parent, child, "graft", ".graph");
    mkdirSync(graphDir, { recursive: true });
    const path = `${file}`;
    writeFileSync(join(parent, child, file), `export function ${name}(): number { return 1; }\n`);
    const graph = {
      meta: { version: 1, nodeCount: 2, edgeCount: 0, languages: ["ts"] },
      nodes: [
        {
          id: path,
          name: path,
          kind: "file",
          path,
          span: "L1-L1",
          signature: null,
          exported: false,
          origin: "ast",
          body_hash: "",
          summary_state: "pending",
          summary: null,
          crux: null,
        },
        {
          id: `${path}#${name}`,
          name,
          kind: "function",
          path,
          span: "L1-L1",
          signature: `${name}(): number`,
          exported: true,
          origin: "ast",
          body_hash: "",
          body_text: "handler result",
          summary_state: "pending",
          summary: null,
          crux: null,
        },
      ],
      edges: [],
    };
    writeFileSync(join(graphDir, "wiring.json"), JSON.stringify(graph));
  }
  return parent;
}

function builtWorkspaceRankingFixture(): string {
  const parent = mkdtempSync(join(tmpdir(), "graft-ask-workspace-ranking-"));
  const files: Record<string, Record<string, string>> = {
    repoA: {
      "a.ts": [
        'export function quartzAlphaA() { return "quartz"; }',
        'export function quartzBetaA() { return "quartz"; }',
        'export function quartzGammaA() { return "quartz"; }',
      ].join("\n") + "\n",
      "b.ts": 'export function quartzOmegaB() { return "quartz"; }\n',
    },
    repoB: {
      "a.ts": [
        'export function quartzAlphaC() { return "quartz"; }',
        'export function quartzBetaC() { return "quartz"; }',
      ].join("\n") + "\n",
      "d.ts": 'export function quartzOmegaD() { return "quartz"; }\n',
    },
  };
  for (const [child, childFiles] of Object.entries(files)) {
    mkdirSync(join(parent, child, ".git"), { recursive: true });
    for (const [file, content] of Object.entries(childFiles)) {
      writeFileSync(join(parent, child, file), content);
    }
  }
  execFileSync(process.execPath, ["--import", "tsx", "src/cli.ts", "build", parent], { stdio: "pipe" });
  return parent;
}

function builtWorkspaceGateFixture(): string {
  const parent = mkdtempSync(join(tmpdir(), "graft-ask-workspace-gate-"));
  const files: Record<string, Record<string, string>> = {
    repoStrong: {
      "m.ts": "export function paymentGatewayRefund() { return 1; }\n",
    },
    repoJunk: {
      "m.ts": "export function renderList() { const gateway = 0; return gateway; }\n",
    },
  };
  for (const [child, childFiles] of Object.entries(files)) {
    mkdirSync(join(parent, child, ".git"), { recursive: true });
    for (const [file, content] of Object.entries(childFiles)) {
      writeFileSync(join(parent, child, file), content);
    }
  }
  execFileSync(process.execPath, ["--import", "tsx", "src/cli.ts", "build", parent], { stdio: "pipe" });
  return parent;
}

function builtFileComplementFixture(): string {
  const dir = mkdtempSync(join(tmpdir(), "graft-ask-file-complement-"));
  writeFileSync(
    join(dir, "a.ts"),
    [
      'export function amberShard() { return "amber"; }',
      'export function cobaltShard() { return "cobalt"; }',
    ].join("\n") + "\n",
  );
  writeFileSync(join(dir, "b.ts"), 'export function amberCobalt() { return "amber cobalt"; }\n');
  execFileSync(process.execPath, ["--import", "tsx", "src/cli.ts", "build", dir], { stdio: "pipe" });
  return dir;
}

function builtWorkspaceFileUnionFixture(): string {
  const parent = mkdtempSync(join(tmpdir(), "graft-ask-workspace-file-union-"));
  const files: Record<string, Record<string, string>> = {
    repoA: {
      "amber-cobalt.ts": [
        'export function amberShard() { return "amber"; }',
        'export function cobaltShard() { return "cobalt"; }',
      ].join("\n") + "\n",
      "amber-only.ts": 'export function amberFallback() { return "amber"; }\n',
    },
    repoB: {
      "cobalt.ts": 'export function cobaltFallback() { return "cobalt"; }\n',
    },
  };
  for (const [child, childFiles] of Object.entries(files)) {
    mkdirSync(join(parent, child, ".git"), { recursive: true });
    for (const [file, content] of Object.entries(childFiles)) {
      writeFileSync(join(parent, child, file), content);
    }
  }
  execFileSync(process.execPath, ["--import", "tsx", "src/cli.ts", "build", parent], { stdio: "pipe" });
  return parent;
}

function runTypeScript(args: string[]): CLIResult {
  try {
    const stdout = execFileSync(process.execPath, ["--import", "tsx", "src/cli.ts", ...args], {
      encoding: "utf8",
      stdio: ["ignore", "pipe", "pipe"],
    });
    return { stdout, stderr: "", status: 0 };
  } catch (error) {
    const result = error as { stdout?: string; stderr?: string; status?: number };
    return { stdout: result.stdout ?? "", stderr: result.stderr ?? "", status: result.status ?? 1 };
  }
}

function runGo(binary: string, args: string[]): CLIResult {
  try {
    const stdout = execFileSync(binary, args, {
      encoding: "utf8",
      stdio: ["ignore", "pipe", "pipe"],
    });
    return { stdout, stderr: "", status: 0 };
  } catch (error) {
    const result = error as { stdout?: string; stderr?: string; status?: number };
    return { stdout: result.stdout ?? "", stderr: result.stderr ?? "", status: result.status ?? 1 };
  }
}

test("Go ask CLI matches TypeScript JSON, exit codes, and diagnostics", () => {
  const dir = askFixture();
  const binary = builtGoCLI();
  const missingGraph = mkdtempSync(join(tmpdir(), "graft-ask-missing-"));
  try {
    const cases = [
      { args: ["ask", "who calls root", dir, "--json", "--no-refresh"], json: true },
      { args: ["ask", "needle", dir, "--no-graph-rank", "--json", "--no-refresh"], json: true },
      { args: ["ask", "needle", dir, "--json", "--no-refresh"], json: true },
      { args: ["ask", "needle", dir, "--limit", "1", "--json", "--no-refresh"], json: true },
      { args: ["ask", "absent", dir, "--json", "--no-refresh"], json: true },
      { args: ["ask", "root", missingGraph, "--json", "--no-refresh"], json: true },
      { args: ["ask", "root", dir, "--in", "missing", "--json", "--no-refresh"], json: true },
      { args: ["ask", "who calls root", dir, "--no-refresh"], json: false },
      { args: ["ask", "absent", dir, "--no-refresh"], json: false },
    ];

    for (const { args, json } of cases) {
      const typescript = runTypeScript(args);
      const go = runGo(binary, args);
      assert.equal(go.status, typescript.status, `status mismatch for ${args.join(" ")}`);
      assert.equal(go.stderr, typescript.stderr, `stderr mismatch for ${args.join(" ")}`);
      if (typescript.stdout === "") {
        assert.equal(go.stdout, "", `stdout mismatch for ${args.join(" ")}`);
        continue;
      }
      if (json) {
        assert.deepEqual(JSON.parse(go.stdout), JSON.parse(typescript.stdout), `JSON mismatch for ${args.join(" ")}`);
      } else {
        assert.equal(go.stdout, typescript.stdout, `human output mismatch for ${args.join(" ")}`);
      }
    }

    writeFileSync(
      join(dir, "graft", "needle-guide.md"),
      [
        "---",
        "slug: needle-guide",
        "name: Needle Guide",
        "type: concept",
        "sources:",
        "  - path: src/root.ts",
        "    hash: fixture",
        "links:",
        "  - to: root",
        "    relation: covers",
        "---",
        "Needle guide explains the root retrieval path.",
        "",
      ].join("\n"),
    );
    const conceptArgs = ["ask", "needle guide", dir, "--no-graph-rank", "--json", "--no-refresh"];
    const typescriptConcept = runTypeScript(conceptArgs);
    const goConcept = runGo(binary, conceptArgs);
    assert.equal(goConcept.status, typescriptConcept.status, `status mismatch for ${conceptArgs.join(" ")}`);
    assert.equal(goConcept.stderr, typescriptConcept.stderr, `stderr mismatch for ${conceptArgs.join(" ")}`);
    assert.deepEqual(JSON.parse(goConcept.stdout), JSON.parse(typescriptConcept.stdout), `JSON mismatch for ${conceptArgs.join(" ")}`);
  } finally {
    rmSync(dir, { recursive: true, force: true });
    rmSync(missingGraph, { recursive: true, force: true });
  }
});

test("Go ask attaches cached brain rules only to source retrieval", () => {
  const dir = askFixture();
  const binary = builtGoCLI();
  try {
    mkdirSync(join(dir, ".graft"), { recursive: true });
    mkdirSync(join(dir, "graft", ".cache"), { recursive: true });
    writeFileSync(
      join(dir, ".graft", "config.json"),
      JSON.stringify({ brain: { brainId: "brain-1", token: "token-1" } }),
    );
    writeFileSync(
      join(dir, "graft", ".cache", "brain-rules.json"),
      JSON.stringify({
        brainId: "brain-1",
        fetchedAt: 1,
        rules: [
          {
            ruleId: "rule-1",
            symbol: "src/root.ts#root",
            fingerprint: "",
            rule: "Keep the root boundary",
            sourceUrl: "https://example.test/rule-1",
          },
          {
            ruleId: "rule-2",
            symbol: "src/root.ts#root",
            fingerprint: "old-hash",
            rule: "Validate the changed root",
          },
          {
            ruleId: "rule-duplicate",
            symbol: "src/root.ts#root",
            fingerprint: "",
            rule: "Keep the root boundary",
            sourceUrl: "https://example.test/duplicate",
          },
          {
            ruleId: "rule-missing",
            symbol: "src/missing.ts#missing",
            fingerprint: "",
            rule: "Do not attach this rule",
          },
        ],
      }),
    );

    for (const args of [
      ["ask", "needle", dir, "--no-graph-rank", "--source", "--json", "--no-refresh"],
      ["ask", "needle", dir, "--no-graph-rank", "--source", "--no-refresh"],
      ["ask", "needle", dir, "--no-graph-rank", "--json", "--no-refresh"],
    ]) {
      const typescript = runTypeScript(args);
      const go = runGo(binary, args);
      assert.equal(go.status, typescript.status, `status mismatch for ${args.join(" ")}`);
      assert.equal(go.stderr, typescript.stderr, `stderr mismatch for ${args.join(" ")}`);
      if (args.includes("--json")) {
        assert.deepEqual(JSON.parse(go.stdout), JSON.parse(typescript.stdout), `JSON mismatch for ${args.join(" ")}`);
      } else {
        assert.equal(go.stdout, typescript.stdout, `human output mismatch for ${args.join(" ")}`);
      }
    }

    writeFileSync(join(dir, "graft", ".cache", "brain-rules.json"), "not-json");
    const malformedArgs = ["ask", "needle", dir, "--no-graph-rank", "--source", "--json", "--no-refresh"];
    const typescriptMalformed = runTypeScript(malformedArgs);
    const goMalformed = runGo(binary, malformedArgs);
    assert.equal(goMalformed.status, typescriptMalformed.status, `status mismatch for ${malformedArgs.join(" ")}`);
    assert.equal(goMalformed.stderr, typescriptMalformed.stderr, `stderr mismatch for ${malformedArgs.join(" ")}`);
    assert.deepEqual(JSON.parse(goMalformed.stdout), JSON.parse(typescriptMalformed.stdout), `JSON mismatch for ${malformedArgs.join(" ")}`);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("Go ask reads the build sidecar and preserves source retrieval", () => {
  const dir = mkdtempSync(join(tmpdir(), "graft-ask-built-cli-"));
  const binary = builtGoCLI();
  try {
    mkdirSync(join(dir, "src"), { recursive: true });
    writeFileSync(
      join(dir, "src", "payments.ts"),
      [
        "export function checkout(amount: number): string {",
        "  const token = createStripeCharge(amount);",
        "  return token;",
        "}",
        "",
      ].join("\n"),
    );
    execFileSync(process.execPath, ["--import", "tsx", "src/cli.ts", "build", dir], { stdio: "pipe" });

    for (const args of [
      ["ask", "stripe", dir, "--no-graph-rank", "--json", "--no-refresh"],
      ["ask", "stripe", dir, "--no-graph-rank", "--source", "--json", "--no-refresh"],
    ]) {
      const typescript = runTypeScript(args);
      const go = runGo(binary, args);
      assert.equal(go.status, typescript.status, `status mismatch for ${args.join(" ")}`);
      assert.equal(go.stderr, typescript.stderr, `stderr mismatch for ${args.join(" ")}`);
      assert.deepEqual(JSON.parse(go.stdout), JSON.parse(typescript.stdout), `JSON mismatch for ${args.join(" ")}`);
    }
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("Go ask federates a workspace parent like TypeScript", () => {
  const dir = workspaceFixture();
  const binary = builtGoCLI();
  try {
    const args = ["ask", "handler", dir, "--json", "--no-refresh"];
    const typescript = runTypeScript(args);
    const go = runGo(binary, args);
    assert.equal(go.status, typescript.status, `status mismatch for ${args.join(" ")}`);
    assert.equal(go.stderr, typescript.stderr, `stderr mismatch for ${args.join(" ")}`);
    assert.deepEqual(JSON.parse(go.stdout), JSON.parse(typescript.stdout), `JSON mismatch for ${args.join(" ")}`);

    const sourceArgs = ["ask", "handler", dir, "--source", "--json", "--no-refresh"];
    const typescriptSource = runTypeScript(sourceArgs);
    const goSource = runGo(binary, sourceArgs);
    assert.equal(goSource.status, typescriptSource.status, `status mismatch for ${sourceArgs.join(" ")}`);
    assert.equal(goSource.stderr, typescriptSource.stderr, `stderr mismatch for ${sourceArgs.join(" ")}`);
    assert.deepEqual(JSON.parse(goSource.stdout), JSON.parse(typescriptSource.stdout), `JSON mismatch for ${sourceArgs.join(" ")}`);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("Go ask preserves workspace child filtering and missing-child coverage", () => {
  const dir = workspaceFixture();
  const binary = builtGoCLI();
  try {
    const scopedArgs = ["ask", "handler", dir, "--in", "repoA", "--json", "--no-refresh"];
    const typescriptScoped = runTypeScript(scopedArgs);
    const goScoped = runGo(binary, scopedArgs);
    assert.equal(goScoped.status, typescriptScoped.status, `status mismatch for ${scopedArgs.join(" ")}`);
    assert.equal(goScoped.stderr, typescriptScoped.stderr, `stderr mismatch for ${scopedArgs.join(" ")}`);
    assert.deepEqual(JSON.parse(goScoped.stdout), JSON.parse(typescriptScoped.stdout), `JSON mismatch for ${scopedArgs.join(" ")}`);

    const unknownArgs = ["ask", "handler", dir, "--in", "nope", "--json", "--no-refresh"];
    const typescriptUnknown = runTypeScript(unknownArgs);
    const goUnknown = runGo(binary, unknownArgs);
    assert.equal(goUnknown.status, typescriptUnknown.status, `status mismatch for ${unknownArgs.join(" ")}`);
    assert.equal(goUnknown.stderr, typescriptUnknown.stderr, `stderr mismatch for ${unknownArgs.join(" ")}`);
    assert.equal(goUnknown.stdout, typescriptUnknown.stdout, `stdout mismatch for ${unknownArgs.join(" ")}`);

    rmSync(join(dir, "repoB", "graft"), { recursive: true, force: true });
    writeFileSync(
      join(dir, "graft", "workspace.json"),
      JSON.stringify({ version: 1, children: ["repoA", "repoB", "repoC"] }) + "\n",
    );
    const missingArgs = ["ask", "handler", dir, "--json", "--no-refresh"];
    const typescriptMissing = runTypeScript(missingArgs);
    const goMissing = runGo(binary, missingArgs);
    assert.equal(goMissing.status, typescriptMissing.status, `status mismatch for ${missingArgs.join(" ")}`);
    assert.equal(goMissing.stderr, typescriptMissing.stderr, `stderr mismatch for ${missingArgs.join(" ")}`);
    assert.deepEqual(JSON.parse(goMissing.stdout), JSON.parse(typescriptMissing.stdout), `JSON mismatch for ${missingArgs.join(" ")}`);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("Go ask preserves file-first projection after workspace fusion", () => {
  const dir = builtWorkspaceRankingFixture();
  const binary = builtGoCLI();
  try {
    const args = ["ask", "quartz", dir, "--no-graph-rank", "--json", "--no-refresh"];
    const typescript = runTypeScript(args);
    const go = runGo(binary, args);
    assert.equal(go.status, typescript.status, `status mismatch for ${args.join(" ")}`);
    assert.equal(go.stderr, typescript.stderr, `stderr mismatch for ${args.join(" ")}`);
    assert.deepEqual(JSON.parse(go.stdout), JSON.parse(typescript.stdout), `JSON mismatch for ${args.join(" ")}`);

  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("Go ask applies the workspace body-only participation gate", () => {
  const dir = builtWorkspaceGateFixture();
  const binary = builtGoCLI();
  try {
    const args = ["ask", "payment gateway refund", dir, "--json", "--no-refresh"];
    const typescript = runTypeScript(args);
    const go = runGo(binary, args);
    assert.equal(go.status, typescript.status, `status mismatch for ${args.join(" ")}`);
    assert.equal(go.stderr, typescript.stderr, `stderr mismatch for ${args.join(" ")}`);
    assert.deepEqual(JSON.parse(go.stdout), JSON.parse(typescript.stdout), `JSON mismatch for ${args.join(" ")}`);

    const scopedArgs = ["ask", "payment gateway refund", dir, "--in", "repoJunk", "--json", "--no-refresh"];
    const typescriptScoped = runTypeScript(scopedArgs);
    const goScoped = runGo(binary, scopedArgs);
    assert.equal(goScoped.status, typescriptScoped.status, `status mismatch for ${scopedArgs.join(" ")}`);
    assert.equal(goScoped.stderr, typescriptScoped.stderr, `stderr mismatch for ${scopedArgs.join(" ")}`);
    assert.deepEqual(JSON.parse(goScoped.stdout), JSON.parse(typescriptScoped.stdout), `JSON mismatch for ${scopedArgs.join(" ")}`);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("Go ask keeps the exact baseline top while complementing sibling evidence", () => {
  const dir = builtFileComplementFixture();
  const binary = builtGoCLI();
  try {
    const args = ["ask", "amber cobalt", dir, "--no-graph-rank", "--json", "--no-refresh"];
    const typescript = runTypeScript(args);
    const go = runGo(binary, args);
    assert.equal(go.status, typescript.status, `status mismatch for ${args.join(" ")}`);
    assert.equal(go.stderr, typescript.stderr, `stderr mismatch for ${args.join(" ")}`);
    assert.deepEqual(JSON.parse(go.stdout), JSON.parse(typescript.stdout), `JSON mismatch for ${args.join(" ")}`);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("Go ask preserves workspace file-union admission and projection", () => {
  const dir = builtWorkspaceFileUnionFixture();
  const binary = builtGoCLI();
  try {
    const args = ["ask", "amber cobalt", dir, "--no-graph-rank", "--json", "--no-refresh"];
    const typescript = runTypeScript(args);
    const go = runGo(binary, args);
    assert.equal(go.status, typescript.status, `status mismatch for ${args.join(" ")}`);
    assert.equal(go.stderr, typescript.stderr, `stderr mismatch for ${args.join(" ")}`);
    assert.deepEqual(JSON.parse(go.stdout), JSON.parse(typescript.stdout), `JSON mismatch for ${args.join(" ")}`);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});
