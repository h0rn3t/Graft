/**
 * The Go CLI prints commander's help text for every command from files
 * generated out of this TypeScript CLI (cmd/graft/help/). A change to a
 * command's description, arguments or options must regenerate them.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readdirSync, readFileSync } from "node:fs";

test("Go help texts match the TypeScript CLI's commander help", () => {
  const files = readdirSync("cmd/graft/help").filter((name) => name.endsWith(".txt"));
  assert.ok(files.length > 20, "every command has a help file");
  for (const file of files) {
    const name = file.replace(/\.txt$/, "");
    const path = name === "graft" ? [] : name.startsWith("brain-") ? ["brain", name.slice("brain-".length)] : [name];
    const help = execFileSync(process.execPath, ["--import", "tsx", "src/cli.ts", ...path, "--help"], {
      encoding: "utf8",
      env: { ...process.env, DO_NOT_TRACK: "1", COLUMNS: "80" },
    });
    assert.equal(readFileSync(`cmd/graft/help/${file}`, "utf8"), help, `graft ${path.join(" ")} --help`);
  }
});
