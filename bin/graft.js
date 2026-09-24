#!/usr/bin/env node
/**
 * The published `graft` entrypoint.
 *
 * Every command runs on the native Go binary shipped next to this file
 * (`bin/graft-<platform>-<arch>`). A platform without a packaged binary is
 * unsupported and exits with one clear error.
 */
import { spawn } from "node:child_process";
import { existsSync, realpathSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

/** File name of the Go binary built for this platform by scripts/build-go.mjs. */
export function nativeBinaryName(platform = process.platform, arch = process.arch) {
  return `graft-${platform}-${arch}${platform === "win32" ? ".exe" : ""}`;
}

function runNative(binary, args) {
  const child = spawn(binary, args, { stdio: "inherit" });
  // A host stopping an MCP server signals this process, not the child, so pass
  // signals on, then stay alive until the child exits and mirror how it ended.
  for (const signal of ["SIGINT", "SIGTERM", "SIGHUP"]) process.on(signal, () => child.kill(signal));
  child.on("error", (err) => {
    console.error(`✗ could not start ${binary}: ${err.message}`);
    process.exit(1);
  });
  child.on("exit", (code, signal) => {
    if (signal) {
      process.removeAllListeners(signal);
      process.kill(process.pid, signal);
      return;
    }
    process.exit(code ?? 1);
  });
}

function main() {
  const here = dirname(fileURLToPath(import.meta.url));
  const binary = join(here, nativeBinaryName());
  if (!existsSync(binary)) {
    console.error(`✗ graft has no native binary for ${process.platform}-${process.arch}`);
    process.exit(1);
  }
  runNative(binary, process.argv.slice(2));
}

// npm links this file into PATH, so compare real paths; importing it (tests)
// must not start a CLI.
if (process.argv[1] && realpathSync(process.argv[1]) === realpathSync(fileURLToPath(import.meta.url))) main();
