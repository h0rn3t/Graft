/**
 * TS↔Go differential for `graft blast` without `--export-viz`: the same git
 * fixture is reported by the TypeScript CLI and the native Go CLI, and stdout,
 * stderr and exit status must match for every format, the option grammar, the
 * failure paths, and the `--name` cache and model call.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { execFile, execFileSync } from "node:child_process";
import { createServer, type IncomingMessage } from "node:http";
import { cpSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { promisify } from "node:util";
import { tmpRepo } from "./helpers.js";

const run = promisify(execFile);

interface Result {
  status: number;
  stdout: string;
  stderr: string;
}

const BASE_ENV: NodeJS.ProcessEnv = {
  ...process.env,
  DO_NOT_TRACK: "1",
  GRAFT_API_KEY: undefined,
  OPENROUTER_API_KEY: undefined,
  ORCAROUTER_API_KEY: undefined,
  GRAFT_PROVIDER: undefined,
  GRAFT_BASE_URL: undefined,
  GRAFT_MODEL: undefined,
  GRAFT_DIR: undefined,
};

function write(root: string, files: Record<string, string>): void {
  for (const [rel, text] of Object.entries(files)) {
    mkdirSync(join(root, rel, ".."), { recursive: true });
    writeFileSync(join(root, rel), text);
  }
}

function git(root: string, ...args: string[]): void {
  execFileSync("git", args, { cwd: root, stdio: "pipe" });
}

async function invoke(command: string[], args: string[], env: NodeJS.ProcessEnv): Promise<Result> {
  try {
    const { stdout, stderr } = await run(command[0], [...command.slice(1), ...args], { env, maxBuffer: 64 * 1024 * 1024 });
    return { status: 0, stdout, stderr };
  } catch (err) {
    const e = err as { code?: number; stdout?: string; stderr?: string };
    return { status: typeof e.code === "number" ? e.code : -1, stdout: e.stdout ?? "", stderr: e.stderr ?? "" };
  }
}

const TS = [process.execPath, "--import", "tsx", "src/cli.ts"];

function goBinary(): string {
  const binary = join(tmpRepo("blast-parity-bin"), "graft");
  execFileSync("go", ["build", "-o", binary, "./cmd/graft"], { stdio: "pipe" });
  return binary;
}

/** Fixture history: an initial commit by the local author, then commits by two
 * other people, then a working-tree edit. */
function fixture(): string {
  const root = tmpRepo("blast-parity-ts");
  git(root, "init", "-q", "-b", "main");
  git(root, "config", "user.name", "Ann Author");
  git(root, "config", "user.email", "ann@example.com");
  write(root, {
    "src/core/store.ts": [
      "export class Store {",
      "  get(key: string): string {",
      "    return key;",
      "  }",
      "  put(key: string): void {",
      "    console.log(key);",
      "  }",
      "}",
      "export function helper(): number { return 1; }",
      "export interface Shape { size: number }",
    ].join("\n") + "\n",
    "src/core/old.ts": "export function legacy() { return 1; }\n",
    "src/core/moved.ts": "export function moved() { return 2; }\n",
    "src/ui/app.ts": [
      'import { Store, helper } from "../core/store";',
      'import { legacy } from "../core/old";',
      "export function render(): string {",
      "  const s = new Store();",
      '  return s.get("x") + helper() + legacy();',
      "}",
      "export function other() { return render(); }",
    ].join("\n") + "\n",
    "src/ui/view.ts": 'import { render } from "./app";\nexport function view() { return render() + "<b>&</b>"; }\n',
    "src/deep/a/b/c.ts": 'import { helper } from "../../../core/store";\nexport function c() { return helper(); }\n',
    "src/other/d.ts": 'import { view } from "../ui/view";\nexport function d() { return view(); }\n',
    "lib/e.ts": 'import { d } from "../src/other/d";\nexport function e() { return d(); }\n',
    "tools/f.ts": 'import { e } from "../lib/e";\nexport function f() { return e(); }\n',
    "scripts/g.ts": 'import { f } from "../tools/f";\nexport function g() { return f(); }\n',
    "test/store.test.ts": 'import { Store } from "../src/core/store";\nexport function testGet() { new Store().get("a"); }\n',
    "README.md": "# fixture\n",
  });
  git(root, "add", "-A");
  git(root, "commit", "-qm", "init");
  const bob = ["-c", "user.name=Bob Reviewer", "-c", "user.email=12345+bobby@users.noreply.github.com"];
  write(root, { "src/ui/app.ts": readFileSync(join(root, "src/ui/app.ts"), "utf8") + "// bob\n" });
  git(root, ...bob, "commit", "-qam", "bob edit");
  const cara = ["-c", "user.name=Cara Coder", "-c", "user.email=cara@corp.example"];
  write(root, { "src/other/d.ts": readFileSync(join(root, "src/other/d.ts"), "utf8") + "// cara\n" });
  git(root, ...cara, "commit", "-qam", "cara edit");
  git(root, "branch", "base");
  // The PR range: a rename, a deletion, and edits across several areas.
  git(root, "mv", "src/core/moved.ts", "src/core/renamed.ts");
  git(root, "rm", "-q", "src/core/old.ts");
  write(root, {
    "src/ui/app.ts": readFileSync(join(root, "src/ui/app.ts"), "utf8").replace(' + legacy()', "").replace('import { legacy } from "../core/old";\n', ""),
    "src/core/store.ts": readFileSync(join(root, "src/core/store.ts"), "utf8").replace("return key;", 'return key + "!";'),
    "src/deep/a/b/c.ts": 'import { helper } from "../../../core/store";\nexport function c() { return helper() + 1; }\n',
    "lib/e.ts": 'import { d } from "../src/other/d";\nexport function e() { return d() * 2; }\n',
    "tools/f.ts": 'import { e } from "../lib/e";\nexport function f() { return e() - 1; }\n',
    "scripts/g.ts": 'import { f } from "../tools/f";\nexport function g() { return f() + 0; }\n',
    "test/store.test.ts": 'import { Store } from "../src/core/store";\nexport function testGet() { new Store().get("b"); }\n',
    "README.md": "# fixture v2\n",
  });
  git(root, "add", "-A");
  git(root, ...cara, "commit", "-qm", "pr");
  // Working-tree edit for the no-base case.
  write(root, { "src/core/store.ts": readFileSync(join(root, "src/core/store.ts"), "utf8").replace("console.log(key);", "console.error(key);") });
  return root;
}

/** Report with the given args in both twins; paths are normalised to ROOT. */
async function both(binary: string, ts: string, go: string, args: string[], env: NodeJS.ProcessEnv = BASE_ENV): Promise<{ ts: Result; go: Result }> {
  const norm = (r: Result, root: string): Result => ({
    status: r.status,
    stdout: r.stdout.split(root).join("ROOT"),
    stderr: r.stderr.split(root).join("ROOT"),
  });
  const [tsResult, goResult] = await Promise.all([
    invoke(TS, ["blast", ts, ...args], env),
    invoke([binary], ["blast", go, ...args], env),
  ]);
  return { ts: norm(tsResult, ts), go: norm(goResult, go) };
}

test("Go blast matches TypeScript output, options and failure paths", async () => {
  const binary = goBinary();
  const ts = fixture();
  const go = tmpRepo("blast-parity-go");
  cpSync(ts, go, { recursive: true });

  // Before any build: the missing-graph failure.
  const missing = await both(binary, ts, go, ["--no-refresh"]);
  assert.equal(missing.go.status, 1);
  assert.deepEqual(missing.go, missing.ts, "missing graph");

  execFileSync(process.execPath, [...TS.slice(1), "build", ts], { stdio: "pipe", env: BASE_ENV });
  execFileSync(binary, ["build", go], { stdio: "pipe", env: BASE_ENV });

  const cases: string[][] = [
    [],
    ["--format", "markdown"],
    ["--format", "mermaid"],
    ["--format", "json", "--no-owners"],
    ["--base", "base", "--format", "markdown"],
    ["--base", "base", "--format", "text"],
    ["--base=base", "--format=json", "--no-owners", "--depth", "all"],
    ["--base", "base", "-d", "1", "--format", "markdown", "--pr-author", "bobby", "Cara Coder"],
    ["--base", "base", "--depth", "3.7", "--format", "mermaid"],
    ["--base", "HEAD", "--format", "markdown"],
    ["--base", "HEAD", "--format", "mermaid"],
    ["--title", "PR #1", "--no-owners"],
    ["--format", "yaml"],
    ["--depth", "0"],
    ["--depth", "nope"],
    ["--base", "does-not-exist"],
  ];
  const seen: Result[] = [];
  for (const args of cases) {
    const { ts: t, go: g } = await both(binary, ts, go, ["--no-refresh", ...args]);
    assert.deepEqual(g, t, `blast ${args.join(" ")}`);
    seen.push(g);
  }
  const ranged = JSON.parse(seen[6].stdout) as { depth: null; areas: unknown[]; changed: { status: string }[] };
  assert.equal(ranged.depth, null, "full closure serialises as null");
  assert.equal(ranged.areas.length, 5, "six changed directories fold into five areas");
  assert.deepEqual([...new Set(ranged.changed.map((c) => c.status))].sort(), ["deleted", "modified", "renamed"]);
  assert.match(seen[4].stdout, /^Tag: .*@bobby/m, "owners suggest the other contributor");
  assert.doesNotMatch(seen[7].stdout, /bobby|Cara/, "--pr-author excludes both names");
  for (const failure of seen.slice(12)) assert.equal(failure.status, 1);

  const notGit = tmpRepo("blast-parity-nogit");
  cpSync(join(ts, "graft"), join(notGit, "graft"), { recursive: true });
  const [tsNoGit, goNoGit] = await Promise.all([
    invoke(TS, ["blast", notGit, "--no-refresh"], BASE_ENV),
    invoke([binary], ["blast", notGit, "--no-refresh"], BASE_ENV),
  ]);
  assert.equal(goNoGit.status, 1);
  assert.deepEqual(goNoGit, tsNoGit, "outside a git repository");
});

test("Go blast --name matches TypeScript: no key, cache hits, and one model call", async () => {
  const binary = goBinary();
  const ts = fixture();
  const go = tmpRepo("blast-parity-name-go");
  cpSync(ts, go, { recursive: true });
  execFileSync(process.execPath, [...TS.slice(1), "build", ts], { stdio: "pipe", env: BASE_ENV });
  execFileSync(binary, ["build", go], { stdio: "pipe", env: BASE_ENV });

  const noKey = await both(binary, ts, go, ["--no-refresh", "--name", "--base", "base", "--format", "markdown"]);
  assert.match(noKey.ts.stderr, /--name: no API key/);
  assert.deepEqual(noKey.go, noKey.ts, "--name without a key");

  const bodies: unknown[] = [];
  const server = createServer((req: IncomingMessage, res) => {
    let raw = "";
    req.on("data", (chunk) => (raw += chunk));
    req.on("end", () => {
      const body = JSON.parse(raw) as { messages: { content: string }[] };
      bodies.push({ url: req.url, auth: req.headers.authorization, body });
      const keys = [...body.messages[1].content.matchAll(/^key: (\w+)$/gm)].map((m) => m[1]);
      const names = keys.map((key, i) => ({ key, name: i === 0 ? "mixed" : `Area <${i}> "Named"` }));
      res.setHeader("content-type", "application/json");
      res.end(JSON.stringify({
        id: "x", object: "chat.completion", created: 0, model: "m",
        choices: [{ index: 0, finish_reason: "tool_calls", message: { role: "assistant", content: null, tool_calls: [
          { id: "c1", type: "function", function: { name: "record_names", arguments: JSON.stringify({ names }) } },
        ] } }],
        usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 },
      }));
    });
  });
  await new Promise<void>((done) => server.listen(0, "127.0.0.1", done));
  try {
    const port = (server.address() as { port: number }).port;
    const env = { ...BASE_ENV, GRAFT_API_KEY: "sk-test", GRAFT_BASE_URL: `http://127.0.0.1:${port}/v1`, GRAFT_MODEL: "test-model" };
    const args = ["--no-refresh", "--name", "--base", "base", "--format", "json", "--no-owners"];
    const tsNamed = await invoke(TS, ["blast", ts, ...args], env);
    const goNamed = await invoke([binary], ["blast", go, ...args], env);
    assert.equal(bodies.length, 2, "one model call per CLI");
    assert.deepEqual(
      JSON.parse(goNamed.stdout.split(go).join("ROOT")),
      JSON.parse(tsNamed.stdout.split(ts).join("ROOT")),
      "named report",
    );
    assert.equal(goNamed.stdout.split(go).join("ROOT"), tsNamed.stdout.split(ts).join("ROOT"), "named report bytes");
    assert.equal(goNamed.stderr, tsNamed.stderr, "naming note");
    assert.match(goNamed.stderr, /• --name: [1-9]\d* named, 0 cached, 1 left as symbols \(mixed\)/);
    assert.match(goNamed.stdout, /"label": "Area 1 Named"/, "model names are sanitised");
    const [tsBody, goBody] = bodies as { url: string; auth: string; body: Record<string, unknown> }[];
    assert.equal(goBody.url, tsBody.url);
    assert.equal(goBody.auth, tsBody.auth);
    assert.deepEqual(goBody.body, tsBody.body, "request body");
    assert.equal(
      readFileSync(join(go, "graft", ".cache", "areas.json"), "utf8"),
      readFileSync(join(ts, "graft", ".cache", "areas.json"), "utf8"),
      "areas cache",
    );

    // Second run: everything named comes from the cache; the mixed cluster asks again.
    const again = await both(binary, ts, go, ["--no-refresh", "--name", "--base", "base", "--format", "markdown", "--no-owners"], env);
    assert.deepEqual(again.go, again.ts, "cached names");
    assert.match(again.go.stderr, /0 named, [1-9]\d* cached, 1 left as symbols/);

    // A failing endpoint leaves the backstop labels and says why.
    const failing = { ...env, GRAFT_BASE_URL: "http://127.0.0.1:1/v1", GRAFT_LLM_RETRIES: "0" };
    rmSync(join(ts, "graft", ".cache", "areas.json"));
    rmSync(join(go, "graft", ".cache", "areas.json"));
    const failed = await both(binary, ts, go, ["--no-refresh", "--name", "--base", "base", "--no-owners"], failing);
    assert.match(failed.go.stderr, /naming failed \(Connection error\.\)/);
    assert.deepEqual(failed.go, failed.ts, "failed naming");
  } finally {
    server.close();
  }
});
