import { test } from "node:test";
import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import { chmodSync, copyFileSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { nativeBinaryName } from "../bin/graft.js";

function launcherFixture() {
  const root = mkdtempSync(join(tmpdir(), "graft-launcher-"));
  mkdirSync(join(root, "bin"));
  copyFileSync("bin/graft.js", join(root, "bin", "graft.js"));
  return root;
}

test("native binary names carry the platform, architecture, and .exe on Windows", () => {
  assert.equal(nativeBinaryName("darwin", "arm64"), "graft-darwin-arm64");
  assert.equal(nativeBinaryName("linux", "x64"), "graft-linux-x64");
  assert.equal(nativeBinaryName("win32", "x64"), "graft-win32-x64.exe");
});

test("the launcher passes arguments, stdio, and exit status to the packaged binary", { skip: process.platform === "win32" }, () => {
  const root = launcherFixture();
  try {
    const binary = join(root, "bin", nativeBinaryName());
    writeFileSync(binary, '#!/bin/sh\necho "go $*"\necho "err" >&2\nexit 3\n');
    chmodSync(binary, 0o755);

    const result = spawnSync(process.execPath, [join(root, "bin", "graft.js"), "ask", "store get"], { encoding: "utf8" });
    assert.equal(result.stdout, "go ask store get\n");
    assert.equal(result.stderr, "err\n");
    assert.equal(result.status, 3);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("the launcher forwards termination signals", { skip: process.platform === "win32" }, async () => {
  const root = launcherFixture();
  try {
    const binary = join(root, "bin", nativeBinaryName());
    writeFileSync(binary, "#!/bin/sh\ntrap 'echo stopped; exit 7' TERM\necho ready\nwhile :; do sleep 0.05; done\n");
    chmodSync(binary, 0o755);

    const stopped = await new Promise((resolve, reject) => {
      const child = spawn(process.execPath, [join(root, "bin", "graft.js"), "mcp"], { stdio: ["ignore", "pipe", "inherit"] });
      let stdout = "";
      child.stdout.on("data", (chunk) => {
        stdout += chunk;
        if (stdout === "ready\n") child.kill("SIGTERM");
      });
      child.on("error", reject);
      child.on("exit", (code) => resolve({ code, stdout }));
    });
    assert.deepEqual(stopped, { code: 7, stdout: "ready\nstopped\n" });
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("the launcher reports a missing native binary and exits 1", () => {
  const root = launcherFixture();
  try {
    const result = spawnSync(process.execPath, [join(root, "bin", "graft.js"), "ask", "x"], { encoding: "utf8" });
    assert.equal(result.stdout, "");
    assert.equal(result.stderr, `✗ graft has no native binary for ${process.platform}-${process.arch}\n`);
    assert.equal(result.status, 1);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});
