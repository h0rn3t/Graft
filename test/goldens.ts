import { mkdirSync, readdirSync, readFileSync, statSync, writeFileSync } from "node:fs";
import { dirname, join, relative, sep } from "node:path";

type GoldenCapture = {
  args: string[];
  cwd?: string;
  env?: Record<string, string>;
  tracked?: string[];
  gitDirs?: string[];
  initialInputs?: Record<string, string>;
  inputs?: Record<string, string>;
  mutations?: { writes: Record<string, string>; deletes: string[] };
  status: number;
  stdout: string;
  stderr: string;
  files?: Record<string, string>;
  checkFiles?: boolean;
  normalize?: Record<string, string>;
};

function sortedRecord(values: Record<string, string>, normalize: (value: string) => string): Record<string, string> {
  return Object.fromEntries(Object.entries(values).sort(([a], [b]) => a < b ? -1 : a > b ? 1 : 0).map(([key, value]) => [key, normalize(value)]));
}

export function snapshotGoldenFiles(root: string): Record<string, string> {
  const files: Record<string, string> = {};
  const walk = (dir: string) => {
    for (const name of readdirSync(dir).sort()) {
      const path = join(dir, name);
      const rel = relative(root, path).split(sep).join("/");
      if (name === ".git" || /(^|\/)\.cache\/(extract|fingerprint)\.[^/]+\.json$/.test(rel)) continue;
      if (statSync(path).isDirectory()) walk(path);
      else files[rel] = readFileSync(path, "utf8");
    }
  };
  walk(root);
  return sortedRecord(files, (value) => value);
}

export function captureGolden(name: string, capture: GoldenCapture): void {
  if (process.env.GRAFT_CAPTURE_GOLDENS !== "1") return;
  if (!Number.isInteger(capture.status)) throw new Error(`golden ${name} has no exit status`);

  const replacements = Object.entries(capture.normalize ?? {}).sort(([a], [b]) => b.length - a.length);
  const normalize = (value: string) => replacements.reduce((text, [from, to]) => text.replaceAll(from, to), value);
  const outputDir = process.env.GRAFT_GOLDEN_DIR ?? join(process.cwd(), "cmd", "graft", "testdata", "goldens");
  const output = join(outputDir, `${name}.json`);
  const golden = {
    args: capture.args.map(normalize),
    ...(capture.cwd !== undefined && { cwd: normalize(capture.cwd) }),
    env: sortedRecord(capture.env ?? {}, normalize),
    ...(capture.tracked && { tracked: [...capture.tracked].sort() }),
    ...(capture.gitDirs && { gitDirs: [...capture.gitDirs].sort() }),
    ...(capture.initialInputs && { initialInputs: sortedRecord(capture.initialInputs, (value) => value) }),
    inputs: sortedRecord(capture.inputs ?? {}, normalize),
    ...(capture.mutations && {
      mutations: {
        writes: sortedRecord(capture.mutations.writes, normalize),
        deletes: [...capture.mutations.deletes].sort(),
      },
    }),
    status: capture.status,
    stdout: normalize(capture.stdout),
    stderr: normalize(capture.stderr),
    files: sortedRecord(capture.files ?? {}, normalize),
    ...(capture.checkFiles !== undefined && { checkFiles: capture.checkFiles }),
  };

  mkdirSync(dirname(output), { recursive: true });
  writeFileSync(output, `${JSON.stringify(golden, null, 2)}\n`);
}
