/**
 * TS↔Go differential for the structural build: the same fixture is built by the
 * TypeScript CLI and the native Go CLI, and `wiring.json` must be byte-identical —
 * ids, spans, signatures, metadata, edges, scopes, key order and sort order alike.
 * Covers a cold build, an incremental rebuild after edits and deletions, and the
 * Go cold/incremental equivalence.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { cpSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { tmpRepo } from "./helpers.js";

function write(root: string, files: Record<string, string>): void {
  for (const [rel, text] of Object.entries(files)) {
    mkdirSync(join(root, rel, ".."), { recursive: true });
    writeFileSync(join(root, rel), text);
  }
}

const FIXTURE: Record<string, string> = {
  "package.json": JSON.stringify({ name: "root", workspaces: ["packages/*"] }),
  "packages/core/package.json": JSON.stringify({ name: "core" }),
  "packages/core/src/store.ts": [
    "import { Logger as Log } from './log.js';",
    "export interface Backend { get(key: string): string }",
    "export abstract class Base { protected log = new Log(); ping(): void { this.log.write('p'); } }",
    "export class Store extends Base implements Backend {",
    "  private cache: Map<string, string> = new Map();",
    "  constructor(private readonly backend: Log, public limit: number = 3) { super(); }",
    "  get(key: string): string {",
    "    this.backend.write(key);",
    "    this.ping();",
    "    return this.cache.get(key) ?? fallback(key);",
    "  }",
    "  static create(): Store { const s: Store = new Store(new Log()); s.get('x'); return s; }",
    "}",
    "function fallback(key: string): string { return key; }",
    "export const shout = (text: string): string => text.toUpperCase();",
    "export const whisper = function (text: string) { return fallback(text); };",
    "export function* ids(): Generator<number> { yield 1; }",
    "export type Key = string;",
    "export enum Mode { A, B }",
  ].join("\n"),
  "packages/core/src/log.ts": [
    "export class Logger { write(message: string): void { console.log(message); } }",
    "export function shadowed(Logger: number) { return Logger; }",
    "export function write() {}",
  ].join("\n"),
  "packages/core/src/index.ts": "export * from './store.js';\nimport { shout, Store } from './store';\nexport function main() { return shout(Store.create().get('k')); }\n",
  "packages/ui/package.json": JSON.stringify({ name: "ui" }),
  "packages/ui/app.tsx": [
    "import React from 'react';",
    "import { main } from '../core/src/index.js';",
    "export function App(): JSX.Element { return <div onClick={() => main()}>{label()}</div>; }",
    "const label = () => 'hi';",
    "class Widget extends React.Component { render() { return this.props.x; } }",
    "export default Widget;",
  ].join("\n"),
  "packages/ui/legacy.jsx": "export const Old = (props) => <span>{props.x}</span>;\nfunction helper() { return Old({}); }\n",
  "scripts/tool.mjs": "import { readFileSync } from 'node:fs';\nexport async function run(p) { return readFileSync(p, 'utf8'); }\nrun('a');\n",
  "scripts/cfg.cjs": "const path = require('path');\nmodule.exports = function config() { return path.join('a'); };\nfunction dup() {}\nfunction dup() {}\n",
  "scripts/types.d.ts": "declare module 'x' { export function y(): void; }\nexport declare function z(a: number): string;\n",
  "src/unicode.ts": "// ünïcödé 😀 — keeps UTF-16 lengths honest\nexport function émoji(): string { return '😀\\u2028'; }\n",
  "README.md": "# fixture\n",
};

const PYTHON_GO_FIXTURE: Record<string, string> = {
  "pyproject.toml": "[project]\nname = 'app'\n",
  "app/__init__.py": "",
  "app/models.py": [
    "from typing import Optional as Opt",
    "import os.path as osp",
    "from .store import Store as S",
    "",
    "class Base:",
    "    def save(self):",
    "        return self._write()",
    "    def _write(self):",
    "        return osp.join('a', 'b')",
    "",
    "class User(Base):",
    "    store: S",
    "    def __init__(self, store: S):",
    "        self.store = S()",
    "        self.cache = dict()",
    "    @property",
    "    def name(self) -> Opt[str]:",
    "        self.store.put('k')",
    "        return self.save()",
    "",
    "def make() -> User:",
    "    u = User(S())",
    "    u.name",
    "    return Widget()",
    "",
    "class Widget: pass",
  ].join("\n"),
  "app/store.py": "class Store:\n    def put(self, key):\n        pass\n\ndef _hidden():\n    pass\n",
  "stubs/api.pyi": "def typed(a: int) -> str: ...\n",
  "go.mod": "module example.com/app\n\ngo 1.22\n",
  "cmd/main.go": [
    "package main",
    "",
    "import (",
    "\t\"fmt\"",
    "\t\"example.com/app/pkg/store\"",
    ")",
    "",
    "func main() {",
    "\ts := store.NewStore()",
    "\tw := &Worker{}",
    "\tvar r Runner",
    "\tfmt.Println(s.Get(\"k\"), w.Run(), r)",
    "}",
    "",
    "type Runner interface{ Run() error }",
    "type Worker struct{ name string }",
    "type ID int",
    "",
    "func (w *Worker) Run() error { return w.helper() }",
    "func (w Worker) helper() error { return nil }",
  ].join("\n"),
  "pkg/store/store.go": "package store\n\ntype Store struct{}\n\nfunc NewStore() *Store { return &Store{} }\n\nfunc (s *Store) Get(k string) string { return s.get(k) }\nfunc (s *Store) get(k string) string { return k }\n",
  "pkg/store/b.go": "package store\n\nimport \"example.com/app/pkg/store\"\n\nvar _ = store.NewStore\n",
};

function buildTs(root: string): string {
  execFileSync(process.execPath, ["--import", "tsx", "src/cli.ts", "build", root], {
    stdio: "pipe",
    env: { ...process.env, GRAFT_NO_REFRESH: "1", DO_NOT_TRACK: "1" },
  });
  return readFileSync(join(root, "graft", ".graph", "wiring.json"), "utf8");
}

function buildGo(binary: string, root: string): string {
  execFileSync(binary, ["build", root], { stdio: "pipe" });
  return readFileSync(join(root, "graft", ".graph", "wiring.json"), "utf8");
}

function goBinary(): string {
  const binary = join(tmpRepo("build-parity-bin"), "graft");
  execFileSync("go", ["build", "-o", binary, "./cmd/graft"], { stdio: "pipe" });
  return binary;
}

function twin(files: Record<string, string>): { ts: string; go: string } {
  const ts = tmpRepo("build-parity-ts");
  write(ts, files);
  execFileSync("git", ["init", "-q"], { cwd: ts });
  const go = tmpRepo("build-parity-go");
  cpSync(ts, go, { recursive: true });
  return { ts, go };
}

test("Go build writes a byte-identical wiring.json to TypeScript, cold and incremental", () => {
  const binary = goBinary();
  const { ts, go } = twin(FIXTURE);
  const cold = buildTs(ts);
  assert.equal(buildGo(binary, go), cold, "cold build");
  assert.match(cold, /"prefix": "packages\/core"/, "fixture exercises workspace scopes");
  assert.match(cold, /"target": "packages\/core\/src\/log.ts#Logger.write"/, "fixture exercises typed member calls");

  const edits = {
    "packages/core/src/log.ts": "export class Logger { write(message: string): void { console.error(message); } flush() {} }\n",
    "packages/ui/new.ts": "import { Logger } from '../core/src/log';\nexport function use(l: Logger) { l.flush(); }\n",
  };
  for (const root of [ts, go]) {
    write(root, edits);
    rmSync(join(root, "scripts", "cfg.cjs"));
  }
  const incremental = buildTs(ts);
  assert.equal(buildGo(binary, go), incremental, "incremental build");

  rmSync(join(go, "graft"), { recursive: true, force: true });
  assert.equal(buildGo(binary, go), incremental, "Go cold build equals its incremental build");
});

test("Go build matches TypeScript for Python and Go sources, cold and incremental", () => {
  const binary = goBinary();
  const { ts, go } = twin(PYTHON_GO_FIXTURE);
  const cold = buildTs(ts);
  assert.equal(buildGo(binary, go), cold, "cold build");
  assert.match(cold, /"target": "cmd\/main.go#Worker.helper"/, "fixture exercises Go receiver calls");
  assert.match(cold, /"target": "app\/models.py#Base.save"/, "fixture exercises Python self calls");
  for (const root of [ts, go]) {
    write(root, { "app/store.py": "class Store:\n    def put(self, key):\n        return key\n    def drop(self):\n        self.put(1)\n" });
    rmSync(join(root, "stubs", "api.pyi"));
  }
  assert.equal(buildGo(binary, go), buildTs(ts), "incremental build");
});

test("Go build matches TypeScript for Rust tags and crate imports", () => {
  const binary = goBinary();
  const { ts, go } = twin({
    "Cargo.toml": "[package]\nname = \"sample\"\nversion = \"0.1.0\"\n",
    "src/lib.rs": "mod util;\nuse crate::util::Thing;\npub fn run() -> i32 { util::helper() }\n",
    "src/util.rs": "pub struct Thing;\npub fn helper() -> i32 { 1 }\n",
  });
  const cold = buildTs(ts);
  assert.equal(buildGo(binary, go), cold, "cold Rust build");
  assert.match(cold, /"target": "src\/util.rs#helper"/, "fixture exercises Rust call resolution");
  assert.equal(buildGo(binary, go), cold, "incremental Rust build");
});

test("Go build matches TypeScript for C and C++ tags and local includes", () => {
  const binary = goBinary();
  const { ts, go } = twin({
    "src/local.h": "int helper(void);\n",
    "src/main.c": '#include "local.h"\n#include <stdio.h>\nint helper(void) { return 1; }\nint run(void) { return helper(); }\n',
    "src/local.hpp": "int make();\n",
    "src/main.cpp": '#include "local.hpp"\nclass Widget { public: int make() { return 1; } int run() { return make(); } };\n',
  });
  const cold = buildTs(ts);
  assert.equal(buildGo(binary, go), cold, "cold C/C++ build");
  assert.match(cold, /"target": "src\/local.h"/, "fixture exercises C local include");
  assert.match(cold, /"target": "src\/local.hpp"/, "fixture exercises C++ local include");
  assert.equal(buildGo(binary, go), cold, "incremental C/C++ build");
});

test("Go build matches TypeScript for Java tags and references", () => {
  const binary = goBinary();
  const { ts, go } = twin({
    "src/Worker.java": "interface Service {}\nclass Base {}\nclass Worker extends Base implements Service { Service run() { return make(); } Service make() { return null; } }\n",
  });
  const cold = buildTs(ts);
  assert.equal(buildGo(binary, go), cold, "cold Java build");
  assert.match(cold, /"target": "src\/Worker.java#Worker.make"/, "fixture exercises Java call resolution");
  assert.equal(buildGo(binary, go), cold, "incremental Java build");
});

test("Go build excludes Ruby while TypeScript still indexes it", () => {
  const binary = goBinary();
  const { ts, go } = twin({
    "src/main.ts": "export function main() {}\n",
    "lib/widget.rb": "class Widget\n  def render\n    self.draw\n  end\n  def draw\n    1\n  end\nend\n",
  });
  assert.match(buildTs(ts), /"id": "lib\/widget.rb#Widget"/, "TypeScript retains Ruby support");
  const cold = buildGo(binary, go);
  assert.doesNotMatch(cold, /lib\/widget.rb/, "Go excludes Ruby by default");
  assert.match(cold, /"id": "src\/main.ts#main"/, "Go keeps selected sources");
  assert.equal(buildGo(binary, go), cold, "incremental Go build");
});

test("Go build carries the prior meaning layer forward like TypeScript", () => {
  const binary = goBinary();
  const { ts, go } = twin({ "src/a.ts": "export function kept() { return 1; }\nexport function changed() { return 1; }\n" });
  const graph = JSON.parse(buildTs(ts));
  for (const node of graph.nodes) {
    if (node.kind === "file") continue;
    node.summary = `about ${node.name}`;
    node.crux = { code: "return 1;", span: node.span };
    node.summary_state = "ready";
  }
  const seeded = JSON.stringify(graph, null, 2) + "\n";
  mkdirSync(join(go, "graft", ".graph"), { recursive: true });
  for (const root of [ts, go]) {
    writeFileSync(join(root, "graft", ".graph", "wiring.json"), seeded);
    write(root, { "src/a.ts": "export function kept() { return 1; }\nexport function changed() { return 2; }\n" });
  }
  const expected = buildTs(ts);
  assert.match(expected, /"summary_state": "stale"/);
  assert.equal(buildGo(binary, go), expected);
});
