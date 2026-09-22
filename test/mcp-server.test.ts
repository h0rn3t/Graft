import { test } from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync, spawn } from 'node:child_process';
import { existsSync, mkdirSync, mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { once } from 'node:events';
import { buildGraph } from '../src/graph/build.js';

async function rpc(messages: object[], dir: string, expected: number): Promise<any[]> {
  const child = spawn(process.execPath, ['--import', 'tsx', 'src/cli.ts', 'mcp', dir], { stdio: ['pipe', 'pipe', 'pipe'] });
  const responses: any[] = [];
  let buf = '';
  child.stdout.on('data', (d) => {
    buf += d.toString();
    let i;
    while ((i = buf.indexOf('\n')) !== -1) {
      const line = buf.slice(0, i).trim();
      buf = buf.slice(i + 1);
      if (line) responses.push(JSON.parse(line));
    }
  });
  for (const m of messages) child.stdin.write(`${JSON.stringify(m)}\n`);
  const deadline = Date.now() + 15000;
  while (responses.length < expected && Date.now() < deadline) await new Promise((r) => setTimeout(r, 50));
  child.kill();
  await once(child, 'exit').catch(() => {});
  return responses;
}

function builtGoCLI(): string {
  const dir = mkdtempSync(join(tmpdir(), 'graft-mcp-go-cli-'));
  const binary = join(dir, 'graft');
  execFileSync('go', ['build', '-o', binary, './cmd/graft'], { stdio: 'pipe' });
  return binary;
}

async function rpcGo(binary: string, messages: object[], dir: string, expected: number): Promise<any[]> {
  const child = spawn(binary, ['mcp', dir], { stdio: ['pipe', 'pipe', 'pipe'] });
  const responses: any[] = [];
  let buf = '';
  child.stdout.on('data', (d) => {
    buf += d.toString();
    let i;
    while ((i = buf.indexOf('\n')) !== -1) {
      const line = buf.slice(0, i).trim();
      buf = buf.slice(i + 1);
      if (line) responses.push(JSON.parse(line));
    }
  });
  for (const m of messages) child.stdin.write(`${JSON.stringify(m)}\n`);
  const deadline = Date.now() + 15000;
  while (responses.length < expected && Date.now() < deadline) await new Promise((r) => setTimeout(r, 50));
  child.kill();
  await once(child, 'exit').catch(() => {});
  return responses;
}

test('initialize → tools/list → tools/call round-trip', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'graft-mcpsrv-'));
  const rs = await rpc(
    [
      { jsonrpc: '2.0', id: 1, method: 'initialize', params: { protocolVersion: '2025-03-26', capabilities: {}, clientInfo: { name: 't', version: '0' } } },
      { jsonrpc: '2.0', method: 'notifications/initialized' },
      { jsonrpc: '2.0', id: 2, method: 'tools/list' },
      { jsonrpc: '2.0', id: 3, method: 'tools/call', params: { name: 'graft_trace_calls', arguments: { symbol: 'x.ts', depth: 2 } } },
    ],
    dir,
    3,
  );
  assert.equal(rs.length, 3);
  const init = rs.find((r) => r.id === 1);
  assert.equal(init.result.protocolVersion, '2025-03-26');
  assert.ok(init.result.capabilities.tools);
  assert.equal(init.result.serverInfo.name, 'graft');
  // This dir has no graph and no parent checkout, so the server advertises
  // nothing: graft is registered at the user MCP scope now (hosts/claude-global.ts),
  // which starts it in every project the user opens, and six tool schemas charged
  // to a repo that never asked for graft is context spent for answers it cannot
  // give. See `advertised` in src/mcp/server.ts.
  const list = rs.find((r) => r.id === 2);
  assert.deepEqual(list.result.tools, []);
  // Not advertised is not the same as not callable — a client that calls anyway
  // still gets the soft error that names the fix.
  const call = rs.find((r) => r.id === 3);
  assert.equal(call.result.isError, true); // unbuilt repo → soft error content
  assert.match(call.result.content[0].text, /graft build/);
});

const ALL_TOOLS = [
  'graft_find_code',
  'graft_file_api',
  'graft_check_freshness',
  'graft_trace_calls',
  'graft_find_all',
  'graft_repo_map',
];

async function listTools(dir: string): Promise<string[]> {
  const rs = await rpc(
    [
      { jsonrpc: '2.0', id: 1, method: 'initialize', params: { protocolVersion: '2025-03-26', capabilities: {}, clientInfo: { name: 't', version: '0' } } },
      { jsonrpc: '2.0', id: 2, method: 'tools/list' },
    ],
    dir,
    2,
  );
  return rs.find((r) => r.id === 2).result.tools.map((t: any) => t.name);
}

test('a built repo advertises every tool', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'graft-mcpbuilt-'));
  mkdirSync(join(dir, 'src'), { recursive: true });
  writeFileSync(join(dir, 'src', 'math.ts'), 'export function add(a: number, b: number) {\n  return a + b;\n}\n');
  await buildGraph(dir);

  assert.deepEqual(await listTools(dir), ALL_TOOLS);
});

test('a fresh worktree advertises every tool, on the strength of its parent', async () => {
  // The case the user-level registration exists for. `graft/` is gitignored, so
  // `git worktree add` never checks it out and this tree has no graph of its own —
  // it gets one from the parent on the first query (graph/seed.ts). Gating on this
  // tree alone would hide graft in exactly the worktree the user came to work in.
  const main = mkdtempSync(join(tmpdir(), 'graft-mcpwtmain-'));
  // Identity in the env, not the config: a CI runner has none, and blanking
  // GIT_CONFIG_GLOBAL removes any it had, so `git commit` would fail.
  const git = (...args: string[]): void =>
    execFileSync('git', args, {
      cwd: main,
      stdio: 'ignore',
      env: {
        ...process.env,
        GIT_CONFIG_GLOBAL: '/dev/null',
        GIT_CONFIG_SYSTEM: '/dev/null',
        GIT_AUTHOR_NAME: 'graft test',
        GIT_AUTHOR_EMAIL: 'test@example.invalid',
        GIT_COMMITTER_NAME: 'graft test',
        GIT_COMMITTER_EMAIL: 'test@example.invalid',
    },
    });
  git('init', '-b', 'main');
  mkdirSync(join(main, 'src'), { recursive: true });
  writeFileSync(join(main, 'src', 'math.ts'), 'export function add(a: number, b: number) {\n  return a + b;\n}\n');
  writeFileSync(join(main, '.gitignore'), 'graft/\n');
  git('add', '-A');
  git('commit', '-m', 'init');
  await buildGraph(main);

  const wt = join(mkdtempSync(join(tmpdir(), 'graft-mcpwt-')), 'feature');
  git('worktree', 'add', '--detach', wt, 'HEAD');
  assert.equal(existsSync(join(wt, 'graft')), false, 'the gitignored cache does not travel');

  assert.deepEqual(await listTools(wt), ALL_TOOLS);
});

test('initialize carries instructions — the layer that survives tool deferral', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'graft-mcpsrv-instr-'));
  const rs = await rpc(
    [{ jsonrpc: '2.0', id: 1, method: 'initialize', params: { protocolVersion: '2025-03-26', capabilities: {}, clientInfo: { name: 't', version: '0' } } }],
    dir,
    1,
  );
  const { instructions, serverInfo } = rs[0].result;
  assert.equal(typeof instructions, 'string');
  // A host that defers graft's schemas shows the model six bare names and nothing
  // else, so this string has to carry both the pitch and the recovery instruction.
  assert.match(instructions, /ONE lookup/, 'tells the agent to batch the schema fetch');
  assert.match(instructions, /select:mcp__graft__graft_find_code,/, 'gives a copy-pasteable query');
  for (const t of ['graft_find_code', 'graft_find_all', 'graft_trace_calls', 'graft_file_api', 'graft_repo_map']) {
    assert.ok(instructions.includes(t), `names ${t}`);
  }
  // Observed sibling servers sit at 660–984 chars; nothing proves a longer one
  // survives un-truncated, so hold the line here rather than discover it later.
  assert.ok(instructions.length < 1000, `instructions must stay under 1000 chars, got ${instructions.length}`);
  assert.match(serverInfo.version, /^\d+\.\d+\.\d+$/, 'real version, not the old hardcoded 0');
});

test('unknown method returns -32601', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'graft-mcpsrv2-'));
  const rs = await rpc([{ jsonrpc: '2.0', id: 9, method: 'resources/list' }], dir, 1);
  assert.equal(rs[0].error.code, -32601);
});

test('Go MCP retrieval server matches TypeScript tool contracts', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'graft-mcp-parity-'));
  mkdirSync(join(dir, 'src'), { recursive: true });
  writeFileSync(
    join(dir, 'src', 'math.ts'),
    'export function add(a: number, b: number): number {\n  return a + b;\n}\n' +
      'export function sub(a: number, b: number): number {\n  return add(a, -b);\n}\n',
  );
  execFileSync(process.execPath, ['--import', 'tsx', 'src/cli.ts', 'build', dir], { stdio: 'pipe' });
  const binary = builtGoCLI();
  const messages = [
    { jsonrpc: '2.0', id: 1, method: 'initialize', params: { protocolVersion: '2025-03-26' } },
    { jsonrpc: '2.0', id: 2, method: 'tools/list' },
    { jsonrpc: '2.0', id: 3, method: 'tools/call', params: { name: 'graft_find_code', arguments: { query: 'add numbers' } } },
    { jsonrpc: '2.0', id: 4, method: 'tools/call', params: { name: 'graft_trace_calls', arguments: { symbol: 'add' } } },
    { jsonrpc: '2.0', id: 5, method: 'tools/call', params: { name: 'graft_find_all', arguments: { pattern: 'add' } } },
    { jsonrpc: '2.0', id: 6, method: 'tools/call', params: { name: 'graft_file_api', arguments: { file: 'src/math.ts' } } },
    { jsonrpc: '2.0', id: 7, method: 'tools/call', params: { name: 'graft_repo_map', arguments: {} } },
    { jsonrpc: '2.0', id: 8, method: 'tools/call', params: { name: 'graft_ask', arguments: { query: 'add numbers' } } },
    { jsonrpc: '2.0', id: 9, method: 'tools/call', params: { name: 'graft_check_freshness', arguments: {} } },
  ];
  const typescript = await rpc(messages, dir, 9);
  // Prime the native extractor's one-time fingerprint before comparing steady-state tool output.
  await rpcGo(binary, [
    { jsonrpc: '2.0', id: 0, method: 'tools/call', params: { name: 'graft_repo_map', arguments: {} } },
  ], dir, 1);
  const go = await rpcGo(binary, messages, dir, 9);
  const byID = (responses: any[]) => new Map(responses.map((response) => [response.id, response]));
  const tsByID = byID(typescript);
  const goByID = byID(go);

  assert.equal(go.length, typescript.length);
  assert.deepEqual(
    goByID.get(2)?.result.tools.map((tool: { name: string }) => tool.name),
    tsByID.get(2)?.result.tools.map((tool: { name: string }) => tool.name),
  );
  assert.equal(goByID.get(1)?.result.protocolVersion, tsByID.get(1)?.result.protocolVersion);
  assert.equal(goByID.get(1)?.result.serverInfo.name, 'graft');
  assert.match(goByID.get(1)?.result.serverInfo.version ?? '', /^\d+\.\d+\.\d+$/);
  for (const id of [3, 4, 5, 6, 7, 8, 9]) {
    assert.equal(goByID.get(id)?.result.isError, tsByID.get(id)?.result.isError, `isError mismatch for ${id}`);
    assert.equal(
      goByID.get(id)?.result.content[0]?.text,
      tsByID.get(id)?.result.content[0]?.text,
      `text mismatch for ${id}`,
    );
  }
});

test('Go MCP refreshes workspace child graphs like TypeScript', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'graft-mcp-workspace-refresh-'));
  mkdirSync(join(dir, 'graft'), { recursive: true });
  writeFileSync(join(dir, 'graft', 'workspace.json'), '{"version":1,"children":["api","web"]}\n');
  for (const child of ['api', 'web']) {
    const childDir = join(dir, child);
    mkdirSync(join(childDir, 'src'), { recursive: true });
    writeFileSync(join(childDir, 'src', 'app.ts'), 'export function before() {}\n');
    await buildGraph(childDir);
  }

  const binary = builtGoCLI();
  const call = {
    jsonrpc: '2.0', id: 1, method: 'tools/call',
    params: { name: 'graft_repo_map', arguments: { max_dirs: 2 } },
  };
  const primed = await rpcGo(binary, [call], dir, 1);
  assert.equal(primed[0]?.result.isError, false, 'native build primes each child fingerprint');

  for (const child of ['api', 'web']) {
    writeFileSync(join(dir, child, 'src', 'app.ts'), 'export function refreshed() {}\n');
  }
  const typescript = await rpc([call], dir, 1);
  const go = await rpcGo(binary, [call], dir, 1);
  assert.equal(go.length, 1);
  assert.equal(go[0]?.result.isError, typescript[0]?.result.isError, 'isError matches for workspace refresh');
  assert.equal(go[0]?.result.content[0]?.text, typescript[0]?.result.content[0]?.text, 'workspace refresh output matches');
});

test('Go MCP federates workspace tools like TypeScript', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'graft-mcp-workspace-tools-'));
  mkdirSync(join(dir, 'graft'), { recursive: true });
  writeFileSync(join(dir, 'graft', 'workspace.json'), '{"version":1,"children":["api","missing","web"]}\n');
  for (const child of ['api', 'web']) {
    const childDir = join(dir, child);
    mkdirSync(join(childDir, 'src'), { recursive: true });
    writeFileSync(
      join(childDir, 'src', 'app.ts'),
      'export function root() {\n  return leaf();\n}\nexport function leaf() {\n  return 1;\n}\n',
    );
    await buildGraph(childDir);
  }

  const binary = builtGoCLI();
  await rpcGo(
    binary,
    [{ jsonrpc: '2.0', id: 0, method: 'tools/call', params: { name: 'graft_repo_map', arguments: {} } }],
    dir,
    1,
  );
  const messages = [
    { jsonrpc: '2.0', id: 1, method: 'tools/call', params: { name: 'graft_find_code', arguments: { query: 'leaf' } } },
    { jsonrpc: '2.0', id: 2, method: 'tools/call', params: { name: 'graft_trace_calls', arguments: { symbol: 'leaf' } } },
    { jsonrpc: '2.0', id: 3, method: 'tools/call', params: { name: 'graft_find_all', arguments: { pattern: 'return' } } },
    { jsonrpc: '2.0', id: 4, method: 'tools/call', params: { name: 'graft_repo_map', arguments: { max_dirs: 2 } } },
    { jsonrpc: '2.0', id: 5, method: 'tools/call', params: { name: 'graft_check_freshness', arguments: {} } },
  ];
  const typescript = await rpc(messages, dir, messages.length);
  const go = await rpcGo(binary, messages, dir, messages.length);
  const byID = (responses: any[]) => new Map(responses.map((response) => [response.id, response]));
  const tsByID = byID(typescript);
  const goByID = byID(go);
  assert.equal(go.length, typescript.length);
  for (const id of [1, 2, 3, 4, 5]) {
    assert.equal(goByID.get(id)?.result.isError, tsByID.get(id)?.result.isError, `workspace isError mismatch for ${id}`);
    assert.equal(
      goByID.get(id)?.result.content[0]?.text,
      tsByID.get(id)?.result.content[0]?.text,
      `workspace text mismatch for ${id}`,
    );
  }
});

test('Go MCP preserves legacy aliases and soft error contracts', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'graft-mcp-errors-'));
  mkdirSync(join(dir, 'src'), { recursive: true });
  writeFileSync(join(dir, 'src', 'math.ts'), 'export function add(a: number, b: number) { return a + b; }\n');
  execFileSync(process.execPath, ['--import', 'tsx', 'src/cli.ts', 'build', dir], { stdio: 'pipe' });
  const binary = builtGoCLI();
  const builtMessages = [
    { jsonrpc: '2.0', id: 1, method: 'tools/call', params: { name: 'graft_ask', arguments: { query: 'add' } } },
    { jsonrpc: '2.0', id: 2, method: 'tools/call', params: { name: 'nope', arguments: {} } },
  ];
  const tsBuilt = new Map((await rpc(builtMessages, dir, 2)).map((response) => [response.id, response]));
  await rpcGo(binary, [
    { jsonrpc: '2.0', id: 0, method: 'tools/call', params: { name: 'graft_repo_map', arguments: {} } },
  ], dir, 1);
  const goBuilt = new Map((await rpcGo(binary, builtMessages, dir, 2)).map((response) => [response.id, response]));
  for (const id of [1, 2]) {
    assert.equal(goBuilt.get(id)?.result.isError, tsBuilt.get(id)?.result.isError, `built isError mismatch for ${id}`);
    assert.equal(goBuilt.get(id)?.result.content[0]?.text, tsBuilt.get(id)?.result.content[0]?.text, `built text mismatch for ${id}`);
  }

  const bare = mkdtempSync(join(tmpdir(), 'graft-mcp-errors-bare-'));
  const bareMessages = [
    { jsonrpc: '2.0', id: 1, method: 'tools/list' },
    { jsonrpc: '2.0', id: 2, method: 'tools/call', params: { name: 'graft_trace_calls', arguments: { symbol: 'add' } } },
    { jsonrpc: '2.0', id: 3, method: 'tools/call', params: { name: 'nope', arguments: {} } },
  ];
  const tsBare = new Map((await rpc(bareMessages, bare, 3)).map((response) => [response.id, response]));
  const goBare = new Map((await rpcGo(binary, bareMessages, bare, 3)).map((response) => [response.id, response]));
  assert.deepEqual(goBare.get(1)?.result.tools, tsBare.get(1)?.result.tools);
  for (const id of [2, 3]) {
    assert.equal(goBare.get(id)?.result.isError, tsBare.get(id)?.result.isError, `bare isError mismatch for ${id}`);
    assert.equal(goBare.get(id)?.result.content[0]?.text, tsBare.get(id)?.result.content[0]?.text, `bare text mismatch for ${id}`);
  }
});
