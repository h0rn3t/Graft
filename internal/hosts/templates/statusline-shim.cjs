#!/usr/bin/env node
const fs = require('fs');
const { spawnSync } = require('child_process');
const BAKED = "@@BAKED@@";

// The graft binary that wrote this shim, else the first graft on PATH. A
// missing binary exits 0 so the host never sees a failing hook.
const binary = BAKED && fs.existsSync(BAKED) ? BAKED : 'graft';
const result = spawnSync(binary, ['_statusline'], { stdio: 'inherit' });
if (result.error) process.exit(0);
process.exit(result.status ?? 1);
