/**
 * CLI tests for `graft callers` and its `--direction`/`--depth` flags — the one
 * command that wires src/graph/traverse.ts's pure resolver + edge-walkers into
 * the `graft` binary (`--direction out` is the old `callees`; `--depth N` is the
 * old `impact`). Runs the real CLI via execFileSync (same pattern as
 * test/mcp-tools.test.ts's `builtRepo` helper) against a built fixture repo,
 * so these tests exercise the actual process boundary: exit codes, stdout vs
 * stderr, and --json shape.
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { execFileSync, spawnSync } from 'node:child_process';
import { captureGolden, snapshotGoldenFiles } from './goldens.js';

function testHome(root: string): string {
  const home = join(root, 'home');
  mkdirSync(join(home, '.graft'), { recursive: true });
  writeFileSync(join(home, '.graft', 'update-check.json'), JSON.stringify({ latest: '0.0.0', checkedAt: 9_999_999_999_999 }, null, 2));
  return home;
}

function builtRepo(): string {
  const d = mkdtempSync(join(tmpdir(), 'graft-traversecli-'));
  mkdirSync(join(d, 'src'), { recursive: true });
  writeFileSync(
    join(d, 'src', 'math.ts'),
    'export function add(a: number, b: number): number {\n  return a + b;\n}\n' +
      'export function sub(a: number, b: number): number {\n  return add(a, -b);\n}\n' +
      'export function compute(a: number, b: number): number {\n  return sub(a, b);\n}\n',
  );
  execFileSync(process.execPath, ['--import', 'tsx', 'src/cli.ts', 'build', d], { stdio: 'pipe' });
  return d;
}

function runCli(args: string[], env: NodeJS.ProcessEnv = process.env): { stdout: string; stderr: string; status: number } {
  const result = spawnSync(process.execPath, ['--import', 'tsx', 'src/cli.ts', ...args], { encoding: 'utf8', env });
  return { stdout: result.stdout, stderr: result.stderr, status: result.status ?? 1 };
}

function builtGoCli(): string {
  const d = mkdtempSync(join(tmpdir(), 'graft-go-cli-'));
  const binary = join(d, 'graft');
  execFileSync('go', ['build', '-o', binary, './cmd/graft'], { stdio: 'pipe' });
  return binary;
}

function runGoCli(binary: string, args: string[], env: NodeJS.ProcessEnv = process.env): { stdout: string; stderr: string; status: number } {
  const result = spawnSync(binary, args, { cwd: process.cwd(), encoding: 'utf8', env });
  return { stdout: result.stdout, stderr: result.stderr, status: result.status ?? 1 };
}

test('graft callers: happy path shows header and the caller hit', () => {
  const d = builtRepo();
  const r = runCli(['callers', 'add', d]);
  assert.equal(r.status, 0);
  assert.match(r.stdout, /add · function · src\/math\.ts:/);
  assert.match(r.stdout, /calls ← sub \(src\/math\.ts:/);
});

test('graft callers --json: shape matches {query, matches:[{symbol,hits}]}', () => {
  const d = builtRepo();
  const r = runCli(['callers', 'add', d, '--json']);
  assert.equal(r.status, 0);
  const parsed = JSON.parse(r.stdout);
  assert.equal(parsed.query, 'add');
  assert.equal(parsed.matches.length, 1);
  const m = parsed.matches[0];
  assert.equal(m.symbol.name, 'add');
  assert.equal(m.symbol.kind, 'function');
  assert.ok(m.symbol.path.endsWith('math.ts'));
  assert.ok(m.symbol.id);
  assert.ok(m.symbol.span);
  assert.equal(m.hits.length, 1);
  assert.equal(m.hits[0].name, 'sub');
  assert.equal(m.hits[0].relation, 'calls');
  assert.equal(m.hits[0].depth, 1);
});

test('graft callers: unknown symbol exits 1 with a stderr message', () => {
  const d = builtRepo();
  const r = runCli(['callers', 'noSuchSymbolAnywhere', d]);
  assert.equal(r.status, 1);
  assert.match(r.stderr, /no symbol "noSuchSymbolAnywhere" in the graph/);
  assert.match(r.stderr, /graft build/);
  assert.equal(r.stdout, '');
});

test('graft callers --direction out: happy path shows the outgoing (callee) hit', () => {
  const d = builtRepo();
  // `sub` calls `add`, so its outgoing edge points at add with a `→` arrow.
  const r = runCli(['callers', 'sub', d, '--direction', 'out']);
  assert.equal(r.status, 0);
  assert.match(r.stdout, /sub · function · src\/math\.ts:/);
  assert.match(r.stdout, /calls → add \(src\/math\.ts:/);
});

test('graft callers --direction out: zero-edge symbol prints a loud callees note and still exits 0', () => {
  const d = builtRepo();
  // `add` calls nothing, so its callees are empty — must not be a silent list.
  const r = runCli(['callers', 'add', d, '--direction', 'out']);
  assert.equal(r.status, 0);
  assert.match(r.stdout, /add · function · src\/math\.ts:/);
  assert.match(r.stdout, /no indexed callees/);
  assert.match(r.stdout, /graft grep "add"/);
});

test('graft callers --direction out --json: zero-edge symbol includes a note field', () => {
  const d = builtRepo();
  const r = runCli(['callers', 'add', d, '--direction', 'out', '--json']);
  assert.equal(r.status, 0);
  const parsed = JSON.parse(r.stdout);
  assert.equal(parsed.query, 'add');
  assert.equal(parsed.matches.length, 1);
  const m = parsed.matches[0];
  assert.equal(m.symbol.name, 'add');
  assert.equal(m.hits.length, 0);
  assert.ok(m.note, 'zero-edge match must have a note field');
  assert.match(m.note, /graft grep "add"/);
});

function ambiguousRepo(): string {
  const d = mkdtempSync(join(tmpdir(), 'graft-traversecli-ambiguous-'));
  mkdirSync(join(d, 'src'), { recursive: true });
  writeFileSync(join(d, 'src', 'a.ts'), 'export function shared(): number {\n  return 1;\n}\n');
  writeFileSync(join(d, 'src', 'b.ts'), 'export function shared(): number {\n  return 2;\n}\n');
  // A cross-file call to the ambiguous name — resolve.ts drops it rather than
  // guessing which `shared` it means, so NEITHER definition gets a caller edge.
  writeFileSync(join(d, 'src', 'user.ts'), 'import { shared } from "./a.js";\nexport function use(): number {\n  return shared();\n}\n');
  execFileSync(process.execPath, ['--import', 'tsx', 'src/cli.ts', 'build', d], { stdio: 'pipe' });
  return d;
}

test('A6: an ambiguous name (2 definitions) states the candidate count in the zero-hit note', () => {
  const d = ambiguousRepo();
  const r = runCli(['callers', 'shared', d]);
  assert.equal(r.status, 0);
  // Both candidates are reported (resolveSymbol returns every match).
  assert.equal((r.stdout.match(/shared · function · src\//g) ?? []).length, 2);
  // Each zero-hit block states 2 definitions share the name.
  assert.equal((r.stdout.match(/2 definitions share the name/g) ?? []).length, 2);
  assert.match(r.stdout, /dropped rather than guessed/);
});

test('A6 --json: the ambiguous-name note includes the candidate count', () => {
  const d = ambiguousRepo();
  const r = runCli(['callers', 'shared', d, '--json']);
  assert.equal(r.status, 0);
  const parsed = JSON.parse(r.stdout);
  assert.equal(parsed.matches.length, 2);
  for (const m of parsed.matches) {
    assert.equal(m.hits.length, 0, 'the ambiguity drop leaves no caller edges for either candidate');
    assert.match(m.note, /2 definitions share the name/);
    assert.match(m.note, /dropped rather than guessed/);
  }
});

test('graft callers --depth: depth flag walks the BFS transitively (blast radius)', () => {
  const d = builtRepo();
  // compute -> sub -> add: callers of `add` at depth 1 is just `sub`;
  // depth 2 also reaches `compute` and tags each hit with its depth.
  const shallow = runCli(['callers', 'add', d, '--depth', '1']);
  assert.equal(shallow.status, 0);
  assert.match(shallow.stdout, /← sub \(/);
  assert.doesNotMatch(shallow.stdout, /compute/);
  assert.doesNotMatch(shallow.stdout, /\[depth/); // depth 1 → no depth tags

  const deeper = runCli(['callers', 'add', d, '--depth', '2']);
  assert.equal(deeper.status, 0);
  assert.match(deeper.stdout, /← sub \(/);
  assert.match(deeper.stdout, /\[depth 1\]/);
  assert.match(deeper.stdout, /← compute \(/);
  assert.match(deeper.stdout, /\[depth 2\]/);
});

test('graft callers --depth all: walks the entire connected closure', () => {
  const d = builtRepo();
  // compute -> sub -> add. `all` must reach BOTH hops (the full closure),
  // like an unbounded depth, terminating when no new node is found.
  const all = runCli(['callers', 'add', d, '--depth', 'all']);
  assert.equal(all.status, 0);
  assert.match(all.stdout, /← sub \(/);
  assert.match(all.stdout, /\[depth 1\]/);
  assert.match(all.stdout, /← compute \(/);
  assert.match(all.stdout, /\[depth 2\]/);
});

test('graft callers --depth: rejects a non-numeric, non-"all" value with exit 1', () => {
  const d = builtRepo();
  const r = runCli(['callers', 'add', d, '--depth', 'banana']);
  assert.equal(r.status, 1);
  assert.match(r.stderr, /--depth must be a positive number or "all"/);
});

test('graft callers --direction: rejects a bad value with exit 1', () => {
  const d = builtRepo();
  const r = runCli(['callers', 'add', d, '--direction', 'sideways']);
  assert.equal(r.status, 1);
  assert.match(r.stderr, /--direction must be "in" or "out"/);
});

test('graft callers: no graph at all is a stderr error, exit 1', () => {
  const bare = mkdtempSync(join(tmpdir(), 'graft-traversecli-bare-'));
  const r = runCli(['callers', 'add', bare]);
  assert.equal(r.status, 1);
  assert.match(r.stderr, /graft build/);
});

test('graft callers: quotes the call site, and only where it is the right line', () => {
  const d = builtRepo();
  const r = runCli(['callers', 'add', d]);
  assert.equal(r.status, 0);
  // `sub` calls `add` on line 5 of the fixture. The edge is a claim; this is the
  // evidence, and it saves opening the file to check.
  assert.match(r.stdout, /calls ← sub \(src\/math\.ts:[^)]*\)\n\s+5: return add\(a, -b\);/);

  // A second-hop hit references what is BETWEEN it and the symbol, not the symbol
  // itself, so quoting it would point at the wrong line.
  const deep = runCli(['callers', 'add', d, '--depth', '2']);
  assert.match(deep.stdout, /calls ← compute \(src\/math\.ts:[^)]*\) \[depth 2\]\n/);
  assert.ok(!/\[depth 2\]\n\s+\d+:/.test(deep.stdout), 'no quote on a second-hop hit');

  // --json is a data contract: the quote is a text-output nicety and must stay out.
  const json = JSON.parse(runCli(['callers', 'add', d, '--json']).stdout);
  assert.ok(!JSON.stringify(json).includes('return add(a, -b)'));
});

test('Go callers CLI matches TypeScript JSON, exit codes, and diagnostics', () => {
  const d = builtRepo();
  const binary = builtGoCli();
  const cases = [
    ['callers', 'add', d, '--json'],
    ['callers', 'add', d, '--depth', '2', '--json'],
    ['callers', 'missingSymbol', d],
    ['callers', 'add', d, '--direction', 'sideways'],
    ['callers', 'add', d, '--depth', 'banana'],
    ['callers', 'add', mkdtempSync(join(tmpdir(), 'graft-go-cli-missing-'))],
  ];

  for (const [index, args] of cases.entries()) {
    const root = args[2];
    const home = testHome(root);
    const inputs = snapshotGoldenFiles(root);
    const env = { ...process.env, HOME: home, USERPROFILE: home, DO_NOT_TRACK: '1', GRAFT_NO_REFRESH: '1', COLUMNS: '80' };
    for (const name of ['GRAFT_DIR', 'GRAFT_BRAIN_TOKEN', 'GRAFT_BRAIN_ID', 'CI', 'GITHUB_ACTIONS']) delete env[name];
    const typescript = runCli(args, env);
    const go = runGoCli(binary, args, env);
    if (process.env.GRAFT_CAPTURE_GOLDENS === '1') {
      captureGolden(`per-command/graph-traverse-cli/${String(index + 1).padStart(3, '0')}`, {
        args,
        env: { DO_NOT_TRACK: '1', GRAFT_NO_REFRESH: '1', COLUMNS: '80' },
        inputs,
        status: typescript.status,
        stdout: typescript.stdout,
        stderr: typescript.stderr,
        files: {},
        normalize: { [root]: '<REPO>', [home]: '<HOME>' },
      });
    }
    assert.equal(go.status, typescript.status, `status mismatch for ${args.join(' ')}`);
    assert.equal(go.stderr, typescript.stderr, `stderr mismatch for ${args.join(' ')}`);
    if (go.status === 0) {
      assert.deepEqual(JSON.parse(go.stdout), JSON.parse(typescript.stdout), `JSON mismatch for ${args.join(' ')}`);
    } else {
      assert.equal(go.stdout, typescript.stdout, `stdout mismatch for ${args.join(' ')}`);
    }
  }
});
