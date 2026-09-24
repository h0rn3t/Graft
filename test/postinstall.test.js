import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdirSync, mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { nativeBinaryName } from '../bin/graft.js';
import { postinstall } from '../scripts/postinstall.mjs';

function writable(value) {
  return { write(text) { value += text; }, text: () => value };
}

test('postinstall calls the packaged native binary and prints the init nudge', () => {
  const root = mkdtempSync(join(tmpdir(), 'graft-postinstall-'));
  const binary = join(root, 'bin', nativeBinaryName());
  mkdirSync(join(root, 'bin'), { recursive: true });
  writeFileSync(binary, 'binary');
  let call;
  const output = writable('');
  assert.equal(postinstall({
    env: { INIT_CWD: root },
    cwd: root,
    output,
    root,
    spawn(command, args, options) { call = { command, args, options }; },
  }), 0);
  assert.deepEqual(call, { command: binary, args: ['_install'], options: { stdio: 'ignore', windowsHide: true } });
  assert.match(output.text(), /npx graft init/);
});

test('postinstall is silent in CI and in an initialized directory', () => {
  const root = mkdtempSync(join(tmpdir(), 'graft-postinstall-'));
  const ciOutput = writable('');
  postinstall({ env: { CI: '1', INIT_CWD: root }, cwd: root, output: ciOutput, root });
  assert.equal(ciOutput.text(), '');

  mkdirSync(join(root, '.claude', 'helpers'), { recursive: true });
  writeFileSync(join(root, '.claude', 'helpers', 'graft-statusline.cjs'), '// wired\n');
  const output = writable('');
  postinstall({ env: { INIT_CWD: root }, cwd: root, output, root });
  assert.equal(output.text(), '');
});

test('postinstall never fails when the native binary is missing or exits nonzero', () => {
  const root = mkdtempSync(join(tmpdir(), 'graft-postinstall-'));
  const output = writable('');
  assert.equal(postinstall({ env: { INIT_CWD: root }, cwd: root, output, root }), 0);
  assert.match(output.text(), /npx graft init/);

  const binary = join(root, 'bin', nativeBinaryName());
  mkdirSync(join(root, 'bin'), { recursive: true });
  writeFileSync(binary, 'binary');
  assert.equal(postinstall({
    env: { INIT_CWD: root },
    cwd: root,
    output: writable(''),
    root,
    spawn() { throw new Error('cannot spawn'); },
  }), 0);
});
