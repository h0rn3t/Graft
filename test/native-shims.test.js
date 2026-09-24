import { test } from 'node:test';
import assert from 'node:assert/strict';
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const root = dirname(dirname(fileURLToPath(import.meta.url)));
const binaryName = `graft-${process.platform}-${process.arch}${process.platform === 'win32' ? '.exe' : ''}`;

function fixture() {
  return mkdtempSync(join(tmpdir(), 'graft-native-shim-'));
}

function template(name, baked) {
  const source = readFileSync(join(root, 'internal', 'hosts', 'templates', name), 'utf8');
  return source.replace('@@BAKED@@', () => baked.replaceAll('\\', '\\\\').replaceAll('"', '\\"'));
}

function fakePackage(pkg, version, body) {
  mkdirSync(join(pkg, 'bin'), { recursive: true });
  writeFileSync(join(pkg, 'package.json'), JSON.stringify({ name: '@nanonets/graft', version }));
  const binary = join(pkg, 'bin', binaryName);
  writeFileSync(binary, `#!/usr/bin/env node\n${body}\n`);
  chmodSync(binary, 0o755);
  return pkg;
}

function isolate(base) {
  const preload = join(base, 'isolate.cjs');
  const execPath = join(base, 'node-prefix', 'bin', 'node');
  const npmPrefix = join(base, 'npm-prefix');
  mkdirSync(npmPrefix, { recursive: true });
  writeFileSync(preload, `Object.defineProperty(process, 'execPath', { value: ${JSON.stringify(execPath)}, configurable: true });\n`);
  return { preload, npm_config_prefix: npmPrefix };
}

function run(base, name, baked, project, args, extraEnv = {}, stdin = '') {
  const shim = join(base, name);
  writeFileSync(shim, template(name, baked));
  const env = isolate(base);
  return spawnSync(process.execPath, ['--require', env.preload, shim, ...args], {
    cwd: project,
    encoding: 'utf8',
    input: stdin,
    env: { ...process.env, ...env, ...extraEnv, CLAUDE_PROJECT_DIR: project },
  });
}

const recorder = `
const fs = require('node:fs');
fs.writeFileSync(process.env.MARKER, JSON.stringify({ args: process.argv.slice(2), stdin: fs.readFileSync(0, 'utf8') }));
process.stdout.write('native-out');
process.stderr.write('native-err');
process.exit(Number(process.env.NATIVE_EXIT || 0));
`;

for (const [name, args] of [['hooks-shim.cjs', ['prompt']], ['statusline-shim.cjs', []]]) {
  test(`${name} forwards stdio and exit status from the newest package`, () => {
    const base = fixture();
    const stale = fakePackage(join(base, 'stale'), '0.9.0', recorder);
    const project = join(base, 'project');
    const current = fakePackage(join(project, 'node_modules', '@nanonets', 'graft'), '0.11.0', recorder);
    const marker = join(base, 'marker.json');
    const result = run(base, name, stale, project, args, { MARKER: marker }, 'payload');
    assert.equal(result.status, 0, result.stderr);
    assert.equal(result.stdout, 'native-out');
    assert.equal(result.stderr, 'native-err');
    assert.deepEqual(JSON.parse(readFileSync(marker, 'utf8')), { args: name.startsWith('hooks') ? ['_hook', 'prompt'] : ['_statusline'], stdin: 'payload' });
    assert.ok(existsSync(join(current, 'bin', binaryName)));
  });

  test(`${name} passes a non-zero native exit status`, () => {
    const base = fixture();
    const pkg = fakePackage(join(base, 'only'), '1.0.0', recorder);
    const project = join(base, 'project');
    mkdirSync(project, { recursive: true });
    const result = run(base, name, pkg, project, args, { NATIVE_EXIT: '23', MARKER: join(base, 'unused') });
    assert.equal(result.status, 23);
  });
}

test('missing native binary exits zero and silently', () => {
  for (const name of ['hooks-shim.cjs', 'statusline-shim.cjs']) {
    const base = fixture();
    const project = join(base, 'project');
    mkdirSync(project, { recursive: true });
    const shim = join(base, name);
    writeFileSync(shim, template(name, join(base, 'missing')));
    const isolated = isolate(base);
    const result = spawnSync(process.execPath, ['--require', isolated.preload, shim], {
      cwd: project,
      encoding: 'utf8',
      env: { ...process.env, ...isolated, CLAUDE_PROJECT_DIR: project },
    });
    assert.equal(result.status, 0, result.stderr);
    assert.equal(result.stdout, '');
    assert.equal(result.stderr, '');
  }
});
