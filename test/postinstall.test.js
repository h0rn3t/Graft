import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdirSync, mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { postinstall } from '../scripts/postinstall.mjs';

function writable(value) {
  return { write(text) { value += text; }, text: () => value };
}

test('postinstall prints the init nudge', () => {
  const root = mkdtempSync(join(tmpdir(), 'graft-postinstall-'));
  const output = writable('');
  assert.equal(postinstall({ env: { INIT_CWD: root }, cwd: root, output }), 0);
  assert.match(output.text(), /npx graft init/);
});

test('postinstall is silent in CI and in an initialized directory', () => {
  const root = mkdtempSync(join(tmpdir(), 'graft-postinstall-'));
  const ciOutput = writable('');
  postinstall({ env: { CI: '1', INIT_CWD: root }, cwd: root, output: ciOutput });
  assert.equal(ciOutput.text(), '');

  mkdirSync(join(root, '.claude', 'helpers'), { recursive: true });
  writeFileSync(join(root, '.claude', 'helpers', 'graft-statusline.cjs'), '// wired\n');
  const output = writable('');
  postinstall({ env: { INIT_CWD: root }, cwd: root, output });
  assert.equal(output.text(), '');
});
