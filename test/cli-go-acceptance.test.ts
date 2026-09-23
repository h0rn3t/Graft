/**
 * TS↔Go acceptance matrix for the Go-scoped CLI. Twin fixtures are driven
 * through the TypeScript CLI (the oracle) and the native Go CLI case by case —
 * missing, built, successful, empty, invalid, stale, environment-steered and
 * failing invocations — and every case must agree on exit status, stdout,
 * stderr and the files it leaves behind. The fixtures cover the eight languages
 * both builders index, a monorepo with several ranking scopes, and a workspace
 * of child repositories. SQL and the languages Go deliberately excludes have no
 * TypeScript oracle, so they get explicit expectations of their own.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { execFileSync, spawn, spawnSync } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, readdirSync, rmSync, statSync, writeFileSync } from "node:fs";
import { join, relative, resolve } from "node:path";
import { pathToFileURL } from "node:url";
import { tmpRepo } from "./helpers.js";
import { captureGolden } from "./goldens.js";

// Absolute, so the TypeScript CLI also runs from inside the fixture repos.
const TS = [process.execPath, "--import", pathToFileURL(resolve("node_modules/tsx/dist/loader.mjs")).href, resolve("src/cli.ts")];

/** Large enough that savings lines cross 1,000 tokens and group their digits. */
const BULK = Array.from({ length: 160 }, (_, i) => `export function bulk${i}(value: number): number { return value + ${i}; }`).join("\n") + "\n";

const FIXTURE: Record<string, string> = {
  "package.json": JSON.stringify({ name: "fixture" }),
  "src/store.ts": [
    "import { format } from './format.js';",
    "export class Store {",
    "  private items = new Map<string, string>();",
    "  get(key: string): string { return format(this.items.get(key) ?? key); }",
    "  put(key: string, value: string): void { this.items.set(key, value); }",
    "}",
    "export function openStore(): Store { const store = new Store(); store.put('a', 'b'); return store; }",
  ].join("\n") + "\n",
  "src/format.js": "export function format(text) { return trim(text).toUpperCase(); }\nfunction trim(text) { return text.trim(); }\n",
  // "_" sorts before "." under localeCompare but after it byte-wise.
  "src/store_util.ts": "import { openStore } from './store';\nexport function storeKeys(): string[] { return [openStore().get('k')]; }\n",
  "src/app.tsx": "import { openStore } from './store';\nexport function App() { return <div>{openStore().get('a')}</div>; }\n",
  "src/bulk.ts": BULK,
  "src/ünï_code.ts": "// ünïcödé 😀 — keeps UTF-16 lengths honest\nexport function größe(): string { return '😀'; }\n",
  "src/store.test.ts": "import { openStore } from './store';\nexport function testStore() { return openStore().get('a'); }\n",
  "py/service.py": "from py.util import normalize\n\nclass Service:\n    def handle(self, text):\n        return normalize(self.prefix(text))\n\n    def prefix(self, text):\n        return 'svc:' + text\n",
  "py/util.py": "def normalize(text):\n    return text.strip().lower()\n",
  "go.mod": "module example.com/fixture\n\ngo 1.22\n",
  "cmd/server/main.go": "package main\n\nimport \"example.com/fixture/pkg/queue\"\n\nfunc main() { queue.New().Push(1) }\n",
  "pkg/queue/queue.go": "package queue\n\ntype Queue struct{ items []int }\n\nfunc New() *Queue { return &Queue{} }\n\nfunc (q *Queue) Push(v int) { q.items = append(q.items, v); q.grow() }\n\nfunc (q *Queue) grow() {}\n",
  "Cargo.toml": "[package]\nname = \"fixture\"\nversion = \"0.1.0\"\n",
  "rust/lib.rs": "mod parse;\npub fn run() -> i32 { parse::parse_line() }\n",
  "rust/parse.rs": "pub fn parse_line() -> i32 { 1 }\n",
  "java/Worker.java": "class Worker { int run() { return compute(); } int compute() { return 1; } }\n",
  "native/codec.h": "int encode(int value);\n",
  "native/codec.c": "#include \"codec.h\"\nint encode(int value) { return value + 1; }\nint encode_twice(int value) { return encode(encode(value)); }\n",
  "native/widget.hpp": "class Widget { public: int draw(); };\n",
  "native/widget.cpp": "#include \"widget.hpp\"\nint Widget::draw() { return 1; }\nint render() { Widget w; return w.draw(); }\n",
  "README.md": "# fixture\n",
};

/** Tracked files; the rest stay untracked, so the walk sees both kinds. */
const COMMITTED = ["package.json", "src/store.ts", "src/format.js", "py", "go.mod", "cmd", "java", "native/codec.h", "native/codec.c", "README.md"];

/** An agent session that has been billed, so savings lines carry dollars. */
const SESSION = JSON.stringify({
  lastQuery: "store get",
  perAgentQuery: {},
  graftReads: 3,
  sourceReads: 1,
  savedTokens: 12345,
  injectedPointers: [],
  nudges: 0,
  inputCostMicros: 3000000,
  inputTokensBilled: 1000000,
});

interface Twin {
  name: string;
  command: string[];
  base: string;
  repo: string;
  home: string;
  elsewhere: string;
  tracked: string[];
  gitDirs: string[];
  fixture: Record<string, string>;
  fixture: Record<string, string>;
}

interface Outcome {
  status: number | null;
  stdout: string;
  stderr: string;
}

interface Case {
  label: string;
  /** "REPO" (or a "REPO/…" prefix) stands for the twin's repository in args and env values. */
  args: string[];
  env?: Record<string, string | undefined>;
  /** Directory to run in (the twin's repo by default). */
  cwd?: (twin: Twin) => string;
  /** Mutation applied to each twin before the case runs. */
  before?: (twin: Twin) => void;
  /** Compare the twins' files after the case. */
  files?: boolean;
}

function write(root: string, files: Record<string, string>): void {
  for (const [rel, text] of Object.entries(files)) {
    mkdirSync(join(root, rel, ".."), { recursive: true });
    writeFileSync(join(root, rel), text);
  }
}

function git(cwd: string, args: string[]): void {
  execFileSync("git", ["-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false", ...args], { cwd, stdio: "pipe" });
}

function environment(twin: Twin, extra: Record<string, string | undefined>): NodeJS.ProcessEnv {
  const env: NodeJS.ProcessEnv = { ...process.env, HOME: twin.home, USERPROFILE: twin.home, DO_NOT_TRACK: "1", COLUMNS: "80" };
  for (const name of ["GRAFT_DIR", "GRAFT_NO_REFRESH", "GRAFT_BRAIN_TOKEN", "GRAFT_BRAIN_ID", "CLAUDECODE", "CI", "GITHUB_ACTIONS"]) delete env[name];
  for (const [name, value] of Object.entries(extra)) {
    if (value === undefined) delete env[name];
    else env[name] = value;
  }
  return env;
}

function normalize(text: string, twin: Twin): string {
  return text
    .split(twin.elsewhere).join("<ELSEWHERE>")
    .split(twin.repo).join("<REPO>")
    .split(twin.home).join("<HOME>")
    .split(twin.base).join("<BASE>")
    .replace(/\b\d+(\.\d+)?\s?(ms|s)\b/g, "<DURATION>")
    .replace(/\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?Z/g, "<TIME>")
    .replace(/"checkedAt":\s*\d+/g, '"checkedAt":<TIME>');
}

function invoke(twin: Twin, args: string[], extra: Record<string, string | undefined> = {}, cwd = twin.repo): Outcome {
  const res = spawnSync(twin.command[0], [...twin.command.slice(1), ...args], { cwd, env: environment(twin, extra), encoding: "utf8" });
  return { status: res.status, stdout: normalize(res.stdout, twin), stderr: normalize(res.stderr, twin) };
}

/** The extraction and fingerprint caches are private to each implementation:
 * they are keyed by an extractor id and hold mtimes, and neither CLI reads the
 * other's. Everything else a CLI writes must match byte for byte. */
const PRIVATE_CACHE = /(^|\/)\.cache\/(extract|fingerprint)\.[^/]+\.json$/;

/** Every file under root that a CLI wrote, except the private caches. */
function snapshot(root: string, twin: Twin): Record<string, string> {
  const out: Record<string, string> = {};
  const walk = (dir: string) => {
    for (const name of readdirSync(dir).sort()) {
      const path = join(dir, name);
      if (name === ".git" || PRIVATE_CACHE.test(relative(root, path))) continue;
      if (statSync(path).isDirectory()) walk(path);
      else out[relative(root, path)] = normalize(readFileSync(path, "utf8"), twin);
    }
  };
  if (existsSync(root)) walk(root);
  return out;
}

let goBinary: string | undefined;

function binary(): string {
  if (!goBinary) {
    goBinary = join(tmpRepo("cli-acceptance-bin"), "graft");
    execFileSync("go", ["build", "-o", goBinary, "./cmd/graft"], { stdio: "pipe" });
  }
  return goBinary;
}

/** Twin copies of a fixture. `setup` runs in each twin's base after the files
 * are written (the repo is `repo/` under it) and may init git or add repos. */
function makeTwins(label: string, files: Record<string, string>, setup: (twin: Twin) => void): { ts: Twin; go: Twin } {
  const base = tmpRepo(label);
  const checkedAt = Date.now();
  const make = (name: string, command: string[]): Twin => {
    const twin: Twin = { name, command, base: join(base, name), repo: join(base, name, "repo"), home: join(base, name, "home"), elsewhere: join(base, name, "elsewhere"), tracked: [], gitDirs: [], fixture: { ...files } };
    write(twin.repo, files);
    mkdirSync(join(twin.home, ".graft"), { recursive: true });
    mkdirSync(twin.elsewhere, { recursive: true });
    // A fresh registry answer, so neither CLI spawns a background update check.
    writeFileSync(join(twin.home, ".graft", "update-check.json"), JSON.stringify({ latest: "0.0.0", checkedAt }, null, 2));
    setup(twin);
    const findGitDirs = (dir: string) => {
      if (existsSync(join(dir, ".git"))) {
        twin.gitDirs.push(relative(twin.repo, dir) || ".");
        return;
      }
      for (const name of readdirSync(dir).sort()) {
        const path = join(dir, name);
        if (statSync(path).isDirectory()) findGitDirs(path);
      }
    };
    findGitDirs(twin.repo);
    if (twin.gitDirs.includes(".")) {
      twin.tracked = execFileSync("git", ["ls-files", "-z"], { cwd: twin.repo, encoding: "utf8" }).split("\0").filter(Boolean);
    }
    return twin;
  };
  return { ts: make("ts", TS), go: make("go", [binary()]) };
}

function bind(twin: Twin, value: string): string {
  return value === "REPO" || value.startsWith("REPO/") ? twin.repo + value.slice("REPO".length) : value;
}

function runMatrix(ts: Twin, go: Twin, cases: Case[], suite: string): string[] {
  const failures: string[] = [];
  const capturing = process.env.GRAFT_CAPTURE_GOLDENS === "1";
  for (const [index, entry] of cases.entries()) {
    const before = capturing ? snapshot(ts.base, ts) : {};
    entry.before?.(ts);
    const afterBefore = capturing ? snapshot(ts.base, ts) : {};
    entry.before?.(go);
    const args = entry.args.map((arg) => bind(ts, arg));
    const env = entry.env && Object.fromEntries(Object.entries(entry.env).map(([name, value]) => [name, value === undefined ? undefined : bind(ts, value)]));
    const cwd = entry.cwd?.(ts) ?? ts.repo;
    const tsOutcome = invoke(ts, args, env, cwd);
    const files = capturing && entry.files ? snapshot(ts.base, ts) : {};
    const goEnv = entry.env && Object.fromEntries(Object.entries(entry.env).map(([name, value]) => [name, value === undefined ? undefined : bind(go, value)]));
    const goOutcome = invoke(go, entry.args.map((arg) => bind(go, arg)), goEnv, entry.cwd?.(go));
    const outcomes = [tsOutcome, goOutcome];
    if (process.env.GRAFT_ACCEPTANCE_TRACE) console.error(JSON.stringify({ case: entry.label, ts: outcomes[0], go: outcomes[1] }));
    if (capturing) {
      const vars = { [ts.base]: "<BASE>", [ts.repo]: "<REPO>", [ts.home]: "<HOME>", [ts.elsewhere]: "<ELSEWHERE>" };
      const slug = entry.label.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "");
      const writes = Object.fromEntries(Object.entries(afterBefore).filter(([path, value]) => before[path] !== value));
      const deletes = Object.keys(before).filter((path) => afterBefore[path] === undefined);
      captureGolden(`cli-go-acceptance/${suite}/${String(index + 1).padStart(3, "0")}-${slug}`, {
        args,
        cwd,
        env: Object.fromEntries(Object.entries(entry.env ?? {}).filter((item): item is [string, string] => item[1] !== undefined).map(([name, value]) => [name, bind(ts, value)])),
        ...(index === 0 && { tracked: ts.tracked, gitDirs: ts.gitDirs, inputs: ts.fixture }),
        ...(Object.keys(writes).length > 0 || deletes.length > 0 ? { mutations: { writes, deletes } } : {}),
        status: tsOutcome.status ?? -1,
        stdout: tsOutcome.stdout,
        stderr: tsOutcome.stderr,
        files,
        checkFiles: entry.files ?? false,
        normalize: vars,
      });
    }
    try {
      assert.deepEqual(outcomes[1], outcomes[0]);
    } catch (err) {
      failures.push(`${entry.label} (graft ${entry.args.join(" ")}):\n${(err as Error).message}`);
    }
    if (entry.files) {
      try {
        assert.deepEqual(snapshot(go.base, go), snapshot(ts.base, ts));
      } catch (err) {
        failures.push(`${entry.label} files:\n${(err as Error).message}`);
      }
    }
  }
  return failures;
}

const NO_REFRESH = { GRAFT_NO_REFRESH: "1" };

test("Go CLI matches TypeScript across the query and build matrix", () => {
  const { ts, go } = makeTwins("cli-acceptance", FIXTURE, (twin) => {
    git(twin.repo, ["init", "-q"]);
    git(twin.repo, ["add", ...COMMITTED]);
    git(twin.repo, ["commit", "-qm", "init"]);
  });
  const cases: Case[] = [
    // No graph yet.
    { label: "ask without a graph, refresh off", args: ["ask", "store", "--no-refresh"] },
    { label: "ask --json without a graph", args: ["ask", "store", "--json"], env: NO_REFRESH },
    { label: "grep without a graph, env refresh off", args: ["grep", "Store"], env: NO_REFRESH },
    { label: "callers without a graph", args: ["callers", "encode", "--no-refresh"] },
    { label: "skeleton without a graph", args: ["skeleton", "src/store.ts", "--no-refresh"] },
    { label: "map without a graph", args: ["map", "--no-refresh"] },
    { label: "stats without a session", args: ["stats"] },
    { label: "stats --json without a session", args: ["stats", "--json"] },
    { label: "check without a graph", args: ["check"] },
    { label: "check --json without a graph", args: ["check", "--json"] },
    // Build: tracked and untracked sources in all eight languages.
    { label: "cold build", args: ["build"], files: true },
    { label: "incremental build", args: ["build", "."], files: true },
    { label: "rebuild without the extraction cache", args: ["build", "--no-reuse"], files: true },
    { label: "check after build", args: ["check"] },
    { label: "check --json after build", args: ["check", "--json"] },
    // Successful queries.
    { label: "ask", args: ["ask", "store get"] },
    { label: "ask --source", args: ["ask", "queue push", "--source"] },
    { label: "ask --full --limit", args: ["ask", "encode value", "--source", "--full", "-n", "2"] },
    { label: "ask --json", args: ["ask", "normalize text", "--json"] },
    { label: "ask --json --source", args: ["ask", "bulk value", "--json", "--source"] },
    { label: "ask --no-graph-rank", args: ["ask", "worker compute", "--no-graph-rank"] },
    { label: "ask --in", args: ["ask", "format", "--in", "src/"] },
    { label: "ask about tests", args: ["ask", "store tests"] },
    { label: "ask unicode", args: ["ask", "größe"] },
    { label: "ask structural callers", args: ["ask", "who calls encode"] },
    { label: "ask structural callees", args: ["ask", "what does openStore call", "--json"] },
    { label: "ask structural, capitalised", args: ["ask", "Who calls encode"] },
    { label: "ask structural miss", args: ["ask", "callers of zzqqxx"] },
    { label: "grep", args: ["grep", "encode"] },
    { label: "grep -i --json", args: ["grep", "WIDGET", "-i", "--json"] },
    { label: "grep --fixed", args: ["grep", "q.items", "--fixed"] },
    { label: "grep regex", args: ["grep", "parse_\\w+"] },
    { label: "grep --in", args: ["grep", "text", "--in", "py/"] },
    { label: "grep many hits", args: ["grep", "value"] },
    { label: "callers", args: ["callers", "encode"] },
    { label: "callers --direction out", args: ["callers", "openStore", "--direction", "out"] },
    { label: "callers --depth 2", args: ["callers", "format", "--depth", "2"] },
    { label: "callers --depth all --json", args: ["callers", "trim", "-d", "all", "--json"] },
    { label: "callers --depth hex", args: ["callers", "trim", "-d", "0x2"] },
    { label: "callers --depth fraction", args: ["callers", "trim", "-d", "2.7"] },
    { label: "callers qualified", args: ["callers", "Queue.grow"] },
    { label: "callers --in", args: ["callers", "encode", "--in", "native"] },
    { label: "skeleton", args: ["skeleton", "src/store.ts"] },
    { label: "skeleton --json", args: ["skeleton", "native/widget.cpp", "--json"] },
    { label: "skeleton go", args: ["skeleton", "pkg/queue/queue.go"] },
    { label: "skeleton by basename", args: ["skeleton", "bulk.ts"] },
    { label: "map", args: ["map"] },
    { label: "map --json --max-dirs", args: ["map", "--json", "--max-dirs", "3"] },
    { label: "explicit dir", args: ["ask", "run", ".", "--source"] },
    { label: "dir from elsewhere", args: ["grep", "Service"], cwd: (twin) => twin.elsewhere },
    { label: "dir argument from elsewhere", args: ["map", "REPO"], cwd: (twin) => twin.elsewhere },
    { label: "nearest graft root from a subdirectory", args: ["callers", "normalize"], cwd: (twin) => join(twin.repo, "py") },
    { label: "ask from a subdirectory", args: ["ask", "normalize", "--source"], cwd: (twin) => join(twin.repo, "py") },
    { label: "grep from a subdirectory", args: ["grep", "normalize"], cwd: (twin) => join(twin.repo, "py") },
    { label: "check from a subdirectory", args: ["check"], cwd: (twin) => join(twin.repo, "py") },
    // A billed session prices every savings line.
    {
      label: "ask --source with a billed session",
      args: ["ask", "bulk value", "--source"],
      before: (twin) => write(twin.repo, { "graft/.cache/session/s1.json": SESSION }),
    },
    { label: "grep with a billed session", args: ["grep", "bulk"] },
    { label: "map with a billed session", args: ["map"] },
    { label: "skeleton with a billed session", args: ["skeleton", "src/bulk.ts"] },
    { label: "stats with a session", args: ["stats"] },
    { label: "stats --json with a session", args: ["stats", "--json"] },
    // Limits read with Number(), as the TypeScript CLI reads them.
    ...["0", "1.5", "-2", "zero", "Infinity", " 3 "].map((limit): Case => ({ label: `ask -n ${JSON.stringify(limit)}`, args: ["ask", "store", "-n", limit, "--json"] })),
    // Empty answers.
    { label: "ask with no hits", args: ["ask", "zzqqxxnothing"] },
    { label: "grep with no hits", args: ["grep", "zzqqxxnothing"] },
    { label: "callers of an unknown symbol", args: ["callers", "zzqqxxnothing"] },
    { label: "skeleton of a missing file", args: ["skeleton", "src/missing.ts"] },
    { label: "skeleton of an unindexed file", args: ["skeleton", "README.md"] },
    // Invalid invocations.
    { label: "ask without a query", args: ["ask"] },
    { label: "ask --in outside the graph", args: ["ask", "store", "--in", "nowhere"] },
    { label: "grep --in outside the graph", args: ["grep", "store", "--in", "nowhere/"] },
    { label: "callers --in outside the graph", args: ["callers", "encode", "--in", "nowhere"] },
    { label: "callers bad direction", args: ["callers", "encode", "--direction", "sideways"] },
    { label: "callers bad depth", args: ["callers", "encode", "--depth", "many"] },
    { label: "callers zero depth", args: ["callers", "encode", "--depth", "0"] },
    { label: "map bad max-dirs", args: ["map", "--max-dirs", "-1"] },
    { label: "grep bad regex", args: ["grep", "("] },
    { label: "unknown option", args: ["ask", "store", "--sauce"] },
    { label: "unknown command", args: ["serach", "store"] },
    { label: "too many arguments", args: ["map", ".", "extra"] },
    { label: "missing option argument", args: ["ask", "store", "--in"] },
    // Stale graph.
    {
      label: "check a stale graph",
      args: ["check"],
      before: (twin) => write(twin.repo, { "src/format.js": "export function format(text) { return trim(text); }\nfunction trim(text) { return text.trim(); }\nexport function pad(text) { return ' ' + text; }\n" }),
    },
    { label: "check --json a stale graph", args: ["check", "--json"] },
    { label: "ask a stale graph, refresh off", args: ["ask", "pad text"], env: NO_REFRESH },
    { label: "ask a stale graph, refreshing", args: ["ask", "pad text"], files: true },
    { label: "check after refresh", args: ["check"] },
    {
      label: "grep after a deletion",
      args: ["grep", "parse_line"],
      before: (twin) => rmSync(join(twin.repo, "rust", "parse.rs")),
      files: true,
    },
    {
      label: "callers after a tracked file changes",
      args: ["callers", "encode"],
      before: (twin) => write(twin.repo, { "native/codec.c": "#include \"codec.h\"\nint encode(int value) { return value + 2; }\nint encode_thrice(int value) { return encode(encode(encode(value))); }\n" }),
      files: true,
    },
    // Environment: GRAFT_DIR steers build, check, ask and state; the other queries read --dir only.
    { label: "GRAFT_DIR at the repo itself", args: ["map"], env: { GRAFT_DIR: "REPO" }, cwd: (twin) => twin.elsewhere },
    ...([
      ["ask", "store"],
      ["ask", "store", "--json", "--source"],
      ["grep", "store"],
      ["callers", "encode"],
      ["skeleton", "src/store.ts"],
      ["map"],
      ["check"],
      ["stats"],
    ] as string[][]).map((args): Case => ({ label: `GRAFT_DIR from elsewhere: ${args[0]}`, args, env: { GRAFT_DIR: "REPO/graft" }, cwd: (twin) => twin.elsewhere })),
    { label: "GRAFT_DIR build into another directory", args: ["build"], env: { GRAFT_DIR: "REPO/alt" }, files: true },
    ...([["ask", "store", "--json"], ["check", "--json"], ["grep", "store"], ["callers", "encode"], ["stats", "--json"]] as string[][]).map(
      (args): Case => ({ label: `relative GRAFT_DIR: ${args[0]}`, args, env: { GRAFT_DIR: "alt" } }),
    ),
    { label: "--dir into another directory", args: ["--dir", "alt", "ask", "store"] },
    { label: "--dir", args: ["--dir", "REPO", "callers", "encode"], cwd: (twin) => twin.elsewhere },
    { label: "--dir after the command", args: ["skeleton", "native/codec.c", "--dir", "REPO"], cwd: (twin) => twin.elsewhere },
    // Failures.
    { label: "build from a subdirectory builds there", args: ["build"], cwd: (twin) => join(twin.repo, "rust"), files: true },
    { label: "build a missing directory", args: ["build", "does/not/exist"], files: true },
    { label: "build a file", args: ["build", "README.md"], files: true },
    { label: "check a missing directory", args: ["check", "does/not/exist"] },
    {
      label: "ask over a corrupt graph",
      args: ["ask", "store", "--no-refresh"],
      before: (twin) => writeFileSync(join(twin.repo, "graft", ".graph", "wiring.json"), "{ not json"),
    },
    { label: "grep over a corrupt graph", args: ["grep", "store", "--no-refresh"] },
    { label: "check over a corrupt graph", args: ["check"] },
    { label: "rebuild over a corrupt graph", args: ["build"], files: true },
  ];
  const failures = runMatrix(ts, go, cases, "main");
  assert.deepEqual(failures, [], failures.join("\n\n"));
});

const MONOREPO: Record<string, string> = {
  "package.json": JSON.stringify({ name: "root", workspaces: ["packages/*"] }),
  "packages/core/package.json": JSON.stringify({ name: "core" }),
  "packages/ui/package.json": JSON.stringify({ name: "ui" }),
  "packages/docs/package.json": JSON.stringify({ name: "docs" }),
  "packages/core/src/store.ts": Array.from({ length: 7 }, (_, i) => `export function storeGet${i}(key: string) { return key + '${i}'; }`).join("\n") + "\n",
  "packages/ui/src/view.ts": Array.from({ length: 7 }, (_, i) => `import { storeGet${i} } from '../../core/src/store';\nexport function renderStore${i}() { return storeGet${i}('x'); }`).join("\n") + "\n",
  "packages/docs/src/notes.ts": Array.from({ length: 6 }, (_, i) => `// the store keeps values; the store is the source of truth\nexport function note${i}() { return ${i}; }`).join("\n") + "\n",
  "tools/build.ts": "export function buildStore() { return 1; }\n",
};

test("Go CLI matches TypeScript on a monorepo with several ranking scopes", () => {
  const { ts, go } = makeTwins("cli-acceptance-mono", MONOREPO, (twin) => git(twin.repo, ["init", "-q"]));
  const cases: Case[] = [
    { label: "build", args: ["build"], files: true },
    { label: "ask across scopes", args: ["ask", "store get"] },
    { label: "ask across scopes --json", args: ["ask", "store", "--json"] },
    { label: "ask --source across scopes", args: ["ask", "render store", "--source", "--json"] },
    { label: "ask one scope", args: ["ask", "render", "--in", "packages/ui"] },
    { label: "ask a body-only scope", args: ["ask", "source of truth"] },
    { label: "ask with no hits names the scopes", args: ["ask", "zzqqxx"] },
    { label: "ask --in outside the graph names the scopes", args: ["ask", "store", "--in", "packages/nope"] },
    { label: "ask structural", args: ["ask", "who calls storeGet3"] },
    { label: "grep", args: ["grep", "storeGet3"] },
    { label: "grep --in scope", args: ["grep", "store", "--in", "packages/core/", "--json"] },
    { label: "callers", args: ["callers", "storeGet3", "--depth", "all"] },
    { label: "callers --json", args: ["callers", "storeGet3", "--json"] },
    { label: "map", args: ["map"] },
    { label: "map --json", args: ["map", "--json"] },
    { label: "skeleton", args: ["skeleton", "packages/ui/src/view.ts"] },
    { label: "check", args: ["check", "--json"] },
  ];
  const failures = runMatrix(ts, go, cases, "monorepo");
  assert.deepEqual(failures, [], failures.join("\n\n"));
});

test("Go CLI matches TypeScript at a workspace of child repositories", () => {
  const children: Record<string, string> = {
    "api/src/server.ts": "import { query } from './db';\nexport function serve() { return query('users'); }\n",
    "api/src/db.ts": "export function query(table: string) { return table; }\n",
    "web/src/page.tsx": "export function Page() { return <main>{title()}</main>; }\nexport function title() { return 'users'; }\n",
    "web/src/users.ts": "export function listUsers() { return ['a']; }\n",
    "tools/notes.md": "# not a repo\n",
    // A child that is itself a monorepo fuses under <child>/<scope>.
    ...Object.fromEntries(Object.entries(MONOREPO).map(([rel, text]) => [`mono/${rel}`, text])),
  };
  const { ts, go } = makeTwins("cli-acceptance-workspace", children, (twin) => {
    for (const child of ["api", "web", "mono"]) git(join(twin.repo, child), ["init", "-q"]);
  });
  const cases: Case[] = [
    { label: "ask before any child graph", args: ["ask", "users"], env: NO_REFRESH },
    { label: "build the workspace", args: ["build"], files: true },
    { label: "ask across children", args: ["ask", "users"] },
    { label: "ask across children --json --source", args: ["ask", "query users", "--json", "--source"] },
    { label: "ask one child", args: ["ask", "users", "--in", "web/"] },
    { label: "ask across scoped children", args: ["ask", "store render", "--json", "--source"] },
    { label: "ask into a child's scope", args: ["ask", "store", "--in", "mono/packages/core"] },
    { label: "ask a missing child", args: ["ask", "users", "--in", "mobile"] },
    { label: "grep across children", args: ["grep", "users"] },
    { label: "grep across children --json", args: ["grep", "query", "--json"] },
    { label: "callers across children", args: ["callers", "query"] },
    { label: "map at the workspace", args: ["map"] },
    { label: "skeleton at the workspace", args: ["skeleton", "api/src/db.ts"] },
    { label: "check the workspace", args: ["check"] },
    { label: "stats at the workspace", args: ["stats"] },
    {
      label: "ask after a child edit",
      args: ["ask", "orders"],
      before: (twin) => write(twin.repo, { "api/src/orders.ts": "export function listOrders() { return query('orders'); }\nimport { query } from './db';\n" }),
      files: true,
    },
  ];
  const failures = runMatrix(ts, go, cases, "workspace");
  assert.deepEqual(failures, [], failures.join("\n\n"));
});

interface GraphFile {
  nodes: { id: string; kind: string; name: string; path: string; span: string; signature?: string | null }[];
  edges: { source: string; relation: string; target: string }[];
}

function readGraph(repo: string): GraphFile {
  return JSON.parse(readFileSync(join(repo, "graft", ".graph", "wiring.json"), "utf8")) as GraphFile;
}

function runGo(repo: string, args: string[], cwd = repo): Outcome {
  const res = spawnSync(binary(), args, { cwd, env: { ...process.env, DO_NOT_TRACK: "1", GRAFT_NO_REFRESH: "1" }, encoding: "utf8" });
  return { status: res.status, stdout: res.stdout, stderr: res.stderr };
}

test("Go CLI indexes PostgreSQL SQL end to end", () => {
  const repo = tmpRepo("cli-acceptance-sql");
  write(repo, {
    "db/schema.sql": [
      "CREATE TYPE billing.status AS ENUM ('open', 'paid');",
      "CREATE TABLE billing.customers (id serial PRIMARY KEY, name text NOT NULL);",
      "CREATE TABLE billing.invoices (",
      "  id serial PRIMARY KEY,",
      "  customer_id int REFERENCES billing.customers(id),",
      "  state billing.status",
      ");",
      "CREATE VIEW billing.open_invoices AS",
      "  SELECT i.id, c.name FROM billing.invoices i JOIN billing.customers c ON c.id = i.customer_id;",
      "CREATE FUNCTION billing.total_due(cid int) RETURNS numeric AS $$",
      "  SELECT count(*) FROM billing.open_invoices WHERE id = cid;",
      "$$ LANGUAGE sql;",
      "CREATE PROCEDURE billing.close_all() LANGUAGE sql AS $$ UPDATE billing.invoices SET state = 'paid'; $$;",
    ].join("\n") + "\n",
    "db/seed.sql": "INSERT INTO billing.customers (name) VALUES ('a');\nSELECT 1;\n",
    "src/app.ts": "export function main() { return 1; }\n",
  });
  execFileSync("git", ["init", "-q"], { cwd: repo });

  const cold = runGo(repo, ["build"]);
  assert.equal(cold.status, 0, cold.stderr);
  const graph = readGraph(repo);
  const byId = new Map(graph.nodes.map((node) => [node.id, node]));
  for (const [id, kind] of [
    ["db/schema.sql#billing.status", "type"],
    ["db/schema.sql#billing.customers", "type"],
    ["db/schema.sql#billing.invoices", "type"],
    ["db/schema.sql#billing.open_invoices", "type"],
    ["db/schema.sql#billing.total_due", "function"],
    ["db/schema.sql#billing.close_all", "function"],
    ["db/seed.sql", "file"],
  ]) {
    assert.equal(byId.get(id)?.kind, kind, `${id} is a ${kind} node`);
  }
  assert.equal(graph.nodes.filter((node) => node.path === "db/seed.sql").length, 1, "anonymous statements keep only the whole-file node");
  const edge = (source: string, target: string) => graph.edges.some((e) => e.source === source && e.target === target);
  assert.ok(edge("db/schema.sql#billing.invoices", "db/schema.sql#billing.customers"), "REFERENCES resolves");
  assert.ok(edge("db/schema.sql#billing.open_invoices", "db/schema.sql#billing.invoices"), "FROM resolves");
  assert.ok(edge("db/schema.sql#billing.open_invoices", "db/schema.sql#billing.customers"), "JOIN resolves");

  const before = readFileSync(join(repo, "graft", ".graph", "wiring.json"), "utf8");
  assert.equal(runGo(repo, ["build"]).status, 0);
  assert.equal(readFileSync(join(repo, "graft", ".graph", "wiring.json"), "utf8"), before, "incremental SQL build equals the cold one");
  assert.equal(runGo(repo, ["build", "--no-reuse"]).status, 0);
  assert.equal(readFileSync(join(repo, "graft", ".graph", "wiring.json"), "utf8"), before, "uncached SQL build equals the cold one");

  const callers = runGo(repo, ["callers", "billing.customers"]);
  assert.equal(callers.status, 0, callers.stderr);
  assert.match(callers.stdout, /billing\.invoices/);
  assert.match(callers.stdout, /billing\.open_invoices/);
  const skeleton = runGo(repo, ["skeleton", "db/schema.sql"]);
  assert.match(skeleton.stdout, /type billing\.customers/);
  assert.match(skeleton.stdout, /function billing\.total_due/);
  const ask = JSON.parse(runGo(repo, ["ask", "open invoices", "--json"]).stdout) as { hits: { pointer: string }[] };
  assert.ok(ask.hits.some((hit) => hit.pointer.startsWith("db/schema.sql:")), "ask finds SQL definitions");
  assert.match(runGo(repo, ["grep", "REFERENCES"]).stdout, /billing\.invoices/);
  const check = runGo(repo, ["check"]);
  assert.equal(check.status, 0, check.stdout + check.stderr);

  // A SQL parse failure never replaces the usable graph.
  write(repo, { "db/schema.sql": "CREATE TABLE billing.broken (id int REFERENCES;\n" });
  const broken = runGo(repo, ["build"]);
  const after = readFileSync(join(repo, "graft", ".graph", "wiring.json"), "utf8");
  if (broken.status !== 0) assert.equal(after, before, "a failed build keeps the previous graph");
  else assert.ok(!after.includes("billing.customers"), "a successful rebuild reflects the edit");
});

test("Go CLI excludes the languages outside its source set", () => {
  const repo = tmpRepo("cli-acceptance-excluded");
  write(repo, {
    "src/main.ts": "export function main() { return helper(); }\nfunction helper() { return 1; }\n",
    "lib/widget.rb": "class Widget\n  def render\n    1\n  end\nend\n",
    "app/Main.kt": "fun main() { println(\"hi\") }\n",
    "web/index.php": "<?php function page() { return 1; }\n",
    "Program.cs": "class Program { static void Main() {} }\n",
  });
  execFileSync("git", ["init", "-q"], { cwd: repo });

  // An older TypeScript graph that indexed every language is replaced.
  execFileSync(TS[0], [...TS.slice(1), "build", repo], { stdio: "pipe", env: { ...process.env, DO_NOT_TRACK: "1", GRAFT_NO_REFRESH: "1" } });
  assert.ok(readGraph(repo).nodes.some((node) => node.path === "lib/widget.rb"), "the TypeScript graph carries Ruby");

  const built = runGo(repo, ["build"]);
  assert.equal(built.status, 0, built.stderr);
  const graph = readGraph(repo);
  const paths = new Set(graph.nodes.map((node) => node.path));
  assert.deepEqual([...paths].sort(), ["src/main.ts"], "only the selected source set is indexed");
  assert.ok(!graph.edges.some((edge) => /\.(rb|kt|php|cs)(#|$)/.test(edge.source + edge.target)), "no edge touches an excluded file");
  assert.equal(runGo(repo, ["check"]).status, 0, "the new graph is fresh");
  assert.doesNotMatch(runGo(repo, ["grep", "render"]).stdout, /widget\.rb/, "grep searches only indexed files");
  assert.match(runGo(repo, ["skeleton", "lib/widget.rb"]).stdout, /no (definitions|file)|not in the graph|no match/i);

  // Asking for an excluded language explicitly is refused without touching the graph.
  const before = readFileSync(join(repo, "graft", ".graph", "wiring.json"), "utf8");
  const explicit = runGo(repo, ["build", "-e", ".rb"]);
  assert.notEqual(explicit.status, 0, "an explicit excluded extension fails the build");
  assert.match(explicit.stderr, /unsupported|widget\.rb/);
  assert.equal(readFileSync(join(repo, "graft", ".graph", "wiring.json"), "utf8"), before, "the graph is unchanged");
});

/** A seeded stream of queries over real code: this repository's own ranking,
 * graph and search sources, in both languages. One TypeScript-built graph
 * serves both CLIs, so every difference is a query difference — ranking order,
 * float bits, tie-breaks, formatting — across hundreds of symbols. */
test("Go queries match TypeScript on real code across a seeded query stream", async () => {
  const base = tmpRepo("cli-acceptance-real");
  const repo = join(base, "repo");
  const fixtureRoot = resolve("cmd/graft/testdata/cli-go-acceptance/real");
  for (const dir of ["src/ask", "src/graph", "src/search", "internal/graph"]) {
    for (const name of readdirSync(join(fixtureRoot, dir))) {
      if (/\.(ts|go)$/.test(name)) write(repo, { [`${dir}/${name}`]: readFileSync(join(fixtureRoot, dir, name), "utf8") });
    }
  }
  write(repo, { "package.json": JSON.stringify({ name: "real" }), "go.mod": "module example.com/real\n\ngo 1.22\n" });
  execFileSync("git", ["init", "-q"], { cwd: repo });
  const home = join(base, "home");
  const elsewhere = join(base, "elsewhere");
  mkdirSync(join(home, ".graft"), { recursive: true });
  mkdirSync(elsewhere, { recursive: true });
  const checkedAt = Date.now();
  writeFileSync(join(home, ".graft", "update-check.json"), JSON.stringify({ latest: "0.0.0", checkedAt }, null, 2));
  const env = { ...process.env, HOME: home, USERPROFILE: home, DO_NOT_TRACK: "1", GRAFT_NO_REFRESH: "1", COLUMNS: "80" };
  execFileSync(TS[0], [...TS.slice(1), "build", repo], { stdio: "pipe", env });
  const normalizer: Twin = { name: "ts", command: TS, base, repo, home, elsewhere, tracked: [], gitDirs: ["."], fixture: {} };
  const vars = { [base]: "<BASE>", [repo]: "<REPO>", [home]: "<HOME>", [elsewhere]: "<ELSEWHERE>" };
  if (process.env.GRAFT_CAPTURE_GOLDENS === "1") {
    const graphFiles: Record<string, string> = {};
    for (const rel of ["graft/.graph/wiring.json", "graft/.cache/ask-index.json", "graft/INDEX.md"]) {
      const path = join(repo, rel);
      if (existsSync(path)) graphFiles[rel] = readFileSync(path, "utf8");
    }
    captureGolden("cli-go-acceptance/seeded-graph", {
      args: ["build", repo],
      cwd: repo,
      env: { DO_NOT_TRACK: "1", GRAFT_NO_REFRESH: "1", COLUMNS: "80" },
      status: 0,
      stdout: "",
      stderr: "",
      files: graphFiles,
      normalize: vars,
    });
  }

  const graph = readGraph(repo);
  const symbols = graph.nodes.filter((node) => node.kind !== "file");
  const files = graph.nodes.filter((node) => node.kind === "file").map((node) => node.path);
  const words = [...new Set(symbols.flatMap((node) => node.name.replace(/([a-z0-9])([A-Z])/g, "$1 $2").toLowerCase().split(/[^a-z0-9]+/)).filter((word) => word.length > 2))];
  let seed = 20260923;
  const pick = <T>(items: T[]): T => items[(seed = (seed * 1103515245 + 12345) % 2147483648) % items.length];
  const queries: string[][] = [];
  for (let i = 0; i < 90; i++) {
    const symbol = pick(symbols);
    switch (i % 6) {
      case 0:
        queries.push(["ask", `${pick(words)} ${pick(words)}`, ...pick([[], ["--json"], ["--source"], ["--no-graph-rank", "--json"], ["--in", "src"]])]);
        break;
      case 1:
        queries.push(["ask", pick([symbol.name, `who calls ${symbol.name}`, `what does ${symbol.name} call`, `${pick(words)} tests`]), ...pick([[], ["--json"], ["-n", "3"]])]);
        break;
      case 2:
        queries.push(["grep", pick([symbol.name, pick(words), `${pick(words)}.*${pick(words)}`, "func \\w+\\("]), ...pick([[], ["-i"], ["--fixed"], ["--json"], ["--in", "internal"]])]);
        break;
      case 3:
        queries.push(["callers", pick([symbol.name, symbol.id]), ...pick([[], ["--direction", "out"], ["--depth", "2"], ["-d", "all", "--json"]])]);
        break;
      case 4:
        queries.push(["skeleton", pick([pick(files), pick(files).split("/").pop()!]), ...pick([[], ["--json"]])]);
        break;
      default:
        queries.push(["map", ...pick([[], ["--json"], ["--max-dirs", "4"]])]);
    }
  }
  const run = (command: string[], args: string[]) =>
    new Promise<Outcome>((done) => {
      const child = spawn(command[0], [...command.slice(1), ...args], { cwd: repo, env });
      let stdout = "";
      let stderr = "";
      child.stdout.on("data", (chunk) => (stdout += chunk));
      child.stderr.on("data", (chunk) => (stderr += chunk));
      child.on("close", (status) => done({ status, stdout, stderr }));
    });
  const failures: string[] = [];
  let next = 0;
  const worker = async () => {
    while (next < queries.length) {
      const index = next++;
      const args = queries[index];
      const [want, got] = await Promise.all([run(TS, args), run([binary()], args)]);
      if (process.env.GRAFT_CAPTURE_GOLDENS === "1") {
        captureGolden(`cli-go-acceptance/seeded/${String(index + 1).padStart(3, "0")}`, {
          args,
          cwd: repo,
          env: { DO_NOT_TRACK: "1", GRAFT_NO_REFRESH: "1", COLUMNS: "80" },
          status: want.status ?? -1,
          stdout: normalize(want.stdout, normalizer),
          stderr: normalize(want.stderr, normalizer),
          files: {},
          normalize: vars,
        });
      }
      try {
        assert.deepEqual(got, want);
      } catch (err) {
        failures.push(`graft ${args.join(" ")}:\n${(err as Error).message}`);
      }
    }
  };
  await Promise.all(Array.from({ length: 6 }, worker));
  assert.deepEqual(failures, [], failures.join("\n\n"));
});
