// Prints a one-line nudge after install, and records the anonymous `install`
// event through the packaged native CLI. Never fails the install.
import { existsSync, realpathSync } from 'node:fs';
import { spawnSync } from 'node:child_process';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { nativeBinaryName } from '../bin/graft.js';

const packageRoot = dirname(dirname(fileURLToPath(import.meta.url)));

export function postinstall({ env = process.env, cwd = process.cwd(), output = process.stdout, root = packageRoot, spawn = spawnSync } = {}) {
  if (env.CI) return 0;

  const binary = join(root, 'bin', nativeBinaryName());
  if (existsSync(binary)) {
    try {
      spawn(binary, ['_install'], { stdio: 'ignore', windowsHide: true });
    } catch {
      // A telemetry estimate must never fail npm's install lifecycle.
    }
  }

  const dir = env.INIT_CWD || cwd;
  if (existsSync(join(dir, '.claude', 'helpers', 'graft-statusline.cjs'))) return 0;
  output.write('\n  Graft installed. Run `npx graft init` to enable the Claude Code integration (statusline + hooks + auto-sync).\n');
  return 0;
}

if (process.argv[1] && realpathSync(process.argv[1]) === realpathSync(fileURLToPath(import.meta.url))) {
  try {
    postinstall();
  } catch {
    // Never fail an install.
  }
}
