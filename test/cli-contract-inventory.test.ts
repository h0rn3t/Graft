/**
 * Keeps `docs/cli-contract.json` — the public CLI contract the Go cutover is
 * measured against — in lockstep with the commander definitions in
 * `src/cli.ts`. A command or flag added to the CLI without an inventory entry
 * (or an entry left behind after a removal) fails here, so the differential
 * matrix can never silently miss a surface.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";

interface InventoryCommand {
  name: string;
  usage: string;
  hidden?: boolean;
  options: string[];
  backend: "go" | "typescript";
}

interface Inventory {
  globalOptions: string[];
  commands: InventoryCommand[];
  environment: Record<string, { scope: string }>;
  exceptions: { invocation: string[]; backend: string }[];
}

const inventory = JSON.parse(readFileSync("docs/cli-contract.json", "utf8")) as Inventory;

function help(args: string[]): string {
  return execFileSync(process.execPath, ["--import", "tsx", "src/cli.ts", ...args, "--help"], {
    encoding: "utf8",
    stdio: ["ignore", "pipe", "pipe"],
    env: { ...process.env, DO_NOT_TRACK: "1" },
  });
}

/** The flag column of commander's help, without the built-in `-h, --help`. */
function helpOptions(text: string): string[] {
  const section = text.split(/^Options:$/m)[1]?.split(/^Commands:$/m)[0] ?? "";
  return section
    .split("\n")
    .map((line) => /^ {2}(-\S.*?)(?: {2,}|$)/.exec(line)?.[1])
    .filter((flag): flag is string => flag !== undefined && flag !== "-h, --help");
}

/** Command names listed under `Commands:`, without commander's `help`. */
function helpCommands(text: string): string[] {
  const section = text.split(/^Commands:$/m)[1] ?? "";
  return section
    .split("\n")
    .map((line) => /^ {2}([a-z_][\w-]*)/.exec(line)?.[1])
    .filter((name): name is string => name !== undefined && name !== "help");
}

test("inventory lists exactly the public commands and global options of src/cli.ts", () => {
  const root = help([]);
  assert.deepEqual(helpOptions(root), ["-v, --version", ...inventory.globalOptions]);

  const brain = help(["brain"]);
  const listed = [
    ...helpCommands(root).filter((name) => name !== "brain"),
    ...helpCommands(brain).map((name) => `brain ${name}`),
  ];
  const publicCommands = inventory.commands.filter((c) => !c.hidden).map((c) => c.name);
  assert.deepEqual([...publicCommands].sort(), [...listed].sort());
});

test("every inventoried command matches its commander usage and flags", () => {
  for (const command of inventory.commands) {
    const text = help(command.name.split(" "));
    const usage = /^Usage: (.*)$/m.exec(text)?.[1];
    assert.equal(usage, command.usage, `usage of ${command.name}`);
    assert.deepEqual(helpOptions(text), command.options, `options of ${command.name}`);
  }
});

test("build --deep is the only TypeScript-routed invocation", () => {
  assert.deepEqual(
    inventory.exceptions.map(({ invocation, backend }) => ({ invocation, backend })),
    [{ invocation: ["build", "--deep"], backend: "typescript" }],
  );
  assert.ok(inventory.commands.find((c) => c.name === "build")?.options.includes("--deep"));
  assert.deepEqual(inventory.commands.filter((c) => c.backend !== "go"), []);
});

function sourceFiles(dir: string): string[] {
  return readdirSync(dir).flatMap((entry) => {
    const path = join(dir, entry);
    if (statSync(path).isDirectory()) return sourceFiles(path);
    return path.endsWith(".ts") ? [path] : [];
  });
}

test("inventory covers every environment variable the CLI source reads", () => {
  const read = new Set<string>();
  for (const file of sourceFiles("src")) {
    // The GitHub App server is a separate deployable, not part of the CLI.
    const scope = file.split(/[\\/]/)[1] === "app" ? "app" : "cli";
    for (const match of readFileSync(file, "utf8").matchAll(/\benv(?:\.|\[")([A-Z][A-Z0-9_]+)/g)) {
      const name = match[1];
      if (scope === "cli") read.add(name);
      assert.ok(inventory.environment[name], `${name} (read in ${file}) is missing from docs/cli-contract.json`);
    }
  }
  for (const [name, entry] of Object.entries(inventory.environment)) {
    if (entry.scope === "app") continue;
    assert.ok(read.has(name), `${name} is inventoried as ${entry.scope} but no CLI source reads it`);
  }
});
