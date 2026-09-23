#!/usr/bin/env node
/**
 * The published `graft` entrypoint.
 *
 * Every command runs on the native Go binary shipped next to this file
 * (`bin/graft-<platform>-<arch>`), except the three invocations that stay on
 * the TypeScript CLI: `graft build --deep`, `graft viz`, and
 * `graft blast --export-viz`. A platform without a packaged binary falls back
 * to the TypeScript CLI, which is the behavioral oracle for the Go one.
 */
import { spawn } from "node:child_process";
import { existsSync, realpathSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

/** Program options that consume the next argument, as declared in src/cli.ts. */
const VALUE_OPTIONS = new Set(["--dir", "--provider", "--model", "--api-key", "--base-url"]);

/** Whether args (argv without node and script) must run on the TypeScript CLI. */
export function routesToTypeScript(args) {
  const end = args.indexOf("--");
  const words = end === -1 ? args : args.slice(0, end);
  let at = 0;
  while (at < words.length && words[at].startsWith("-")) {
    at += VALUE_OPTIONS.has(words[at]) ? 2 : 1;
  }
  const command = words[at];
  const rest = words.slice(at + 1);
  const has = (flag) => rest.some((word) => word === flag || word.startsWith(`${flag}=`));
  if (command === "viz") return true;
  if (command === "build") return has("--deep");
  if (command === "blast") return has("--export-viz");
  return false;
}

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
  const args = process.argv.slice(2);
  const binary = join(here, nativeBinaryName());
  if (!routesToTypeScript(args) && existsSync(binary)) {
    runNative(binary, args);
    return;
  }
  import(pathToFileURL(join(here, "..", "dist", "cli.js")).href);
}

// npm links this file into PATH, so compare real paths; importing it (tests)
// must not start a CLI.
if (process.argv[1] && realpathSync(process.argv[1]) === realpathSync(fileURLToPath(import.meta.url))) main();
