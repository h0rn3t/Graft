/**
 * Builds the native Go CLI for the current platform into
 * `bin/graft-<platform>-<arch>`, the file `bin/graft.js` launches.
 *
 * The tree-sitter grammars are cgo, so each binary is built on its own
 * platform; a platform without one is unsupported.
 */
import { spawnSync } from "node:child_process";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const { nativeBinaryName } = await import(pathToFileURL(join(root, "bin", "graft.js")).href);
const output = join(root, "bin", nativeBinaryName());

const result = spawnSync("go", ["build", "-trimpath", "-ldflags=-s -w", "-o", output, "./cmd/graft"], {
  cwd: root,
  stdio: "inherit",
});
if (result.error) {
  console.error(`✗ could not run go: ${result.error.message}`);
  process.exit(1);
}
if (result.status !== 0) process.exit(result.status ?? 1);
console.log(`✓ built ${output}`);
