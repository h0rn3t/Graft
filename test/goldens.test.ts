import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { captureGolden, snapshotGoldenFiles } from "./goldens.js";

test("golden capture is deterministic", (t) => {
  const root = mkdtempSync(join(tmpdir(), "graft-golden-"));
  const fixture = join(root, "fixture");
  mkdirSync(join(fixture, "src"), { recursive: true });
  writeFileSync(join(fixture, "src", "b.ts"), "b\n");
  writeFileSync(join(fixture, "src", "a.ts"), `root=${fixture}\n`);
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const previousCapture = process.env.GRAFT_CAPTURE_GOLDENS;
  const previousDir = process.env.GRAFT_GOLDEN_DIR;
  process.env.GRAFT_CAPTURE_GOLDENS = "1";
  process.env.GRAFT_GOLDEN_DIR = root;
  t.after(() => {
    if (previousCapture === undefined) delete process.env.GRAFT_CAPTURE_GOLDENS;
    else process.env.GRAFT_CAPTURE_GOLDENS = previousCapture;
    if (previousDir === undefined) delete process.env.GRAFT_GOLDEN_DIR;
    else process.env.GRAFT_GOLDEN_DIR = previousDir;
  });

  const golden = {
    args: ["build", "{{ROOT}}"],
    cwd: "<REPO>/src",
    env: { GRAFT_NO_REFRESH: "1" },
    gitDirs: [".", "packages/core"],
    initialInputs: { "src/a.ts": "before\n" },
    inputs: { "src/a.ts": "export function a() {}\n" },
    mutations: { writes: { "repo/src/a.ts": "changed\n" }, deletes: ["repo/old.ts"] },
    status: 0,
    tracked: ["src/a.ts"],
    checkFiles: true,
    stdout: "",
    stderr: "{{ROOT}}\n",
    files: { "graft/.graph/wiring.json": "{}\n" },
    normalize: { [root]: "{{ROOT}}" },
  };

  captureGolden("suite/case", golden);
  const first = readFileSync(join(root, "suite", "case.json"), "utf8");
  captureGolden("suite/case", golden);
  const second = readFileSync(join(root, "suite", "case.json"), "utf8");

  assert.equal(second, first);
  assert.deepEqual(JSON.parse(second).initialInputs, { "src/a.ts": "before\n" });
  assert.equal(JSON.parse(second).cwd, "<REPO>/src");
  assert.deepEqual(JSON.parse(second).gitDirs, [".", "packages/core"]);
  assert.deepEqual(JSON.parse(second).mutations, { writes: { "repo/src/a.ts": "changed\n" }, deletes: ["repo/old.ts"] });
  assert.deepEqual(JSON.parse(second).tracked, ["src/a.ts"]);
  assert.equal(JSON.parse(second).checkFiles, true);
  assert.match(second, /"status": 0/);
  assert.match(second, /"files":/);
  assert.doesNotMatch(second, new RegExp(root.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
  assert.deepEqual(snapshotGoldenFiles(fixture), {
    "src/a.ts": `root=${fixture}\n`,
    "src/b.ts": "b\n",
  });
});
