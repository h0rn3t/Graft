/**
 * TS↔Go differential for the MCP server: one scripted JSON-RPC session is
 * played against each server, request by request, and every reply must match —
 * results, error codes and messages, tool-level error markers, silence for
 * notifications, and a stdout that carries nothing but protocol lines. The
 * session covers every tool and alias, bad input, a refresh after an edit, and
 * the freshness report for a clean and a drifted graph.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { execFileSync, spawn } from "node:child_process";
import { cpSync, mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { tmpRepo } from "./helpers.js";

const TS = [process.execPath, "--import", "tsx", "src/cli.ts"];

function write(root: string, files: Record<string, string>): void {
  for (const [rel, text] of Object.entries(files)) {
    mkdirSync(join(root, rel, ".."), { recursive: true });
    writeFileSync(join(root, rel), text);
  }
}

type Step =
  | { send: object | string; reply: false }
  | { send: object; reply: true; id: number }
  | { edit: Record<string, string> };

/** A lock another process just took, so a refresh has to wait it out. */
const HELD_LOCK = { "graft/.cache/.sync.lock": "held\n" };

interface Session {
  replies: Record<number, unknown>;
  stray: string[];
  stderr: string;
}

async function play(command: string[], root: string, steps: Step[], env: NodeJS.ProcessEnv): Promise<Session> {
  const child = spawn(command[0], [...command.slice(1), "mcp", root], { env, stdio: ["pipe", "pipe", "pipe"] });
  const replies: Record<number, unknown> = {};
  const stray: string[] = [];
  let stderr = "";
  let buffer = "";
  const waiting = new Map<number, () => void>();
  child.stderr.on("data", (chunk) => (stderr += chunk));
  child.stdout.on("data", (chunk) => {
    buffer += chunk;
    let cut: number;
    while ((cut = buffer.indexOf("\n")) >= 0) {
      const line = buffer.slice(0, cut);
      buffer = buffer.slice(cut + 1);
      if (!line.trim()) continue;
      let message: { id?: number | null };
      try {
        message = JSON.parse(line);
      } catch {
        stray.push(line);
        continue;
      }
      const key = typeof message.id === "number" ? message.id : -1;
      replies[key] = message;
      waiting.get(key)?.();
    }
  });
  for (const step of steps) {
    if ("edit" in step) {
      write(root, step.edit);
      continue;
    }
    const line = typeof step.send === "string" ? step.send : JSON.stringify(step.send);
    if (!step.reply) {
      child.stdin.write(`${line}\n`);
      // A notification gets no reply; give a wrong reply the chance to appear.
      await new Promise((done) => setTimeout(done, 150));
      continue;
    }
    const answered = new Promise<void>((done) => waiting.set(step.id, done));
    child.stdin.write(`${line}\n`);
    await Promise.race([answered, new Promise((_, fail) => setTimeout(() => fail(new Error(`no reply to ${line}`)), 30000))]);
  }
  child.stdin.end();
  await new Promise((done) => child.on("close", done));
  return { replies, stray, stderr };
}

const call = (id: number, name: string, args: object = {}) => ({ jsonrpc: "2.0", id, method: "tools/call", params: { name, arguments: args } });

test("Go MCP server matches TypeScript over a full session", async () => {
  const binary = join(tmpRepo("mcp-parity-bin"), "graft");
  execFileSync("go", ["build", "-o", binary, "./cmd/graft"], { stdio: "pipe" });
  const base = tmpRepo("mcp-parity");
  const ts = join(base, "ts");
  const home = join(base, "home");
  mkdirSync(join(home, ".graft"), { recursive: true });
  writeFileSync(join(home, ".graft", "update-check.json"), JSON.stringify({ latest: "0.0.1", checkedAt: Date.now() }, null, 2));
  write(ts, {
    "src/store.ts": "export class Store {\n  get(key: string): string {\n    return helper(key);\n  }\n}\nexport function helper(key: string): string { return key; }\n",
    "src/app.ts": 'import { Store } from "./store";\nexport function run(): string {\n  return new Store().get("x");\n}\n',
    "README.md": "# fixture\n",
  });
  execFileSync("git", ["init", "-q"], { cwd: ts });
  const go = join(base, "go");
  cpSync(ts, go, { recursive: true });
  const env: NodeJS.ProcessEnv = { ...process.env, HOME: home, USERPROFILE: home, DO_NOT_TRACK: "1", GRAFT_MCP_NPX: "1" };
  delete env.GRAFT_DIR;
  execFileSync(process.execPath, [...TS.slice(1), "build", ts], { stdio: "pipe", env });
  execFileSync(binary, ["build", go], { stdio: "pipe", env });

  const steps: Step[] = [
    { send: { jsonrpc: "2.0", id: 1, method: "initialize", params: { protocolVersion: "2025-06-18", capabilities: {}, clientInfo: { name: "t", version: "1" } } }, reply: true, id: 1 },
    { send: { jsonrpc: "2.0", method: "notifications/initialized" }, reply: false },
    { send: { jsonrpc: "2.0", id: 2, method: "ping" }, reply: true, id: 2 },
    { send: { jsonrpc: "2.0", id: 3, method: "tools/list" }, reply: true, id: 3 },
    { send: call(4, "graft_find_code", { query: "store get helper" }), reply: true, id: 4 },
    { send: call(5, "graft_find_all", { pattern: "helper" }), reply: true, id: 5 },
    { send: call(6, "graft_trace_calls", { symbol: "helper" }), reply: true, id: 6 },
    { send: call(7, "graft_trace_calls", { symbol: "run", direction: "out", depth: "all" }), reply: true, id: 7 },
    { send: call(8, "graft_file_api", { file: "src/store.ts" }), reply: true, id: 8 },
    { send: call(9, "graft_repo_map", { max_dirs: 3 }), reply: true, id: 9 },
    { send: call(10, "graft_check_freshness"), reply: true, id: 10 },
    { send: call(11, "graft_ask", { query: "run" }), reply: true, id: 11 },
    { send: call(12, "graft_callers", { symbol: "nope" }), reply: true, id: 12 },
    { send: call(13, "graft_nope"), reply: true, id: 13 },
    { send: call(14, "graft_find_code"), reply: true, id: 14 },
    { send: call(15, "graft_find_all", { pattern: "zzz_no_such_text" }), reply: true, id: 15 },
    { send: { jsonrpc: "2.0", id: 16, method: "resources/list" }, reply: true, id: 16 },
    { send: "this is not json", reply: false },
    { send: { jsonrpc: "2.0", method: "notifications/unknown" }, reply: false },
    { send: { jsonrpc: "2.0", method: "tools/call", params: { name: "graft_repo_map", arguments: {} } }, reply: false },
    { edit: { "src/extra.ts": 'import { helper } from "./store";\nexport function extra(): string { return helper("y"); }\n' } },
    { send: call(17, "graft_trace_calls", { symbol: "helper" }), reply: true, id: 17 },
    { edit: { "src/app.ts": 'import { Store } from "./store";\nexport function run(): string {\n  return new Store().get("changed");\n}\n' } },
    { send: call(18, "graft_check_freshness"), reply: true, id: 18 },
    { edit: HELD_LOCK },
    { send: call(19, "graft_repo_map"), reply: true, id: 19 },
  ];
  const tsSession = await play(TS, ts, steps, env);
  const goSession = await play([binary], go, steps, env);
  const normalize = (session: Session, root: string): Session =>
    JSON.parse(JSON.stringify(session).split(root).join("<ROOT>").replace(/"version":"[^"]*"/g, '"version":"<V>"'));
  const got = normalize(goSession, go);
  const want = normalize(tsSession, ts);
  assert.deepEqual(got.stray, [], "stdout carries protocol lines only");
  for (const id of Object.keys(want.replies)) {
    assert.deepEqual(got.replies[Number(id)], want.replies[Number(id)], `reply ${id}`);
  }
  assert.deepEqual(Object.keys(got.replies).sort(), Object.keys(want.replies).sort(), "no replies to notifications");
  assert.equal(got.stderr, want.stderr, "stderr");
  assert.match(JSON.stringify(got.replies[17]), /refreshed the graph \(1 file changed\)/, "an edit refreshes before answering");
  assert.match(JSON.stringify(got.replies[18]), /graph check: STALE/, "the freshness tool reports drift without fixing it");
  assert.match(JSON.stringify(got.replies[19]), /a graph rebuild is already in flight/, "a held lock answers from the current graph");
  assert.match(JSON.stringify(got.replies[16]), /-32601/, "unknown methods are JSON-RPC errors");
});

async function compareSessions(
  binary: string, ts: string, go: string, steps: Step[], env: NodeJS.ProcessEnv, label: string,
  also: [string, string][] = [],
): Promise<Session> {
  const tsSession = await play(TS, ts, steps, env);
  const goSession = await play([binary], go, steps, env);
  const normalize = (session: Session, roots: string[]): Session => {
    let text = JSON.stringify(session);
    roots.forEach((root, i) => (text = text.split(root).join(`<ROOT${i}>`)));
    return JSON.parse(text.replace(/"version":"[^"]*"/g, '"version":"<V>"'));
  };
  const got = normalize(goSession, [go, ...also.map(([, g]) => g)]);
  const want = normalize(tsSession, [ts, ...also.map(([t]) => t)]);
  assert.deepEqual(got.stray, [], `${label}: stdout carries protocol lines only`);
  for (const id of Object.keys(want.replies)) {
    assert.deepEqual(got.replies[Number(id)], want.replies[Number(id)], `${label}: reply ${id}`);
  }
  assert.deepEqual(Object.keys(got.replies).sort(), Object.keys(want.replies).sort(), `${label}: reply ids`);
  assert.equal(got.stderr, want.stderr, `${label}: stderr`);
  return got;
}

function scratch(tag: string): { base: string; home: string; env: NodeJS.ProcessEnv; binary: string } {
  const binary = join(tmpRepo(`${tag}-bin`), "graft");
  execFileSync("go", ["build", "-o", binary, "./cmd/graft"], { stdio: "pipe" });
  const base = tmpRepo(tag);
  const home = join(base, "home");
  mkdirSync(join(home, ".graft"), { recursive: true });
  writeFileSync(join(home, ".graft", "update-check.json"), JSON.stringify({ latest: "0.0.1", checkedAt: Date.now() }, null, 2));
  const env: NodeJS.ProcessEnv = { ...process.env, HOME: home, USERPROFILE: home, DO_NOT_TRACK: "1", GRAFT_MCP_NPX: "1" };
  delete env.GRAFT_DIR;
  return { base, home, env, binary };
}

const init = (id: number) => ({ jsonrpc: "2.0", id, method: "initialize", params: { protocolVersion: "2024-11-05", capabilities: {}, clientInfo: { name: "t", version: "1" } } });

test("Go MCP server matches TypeScript on a workspace root", async () => {
  const { base, env, binary } = scratch("mcp-parity-ws");
  const ts = join(base, "ts");
  for (const [child, text] of [["api", "export function serve() { return handle(); }\nexport function handle() { return 1; }\n"], ["web", "export function render() { return 2; }\nexport function handle() { return 3; }\n"]]) {
    write(join(ts, child), { "src/index.ts": text });
    execFileSync("git", ["init", "-q"], { cwd: join(ts, child) });
  }
  mkdirSync(join(ts, "docs", ".git"), { recursive: true });
  const go = join(base, "go");
  cpSync(ts, go, { recursive: true });
  execFileSync(process.execPath, [...TS.slice(1), "build", ts], { stdio: "pipe", env });
  execFileSync(binary, ["build", go], { stdio: "pipe", env });
  await compareSessions(binary, ts, go, [
    { send: init(1), reply: true, id: 1 },
    { send: { jsonrpc: "2.0", id: 2, method: "tools/list" }, reply: true, id: 2 },
    { send: call(3, "graft_find_code", { query: "handle" }), reply: true, id: 3 },
    { send: call(4, "graft_find_code", { query: "handle", in: "web/" }), reply: true, id: 4 },
    { send: call(5, "graft_find_all", { pattern: "handle" }), reply: true, id: 5 },
    { send: call(6, "graft_trace_calls", { symbol: "handle" }), reply: true, id: 6 },
    { send: call(7, "graft_trace_calls", { symbol: "nothing_here" }), reply: true, id: 7 },
    { send: call(8, "graft_repo_map"), reply: true, id: 8 },
    { send: call(9, "graft_check_freshness"), reply: true, id: 9 },
    { send: call(10, "graft_file_api", { file: "api/src/index.ts" }), reply: true, id: 10 },
    { edit: { "web/src/index.ts": "export function render() { return handle(); }\nexport function handle() { return 3; }\n" } },
    { send: call(11, "graft_trace_calls", { symbol: "handle", in: "web/" }), reply: true, id: 11 },
  ], env, "workspace");
});

test("Go MCP server matches TypeScript with no graph and in a git worktree", async () => {
  const { base, env, binary } = scratch("mcp-parity-fresh");
  const files = { "src/a.ts": "export function a() { return b(); }\nexport function b() { return 1; }\n" };
  const [ts, go] = [join(base, "ts"), join(base, "go")];
  for (const root of [ts, go]) {
    write(root, files);
    execFileSync("git", ["init", "-q"], { cwd: root });
  }
  const unbuilt: Step[] = [
    { send: init(1), reply: true, id: 1 },
    { send: { jsonrpc: "2.0", id: 2, method: "tools/list" }, reply: true, id: 2 },
    { send: call(3, "graft_check_freshness"), reply: true, id: 3 },
    { send: call(4, "graft_trace_calls", { symbol: "b" }), reply: true, id: 4 },
    { send: call(5, "graft_check_freshness"), reply: true, id: 5 },
  ];
  await compareSessions(binary, ts, go, unbuilt, env, "no graph yet");

  const git = (root: string, ...args: string[]) => execFileSync("git", ["-c", "user.name=t", "-c", "user.email=t@t", ...args], { cwd: root, stdio: "pipe" });
  execFileSync(process.execPath, [...TS.slice(1), "build", ts], { stdio: "pipe", env });
  execFileSync(binary, ["build", go], { stdio: "pipe", env });
  for (const root of [ts, go]) {
    git(root, "add", "-A");
    git(root, "commit", "-qm", "init");
    git(root, "worktree", "add", "-q", join(root, "..", `${root === ts ? "ts" : "go"}-wt`));
  }
  const seeded = await compareSessions(binary, join(base, "ts-wt"), join(base, "go-wt"), [
    { send: init(1), reply: true, id: 1 },
    { send: call(2, "graft_trace_calls", { symbol: "b" }), reply: true, id: 2 },
  ], env, "linked worktree seeds from the main checkout", [[ts, go]]);
  assert.match(JSON.stringify(seeded.replies[2]), /copied the graph from the main checkout/);
});
