/**
 * TS↔Go differential for startup upkeep: an MCP server booting in a repo whose
 * wiring stamp is stale must rewrite every wired host with the stamped choices,
 * restore a lost rule file, stamp the running version, and carry the refresh
 * note and the update nudge in its instructions — identically in both CLIs.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import { cpSync, existsSync, mkdirSync, readFileSync, readdirSync, rmSync, statSync, writeFileSync } from "node:fs";
import { join, relative } from "node:path";
import { tmpRepo } from "./helpers.js";

const TS = [process.execPath, "--import", "tsx", "src/cli.ts"];

interface Twin {
  repo: string;
  home: string;
}

function env(twin: Twin): NodeJS.ProcessEnv {
  const out: NodeJS.ProcessEnv = { ...process.env, HOME: twin.home, USERPROFILE: twin.home, GRAFT_MCP_NPX: "1", DO_NOT_TRACK: "1" };
  for (const name of ["GRAFT_DIR", "GRAFT_NO_STATUSLINE", "GRAFT_BRAIN_TOKEN", "GRAFT_BRAIN_ID"]) delete out[name];
  return out;
}

function normalize(text: string, twin: Twin): string {
  return text
    .split(twin.repo).join("<REPO>")
    .split(twin.home).join("<HOME>")
    .replace(/const BAKED = ".*";/, 'const BAKED = "<BAKED>";')
    .replace(/"at": "[^"]+"/, '"at": "<AT>"');
}

function snapshot(root: string, twin: Twin): Record<string, string> {
  const out: Record<string, string> = {};
  const walk = (dir: string) => {
    for (const name of readdirSync(dir)) {
      const path = join(dir, name);
      const rel = relative(root, path);
      if (name === ".git" || (rel.startsWith("graft/") && !rel.startsWith("graft/.cache/wiring-stamp") && !statSync(path).isDirectory())) continue;
      if (statSync(path).isDirectory()) walk(path);
      else out[rel] = normalize(readFileSync(path, "utf8"), twin);
    }
  };
  if (existsSync(root)) walk(root);
  return out;
}

const INITIALIZE = JSON.stringify({ jsonrpc: "2.0", id: 1, method: "initialize", params: { protocolVersion: "2024-11-05", capabilities: {}, clientInfo: { name: "t", version: "1" } } }) + "\n";

function boot(command: string[], twin: Twin): { stdout: unknown; stderr: string } {
  const res = spawnSync(command[0], [...command.slice(1), "mcp", twin.repo], { env: env(twin), input: INITIALIZE, encoding: "utf8" });
  const reply = JSON.parse(res.stdout.split("\n").find((line) => line.includes('"id":1')) ?? "null") as { result?: { instructions?: string; serverInfo?: unknown } } | null;
  return {
    stdout: { instructions: normalize(reply?.result?.instructions ?? "", twin), serverInfo: reply?.result?.serverInfo },
    stderr: normalize(res.stderr, twin),
  };
}

test("Go MCP startup upkeep matches TypeScript on a stale wiring stamp", () => {
  const binary = join(tmpRepo("upkeep-parity-bin"), "graft");
  execFileSync("go", ["build", "-o", binary, "./cmd/graft"], { stdio: "pipe" });
  const base = tmpRepo("upkeep-parity");
  const ts: Twin = { repo: join(base, "ts", "repo"), home: join(base, "ts", "home") };
  for (const dir of [".codex", ".cursor", ".gemini/config", ".graft"]) mkdirSync(join(ts.home, dir), { recursive: true });
  // A fresh registry answer, so neither CLI spawns a background update check.
  writeFileSync(join(ts.home, ".graft", "update-check.json"), JSON.stringify({ latest: "99.0.0", checkedAt: Date.now() }, null, 2));
  mkdirSync(join(ts.repo, "src"), { recursive: true });
  writeFileSync(join(ts.repo, "src", "main.ts"), "export function main() { return 1; }\n");
  execFileSync("git", ["init", "-q"], { cwd: ts.repo });
  const go: Twin = { repo: join(base, "go", "repo"), home: join(base, "go", "home") };
  cpSync(join(base, "ts"), join(base, "go"), { recursive: true });

  execFileSync(process.execPath, [...TS.slice(1), "init", ts.repo, "--agents", "claude", "agents", "cursor", "gemini", "--no-statusline", "--no-build"], { env: env(ts), stdio: "pipe" });
  execFileSync(binary, ["init", go.repo, "--agents", "claude", "agents", "cursor", "gemini", "--no-statusline", "--no-build"], { env: env(go), stdio: "pipe" });

  for (const twin of [ts, go]) {
    const stampPath = join(twin.repo, "graft", ".cache", "wiring-stamp.json");
    const stamp = JSON.parse(readFileSync(stampPath, "utf8")) as { version: string };
    stamp.version = "0.0.1";
    writeFileSync(stampPath, JSON.stringify(stamp, null, 2));
    rmSync(join(twin.repo, ".cursor", "rules", "graft.mdc"));
    writeFileSync(join(twin.repo, "GEMINI.md"), "# Mine\n\n<!-- graft:start -->\nold text\n<!-- graft:end -->\n");
  }

  const tsBoot = boot(TS, ts);
  const goBoot = boot([binary], go);
  assert.deepEqual(goBoot, tsBoot, "MCP boot");
  assert.match(tsBoot.stderr, /graft refreshed this repo's agent wiring \(including this machine's ~\/\.codex config\) \(written by 0\.0\.1, now/);
  assert.match(tsBoot.stderr, /⬆ graft .* → 99\.0\.0 available/);
  assert.deepEqual(snapshot(go.repo, go), snapshot(ts.repo, ts), "repo after upkeep");
  assert.deepEqual(snapshot(go.home, go), snapshot(ts.home, ts), "home after upkeep");
  assert.ok(existsSync(join(go.repo, ".cursor", "rules", "graft.mdc")), "the lost rule file is restored");

  const again = boot([binary], go);
  assert.doesNotMatch(again.stderr, /refreshed this repo's agent wiring/, "a current stamp is a no-op");
});
