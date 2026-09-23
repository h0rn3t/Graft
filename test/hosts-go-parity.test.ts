/**
 * TS↔Go differential for host setup: `graft init` and `graft uninstall` run
 * against twin repositories and twin home directories, and every file both
 * write — repo and machine-level — must match byte for byte, as must stderr,
 * stdout and the exit status. The fixture carries user-owned configuration in
 * every shared file so preservation, idempotence and retraction are covered.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import { cpSync, existsSync, mkdirSync, readFileSync, readdirSync, statSync, writeFileSync } from "node:fs";
import { join, relative } from "node:path";
import { tmpRepo } from "./helpers.js";

interface Result {
  status: number | null;
  stdout: string;
  stderr: string;
}

const TS = [process.execPath, "--import", "tsx", "src/cli.ts"];

function goBinary(): string {
  const binary = join(tmpRepo("hosts-parity-bin"), "graft");
  execFileSync("go", ["build", "-o", binary, "./cmd/graft"], { stdio: "pipe" });
  return binary;
}

function write(root: string, files: Record<string, string>): void {
  for (const [rel, text] of Object.entries(files)) {
    mkdirSync(join(root, rel, ".."), { recursive: true });
    writeFileSync(join(root, rel), text);
  }
}

interface Twin {
  repo: string;
  home: string;
}

function env(home: string): NodeJS.ProcessEnv {
  const out: NodeJS.ProcessEnv = { ...process.env, HOME: home, USERPROFILE: home, GRAFT_MCP_NPX: "1", DO_NOT_TRACK: "1" };
  for (const name of ["GRAFT_DIR", "GRAFT_NO_STATUSLINE", "GRAFT_BRAIN_TOKEN", "GRAFT_BRAIN_ID", "GRAFT_BRAIN_URL"]) delete out[name];
  return out;
}

function runIn(command: string[], args: string[], twin: Twin, extra: NodeJS.ProcessEnv = {}): Result {
  const res = spawnSync(command[0], [...command.slice(1), ...args], {
    env: { ...env(twin.home), ...extra },
    encoding: "utf8",
    cwd: process.cwd(),
  });
  const norm = (text: string) => text.split(twin.repo).join("<REPO>").split(twin.home).join("<HOME>");
  return { status: res.status, stdout: norm(res.stdout), stderr: norm(res.stderr) };
}

/** Every file under root, relative path → content, with the shim's baked
 * package directory and the stamp's clock normalised. */
function snapshot(root: string, twin: Twin): Record<string, string> {
  const out: Record<string, string> = {};
  const walk = (dir: string) => {
    for (const name of readdirSync(dir)) {
      const path = join(dir, name);
      if (statSync(path).isDirectory()) {
        walk(path);
        continue;
      }
      const mode = (statSync(path).mode & 0o777).toString(8);
      out[`${relative(root, path)} [${mode}]`] = readFileSync(path, "utf8")
        .replace(/const BAKED = ".*";/, 'const BAKED = "<BAKED>";')
        .replace(/"at": "[^"]+"/, '"at": "<AT>"')
        .split(twin.repo).join("<REPO>")
        .split(twin.home).join("<HOME>");
    }
  };
  if (existsSync(root)) walk(root);
  return out;
}

function fixture(): { ts: Twin; go: Twin } {
  const base = tmpRepo("hosts-parity");
  const ts: Twin = { repo: join(base, "ts", "repo"), home: join(base, "ts", "home") };
  for (const dir of [".codex", ".cursor", ".gemini/config", ".config/opencode", ".kiro", ".codeium/windsurf", ".adal", ".grok", ".hermes"]) {
    mkdirSync(join(ts.home, dir), { recursive: true });
  }
  write(ts.home, {
    ".codex/config.toml": '[mcp_servers.other]\ncommand = "other"\n',
    ".codex/hooks.json": JSON.stringify({ hooks: { Stop: [{ hooks: [{ type: "command", command: "echo mine" }] }] }, custom: 1 }, null, 2) + "\n",
    ".claude.json": JSON.stringify({ numStartups: 3, mcpServers: { mine: { command: "mine" } } }, null, 2) + "\n",
    // A fresh registry answer, so neither CLI spawns a background update check.
    ".graft/update-check.json": JSON.stringify({ latest: "0.0.1", checkedAt: Date.now() }, null, 2),
  });
  write(ts.repo, {
    ".github/workflows/ci.yml": "on: push\n",
    "AGENTS.md": "# Team notes\n\nKeep PRs small.\n",
    ".claude/settings.json": JSON.stringify({
      permissions: { allow: ["Bash(npm test:*)", "Bash(graft:*)"], deny: ["Bash(rm:*)"] },
      hooks: { Stop: [{ hooks: [{ type: "command", command: "echo done" }] }] },
      statusLine: { type: "command", command: "my-statusline" },
      "2": "numeric key",
    }, null, 2) + "\n",
    ".cursor/mcp.json": JSON.stringify({ mcpServers: { other: { command: "x", args: ["1e3", 1e3, 0.1] } } }) + "\n",
    "src/main.ts": "export function main() { return 1; }\n",
  });
  execFileSync("git", ["init", "-q"], { cwd: ts.repo });
  const go: Twin = { repo: join(base, "go", "repo"), home: join(base, "go", "home") };
  cpSync(join(base, "ts"), join(base, "go"), { recursive: true });
  return { ts, go };
}

test("Go init and uninstall match TypeScript writes, output, and retraction", () => {
  const binary = goBinary();
  const twins = fixture();

  const cwdArgs = (twin: Twin, args: string[]) => args.map((a) => (a === "<REPO>" ? twin.repo : a));
  const both = (args: string[], label: string, extra: NodeJS.ProcessEnv = {}) => {
    const tsResult = runIn(TS, cwdArgs(twins.ts, args), twins.ts, extra);
    const goResult = runIn([binary], cwdArgs(twins.go, args), twins.go, extra);
    assert.deepEqual(goResult, tsResult, `${label}: output`);
    assert.deepEqual(snapshot(twins.go.repo, twins.go), snapshot(twins.ts.repo, twins.ts), `${label}: repo files`);
    assert.deepEqual(snapshot(twins.go.home, twins.go), snapshot(twins.ts.home, twins.ts), `${label}: home files`);
    return goResult;
  };

  both(["init", "--list-agents"], "list agents");
  both(["init", "<REPO>"], "non-interactive help");
  both(["init", "<REPO>", "--agents", "claude", "nope"], "unknown agent");
  const dry = both(["init", "<REPO>", "--dry-run"], "dry run");
  assert.match(dry.stderr, /would write — this repo:/);
  const first = both(["init", "<REPO>", "--all-agents", "--no-build"], "init all agents");
  assert.match(first.stderr, /✓ mcp codex: <HOME>\/\.codex\/config\.toml/);
  const again = both(["init", "<REPO>", "--all-agents", "--no-build"], "init again is idempotent");
  assert.match(again.stderr, /· mcp claude: <REPO>\/\.mcp\.json \(already registered\)/);
  assert.match(again.stderr, /✓ hook codex-hooks: <HOME>\/\.codex\/hooks\.json \(unchanged\)/);
  const settings = JSON.parse(readFileSync(join(twins.go.repo, ".claude", "settings.json"), "utf8"));
  assert.deepEqual(settings.permissions.deny, ["Bash(rm:*)"], "user permissions survive");
  assert.equal(settings.statusLine.command, "my-statusline", "a foreign statusline is left alone");
  assert.match(readFileSync(join(twins.go.repo, "AGENTS.md"), "utf8"), /^# Team notes\n\nKeep PRs small\.\n\n<!-- graft:start -->/);
  const narrow = both(["init", "<REPO>", "--agents", "cursor", "gemini", "--no-global", "--no-build", "--no-hooks"], "narrow to two agents without global writes");
  assert.match(narrow.stderr, /- removed <REPO>\/\.claude\/settings\.json/);
  both(["init", "<REPO>", "--agents", "claude", "agents", "--no-build", "--no-statusline", "--no-mcp"], "claude and agents without statusline or mcp");
  both(["init", "<REPO>", "--yes", "--no-build"], "detected agents");

  write(twins.ts.repo, { ".kiro/settings/mcp.json": "{ not json" });
  write(twins.go.repo, { ".kiro/settings/mcp.json": "{ not json" });
  both(["init", "<REPO>", "--agents", "kiro", "--no-build"], "unparseable MCP config is left alone");

  const plan = both(["uninstall", "<REPO>"], "uninstall dry run");
  assert.match(plan.stderr, /Dry run — nothing was touched/);
  assert.match(plan.stderr, /\[machine-wide\]/);
  both(["uninstall", "<REPO>", "--no-global", "--keep-cache"], "uninstall dry run without global");
  both(["uninstall", "<REPO>", "-y", "--keep-cache"], "uninstall");
  const gone = both(["uninstall", "<REPO>", "-y"], "uninstall again");
  assert.match(gone.stderr, /⚠ 1 file\(s\) could not be parsed/);
  assert.equal(existsSync(join(twins.go.repo, "graft")), false);
});
