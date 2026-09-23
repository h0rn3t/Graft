/**
 * The Go host writers embed their instruction, skill and shim text from
 * internal/hosts/templates/. Those files are generated from the TypeScript
 * sources below, so a change on either side must be mirrored on the other.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { instructionBody } from "../src/hosts/instructions.js";
import { skillTemplate } from "../src/claude/skill-template.js";
import { hooksShim, statuslineShim } from "../src/claude/shim-template.js";

const template = (name: string) => readFileSync(`internal/hosts/templates/${name}`, "utf8");

test("Go host templates match the TypeScript sources", () => {
  assert.equal(template("instructions.md"), `${instructionBody()}\n`);
  assert.equal(template("skill.md"), skillTemplate());
  assert.equal(template("hooks-shim.cjs"), hooksShim("@@BAKED@@"));
  assert.equal(template("statusline-shim.cjs"), statuslineShim("@@BAKED@@"));
});
