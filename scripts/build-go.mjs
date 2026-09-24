/**
 * Builds the native Go CLI for the current platform into
 * `bin/graft-<platform>-<arch>`, the file `bin/graft.js` launches.
 *
 * The tree-sitter grammars are cgo, so each binary is built on its own
 * platform; a platform without one is unsupported.
 *
 * The PostHog key is baked in only when GRAFT_POSTHOG_KEY is set, so source
 * builds never send telemetry.
 */
import { spawnSync } from "node:child_process";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const { nativeBinaryName } = await import(pathToFileURL(join(root, "bin", "graft.js")).href);
const output = join(root, "bin", nativeBinaryName());

const key = process.env.GRAFT_POSTHOG_KEY ?? "";
const host = process.env.GRAFT_POSTHOG_HOST ?? "";
if (key && !/^[A-Za-z0-9_-]+$/.test(key)) {
  console.error("✗ GRAFT_POSTHOG_KEY contains characters that are not valid in a project key.");
  process.exit(1);
}
if (host && !/^https:\/\/[A-Za-z0-9.-]+(:\d+)?$/.test(host)) {
  console.error("✗ GRAFT_POSTHOG_HOST must be a plain https origin, e.g. https://eu.i.posthog.com");
  process.exit(1);
}

const telemetry = "github.com/NanoNets/context-graph-engine/internal/telemetry";
const ldflags = ["-s", "-w"];
if (key) ldflags.push(`-X ${telemetry}.bakedKey=${key}`);
if (host) ldflags.push(`-X ${telemetry}.bakedHost=${host}`);

const result = spawnSync("go", ["build", "-trimpath", `-ldflags=${ldflags.join(" ")}`, "-o", output, "./cmd/graft"], {
  cwd: root,
  stdio: "inherit",
});
if (result.error) {
  console.error(`✗ could not run go: ${result.error.message}`);
  process.exit(1);
}
if (result.status !== 0) process.exit(result.status ?? 1);
console.log(`✓ built ${output}${key ? " (telemetry key stamped)" : ""}`);
