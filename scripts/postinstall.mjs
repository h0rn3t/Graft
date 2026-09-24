// Prints a one-line nudge after install. Never fails the install.
import { existsSync, realpathSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

export function postinstall({ env = process.env, cwd = process.cwd(), output = process.stdout } = {}) {
  if (env.CI) return 0;

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
