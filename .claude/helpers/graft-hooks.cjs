#!/usr/bin/env node
const path = require('path');
const fs = require('fs');
const { spawnSync } = require('child_process');
const dir = process.env.CLAUDE_PROJECT_DIR || process.cwd();
const BAKED = "/Users/eugeneshershen/go/pkg/mod/github.com/h0rn3t/!graft@v0.1.2-0.20260924131236-2fd47debe7b4";

function fromPkg(base) {
  try {
    const pkg = require.resolve('@nanonets/graft/package.json', { paths: [base] });
    return path.dirname(pkg);
  } catch { return null; }
}

function globalRoot() {
  try {
    const root = require('child_process').execFileSync('npm', ['root', '-g'], {
      encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore'], shell: process.platform === 'win32',
    }).trim();
    return root || null;
  } catch { return null; }
}

function versionOf(pkg) {
  try { return JSON.parse(fs.readFileSync(path.join(pkg, 'package.json'), 'utf8')).version || null; }
  catch { return null; }
}

function newer(a, b) {
  if (!a) return false;
  if (!b) return true;
  const parts = (v) => String(v).split('-')[0].split('.').map((n) => Number(n) || 0);
  const left = parts(a), right = parts(b);
  for (let i = 0; i < Math.max(left.length, right.length); i++) {
    const delta = (left[i] || 0) - (right[i] || 0);
    if (delta !== 0) return delta > 0;
  }
  return false;
}

function binaryName() {
  return `graft-${process.platform}-${process.arch}${process.platform === 'win32' ? '.exe' : ''}`;
}

function best(pkgs) {
  const name = binaryName();
  let selected = null, selectedVersion = null;
  for (const pkg of pkgs) {
    if (!pkg || !fs.existsSync(path.join(pkg, 'bin', name))) continue;
    const version = versionOf(pkg);
    if (selected === null || newer(version, selectedVersion)) {
      selected = pkg;
      selectedVersion = version;
    }
  }
  return selected && path.join(selected, 'bin', name);
}

function entry() {
  const cheap = [BAKED, fromPkg(dir), fromPkg(path.join(path.dirname(process.execPath), '..', 'lib'))];
  const hit = best(cheap);
  if (hit) return hit;
  const root = globalRoot();
  const global = root && path.join(root, '@nanonets', 'graft');
  return global && fs.existsSync(path.join(global, 'bin', binaryName()))
    ? path.join(global, 'bin', binaryName())
    : null;
}

const binary = entry();
if (binary) {
  const result = spawnSync(binary, ['_hook', process.argv[2]], { stdio: 'inherit' });
  if (result.error) process.exit(0);
  process.exit(result.status ?? 1);
}
