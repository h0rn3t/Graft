/**
 * TS↔Go differential for the brain commands and telemetry. Twin checkouts talk
 * to one fake brain API; stdout, stderr, exit status, the files each CLI writes
 * and the digests each posts must match, with only clocks and random ids
 * normalised.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { execFile, execFileSync } from "node:child_process";
import { createServer, type Server } from "node:http";
import { chmodSync, cpSync, existsSync, mkdirSync, readFileSync, readdirSync, statSync, writeFileSync } from "node:fs";
import { join, relative } from "node:path";
import { promisify } from "node:util";
import { tmpRepo } from "./helpers.js";

const run = promisify(execFile);
const TS = [process.execPath, "--import", "tsx", "src/cli.ts"];

interface Twin {
  repo: string;
  home: string;
}

interface Result {
  status: number;
  stdout: string;
  stderr: string;
}

function goBinary(): string {
  const binary = join(tmpRepo("brain-parity-bin"), "graft");
  execFileSync("go", ["build", "-o", binary, "./cmd/graft"], { stdio: "pipe" });
  return binary;
}

function write(root: string, files: Record<string, string>): void {
  for (const [rel, text] of Object.entries(files)) {
    mkdirSync(join(root, rel, ".."), { recursive: true });
    writeFileSync(join(root, rel), text);
  }
}

const ISO = /\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z/g;
const UUID = /[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}/g;

function normalize(text: string, twin: Twin): string {
  return text
    .split(twin.repo).join("<REPO>")
    .split(twin.home).join("<HOME>")
    .replace(ISO, "<ISO>")
    .replace(UUID, "<UUID>")
    .replace(/"(fetchedAt|checkedAt|flushedAt)": ?\d+/g, '"$1":<MS>')
    .replace(/"node_major":\s*"\d+",?\s*/g, "")
    .replace(/const BAKED = ".*";/, 'const BAKED = "<BAKED>";')
    .replace(/graft_port=\d+&graft_state=[A-Za-z0-9_-]+/, "graft_port=<PORT>&graft_state=<STATE>");
}

function snapshot(root: string, twin: Twin): Record<string, string> {
  const out: Record<string, string> = {};
  const walk = (dir: string) => {
    for (const name of readdirSync(dir)) {
      if (name === ".git") continue;
      const path = join(dir, name);
      const rel = relative(root, path);
      // The graph itself is each CLI's own build (see build-go-parity.test.ts);
      // only the files these commands write under graft/ are compared here.
      if (rel.startsWith("graft/") && !/^graft\/\.cache\/(wiring-stamp|brain-rules|telemetry-repo-id)\.json$/.test(rel) && !statSync(path).isDirectory()) continue;
      if (statSync(path).isDirectory()) walk(path);
      else out[rel] = normalize(readFileSync(path, "utf8"), twin);
    }
  };
  if (existsSync(root)) walk(root);
  return out;
}

const posted: { url: string; body: unknown }[] = [];

function fakeBrain(): Promise<{ server: Server; url: string }> {
  const server = createServer((req, res) => {
    let raw = "";
    req.on("data", (chunk) => (raw += chunk));
    req.on("end", () => {
      res.setHeader("content-type", "application/json");
      if (req.headers.authorization !== "Bearer tok") {
        res.statusCode = 401;
        res.end("{}");
        return;
      }
      if (req.method === "POST") {
        posted.push({ url: req.url ?? "", body: JSON.parse(raw) });
        res.end(JSON.stringify({ job_id: "job-1" }));
        return;
      }
      if (req.url === "/api/public/brains/B1/rules/anchors") {
        res.end(JSON.stringify({
          anchors: [
            { rule_id: "r1", symbol: "src/store.ts#Store.get", fingerprint: "stale-hash", rule: "Reads never hit the network.", source_url: "https://github.com/acme/widgets/pull/7" },
            { rule_id: "r2", symbol: "general", fingerprint: "", rule: "Keep PRs under 400 lines." },
            { rule_id: "r3", symbol: "src/a b.ts#x", rule: "Spaces sort before commas." },
            { rule_id: "r4", symbol: "", rule: "dropped: no symbol" },
          ],
        }));
        return;
      }
      if (req.url === "/api/public/brains/B1/repo") {
        res.end(JSON.stringify({
          repo: { slug: "Acme/Widgets", status: "completed", rule_count: 3, commit_count: 4, thread_count: 0 },
          brain_name: "Widgets",
          build: { found_so_far: 3, filed_so_far: 3 },
        }));
        return;
      }
      res.statusCode = 404;
      res.end("{}");
    });
  });
  return new Promise((done) => server.listen(0, "127.0.0.1", () => done({ server, url: `http://127.0.0.1:${(server.address() as { port: number }).port}` })));
}

function twins(): { ts: Twin; go: Twin; bin: string } {
  const base = tmpRepo("brain-parity");
  const ts: Twin = { repo: join(base, "ts", "repo"), home: join(base, "ts", "home") };
  const bin = join(base, "bin");
  write(bin, { gh: "#!/bin/sh\nexit 1\n" });
  chmodSync(join(bin, "gh"), 0o755);
  write(ts.home, {
    // A fresh registry answer and a recent flush, so neither CLI spawns a child.
    ".graft/update-check.json": JSON.stringify({ latest: "0.0.1", checkedAt: Date.now() }, null, 2),
  });
  mkdirSync(join(ts.home, ".codex"), { recursive: true });
  write(ts.repo, {
    "src/store.ts": "export class Store {\n  get(key: string): string { return key; }\n}\n",
    "test/store.test.ts": "export function testReturnsTheStoredKeyUnchanged() {}\n",
    "CLAUDE.md": "Always run the linter.\n\n<!-- graft:start -->\nmanaged\n<!-- graft:end -->\n",
    ".github/workflows/ci.yml": "on: push\njobs: {}\n",
    ".editorconfig": "root = true\n",
    "docs/adr/0001-record.md": "# Use SQLite\n\nWe chose SQLite.\n",
  });
  const git = (...args: string[]) => execFileSync("git", args, { cwd: ts.repo, stdio: "pipe" });
  git("init", "-q", "-b", "main");
  git("config", "user.name", "Ann");
  git("config", "user.email", "ann@example.com");
  git("remote", "add", "origin", "git@github.com:acme/widgets.git");
  git("add", "-A");
  git("commit", "-qm", "Add the store\n\nReads stay local.");
  write(ts.repo, { "src/store.ts": "export class Store {\n  get(key: string): string { return key + ''; }\n}\n" });
  git("commit", "-qam", "Tweak get");
  git("revert", "--no-edit", "HEAD");
  const go: Twin = { repo: join(base, "go", "repo"), home: join(base, "go", "home") };
  cpSync(join(base, "ts"), join(base, "go"), { recursive: true });
  return { ts, go, bin };
}

async function invoke(command: string[], args: string[], env: NodeJS.ProcessEnv): Promise<Result> {
  try {
    const { stdout, stderr } = await run(command[0], [...command.slice(1), ...args], { env });
    return { status: 0, stdout, stderr };
  } catch (err) {
    const e = err as { code?: number; stdout?: string; stderr?: string };
    return { status: typeof e.code === "number" ? e.code : -1, stdout: e.stdout ?? "", stderr: e.stderr ?? "" };
  }
}

test("Go brain and telemetry commands match TypeScript", async () => {
  const binary = goBinary();
  const pair = twins();
  const brain = await fakeBrain();
  try {
    const envFor = (twin: Twin, extra: NodeJS.ProcessEnv = {}): NodeJS.ProcessEnv => {
      const env: NodeJS.ProcessEnv = {
        ...process.env, HOME: twin.home, USERPROFILE: twin.home, GRAFT_MCP_NPX: "1", GRAFT_NO_BROWSER: "1",
        GRAFT_BRAIN_URL: brain.url, DO_NOT_TRACK: "1", PATH: `${pair.bin}:${process.env.PATH}`, ...extra,
      };
      for (const name of ["GH_TOKEN", "GITHUB_TOKEN", "GRAFT_DIR", "GRAFT_BRAIN_TOKEN", "GRAFT_BRAIN_ID", "GRAFT_POSTHOG_KEY", "GRAFT_POSTHOG_HOST"]) {
        if (!(name in extra)) delete env[name];
      }
      return env;
    };
    const both = async (args: string[], label: string, extra: NodeJS.ProcessEnv = {}) => {
      const sub = (twin: Twin) => args.map((a) => (a === "<REPO>" ? twin.repo : a));
      posted.length = 0;
      const tsResult = await invoke(TS, sub(pair.ts), envFor(pair.ts, extra));
      const tsPosts = posted.splice(0);
      const goResult = await invoke([binary], sub(pair.go), envFor(pair.go, extra));
      const goPosts = posted.splice(0);
      const norm = (r: Result, twin: Twin) => ({ status: r.status, stdout: normalize(r.stdout, twin), stderr: normalize(r.stderr, twin) });
      assert.deepEqual(norm(goResult, pair.go), norm(tsResult, pair.ts), `${label}: output`);
      assert.deepEqual(goPosts, tsPosts, `${label}: posted digests`);
      assert.deepEqual(snapshot(pair.go.repo, pair.go), snapshot(pair.ts.repo, pair.ts), `${label}: repo files`);
      assert.deepEqual(snapshot(pair.go.home, pair.go), snapshot(pair.ts.home, pair.ts), `${label}: home files`);
      return { result: norm(goResult, pair.go), posts: goPosts };
    };

    execFileSync(process.execPath, [...TS.slice(1), "build", pair.ts.repo], { stdio: "pipe", env: envFor(pair.ts) });
    execFileSync(binary, ["build", pair.go.repo], { stdio: "pipe", env: envFor(pair.go) });
    await both(["init", "<REPO>", "--agents", "agents", "copilot", "--no-build"], "init");
    await both(["brain", "status", "<REPO>"], "status before connect");
    await both(["brain", "status", "<REPO>", "--json"], "status json before connect");
    await both(["brain", "pull", "<REPO>"], "pull before connect");
    await both(["brain", "connect", "nope", "<REPO>"], "bad handoff");
    const connected = await both(["brain", "connect", "B1:tok", "<REPO>"], "connect");
    assert.match(connected.result.stderr, /✓ pulled 3 rule\(s\) from B1/);
    assert.match(readFileSync(join(pair.go.repo, "AGENTS.md"), "utf8"), /### src\/a b\.ts\n[\s\S]*### src\/store\.ts/);
    await both(["brain", "status", "<REPO>"], "status");
    await both(["brain", "status", "<REPO>", "--json"], "status json");
    await both(["brain", "pull", "<REPO>"], "pull");
    await both(["brain", "pull", "<REPO>"], "pull while the brain is down", { GRAFT_BRAIN_URL: "http://127.0.0.1:1" });
    const pushed = await both(["brain", "push", "<REPO>", "--no-watch", "--no-approve"], "push without watching");
    assert.equal(pushed.posts.length, 1, "one digest posted");
    assert.match(pushed.result.stderr, /✓ sent acme\/widgets to “Widgets”/);
    await both(["brain", "push", "<REPO>"], "push and watch");
    await both(["brain", "disconnect", "<REPO>"], "disconnect");
    await both(["brain", "push", "<REPO>"], "push with no brain and no terminal");
    await both(["init", "<REPO>", "--agents", "agents", "--no-build", "--brain", "B1:tok"], "init with a brain");
    await both(["init", "<REPO>", "--agents", "agents", "--no-build", "--brain", ":"], "init with a bad brain");

    const on = { GRAFT_POSTHOG_KEY: "phc_test", DO_NOT_TRACK: "", CI: "", GITHUB_ACTIONS: "", GRAFT_POSTHOG_HOST: "http://127.0.0.1:1" };
    await both(["telemetry"], "status under DO_NOT_TRACK");
    for (const twin of [pair.ts, pair.go]) {
      write(twin.home, { ".graft/telemetry.json": JSON.stringify({ installId: "11111111-1111-4111-8111-111111111111", flushedAt: Date.now() }, null, 2) });
    }
    const first = await both(["telemetry", "status"], "status with telemetry on", on);
    assert.match(first.result.stderr, /graft collects anonymous usage stats/, "first-run notice");
    await both(["map", "<REPO>"], "a tracked query", on);
    await both(["telemetry", "debug"], "debug shows the queued batch", on);
    await both(["telemetry", "disable"], "disable", on);
    await both(["telemetry", "status"], "status when disabled", on);
    await both(["telemetry", "enable"], "enable", on);
    await both(["telemetry", "bogus"], "unknown action", on);
  } finally {
    brain.server.close();
  }
});
