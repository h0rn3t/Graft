import { test } from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { mkdtempSync, mkdirSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

function builtRepo(): string {
  const dir = mkdtempSync(join(tmpdir(), 'graft-skeleton-cli-'));
  mkdirSync(join(dir, 'src'), { recursive: true });
  writeFileSync(
    join(dir, 'src', 'api.ts'),
    'export function first(a: number): number {\n  return a;\n}\n\n' +
      'export function second(b: string): string {\n  return b;\n}\n',
  );
  execFileSync(process.execPath, ['--import', 'tsx', 'src/cli.ts', 'build', dir], { stdio: 'pipe' });
  return dir;
}

function ambiguousRepo(): string {
  const dir = mkdtempSync(join(tmpdir(), 'graft-skeleton-ambiguous-'));
  mkdirSync(join(dir, 'src', 'a'), { recursive: true });
  mkdirSync(join(dir, 'src', 'b'), { recursive: true });
  writeFileSync(join(dir, 'src', 'a', 'api.ts'), 'export function first(): number { return 1; }\n');
  writeFileSync(join(dir, 'src', 'b', 'api.ts'), 'export function second(): number { return 2; }\n');
  execFileSync(process.execPath, ['--import', 'tsx', 'src/cli.ts', 'build', dir], { stdio: 'pipe' });
  return dir;
}

function builtGoCli(): string {
  const dir = mkdtempSync(join(tmpdir(), 'graft-skeleton-go-cli-'));
  const binary = join(dir, 'graft');
  execFileSync('go', ['build', '-o', binary, './cmd/graft'], { stdio: 'pipe' });
  return binary;
}

function runCli(args: string[]): { stdout: string; stderr: string; status: number } {
  try {
    const stdout = execFileSync(process.execPath, ['--import', 'tsx', 'src/cli.ts', ...args], {
      encoding: 'utf8',
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    return { stdout, stderr: '', status: 0 };
  } catch (err) {
    const e = err as { stdout?: string; stderr?: string; status?: number };
    return { stdout: e.stdout ?? '', stderr: e.stderr ?? '', status: e.status ?? 1 };
  }
}

function runGoCli(binary: string, args: string[]): { stdout: string; stderr: string; status: number } {
  try {
    const stdout = execFileSync(binary, args, {
      cwd: process.cwd(),
      encoding: 'utf8',
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    return { stdout, stderr: '', status: 0 };
  } catch (err) {
    const e = err as { stdout?: string; stderr?: string; status?: number };
    return { stdout: e.stdout ?? '', stderr: e.stderr ?? '', status: e.status ?? 1 };
  }
}

test('Go skeleton CLI matches TypeScript JSON, exit codes, and diagnostics', () => {
  const dir = builtRepo();
  const ambiguous = ambiguousRepo();
  const binary = builtGoCli();
  const cases = [
    ['skeleton', 'src/api.ts', dir, '--json'],
    ['skeleton', 'api.ts', dir, '--json'],
    ['skeleton', 'missing.ts', dir, '--json'],
    ['skeleton', 'api.ts', ambiguous, '--json'],
    ['skeleton', 'api.ts', mkdtempSync(join(tmpdir(), 'graft-skeleton-missing-')), '--json'],
  ];

  for (const args of cases) {
    const typescript = runCli(args);
    const go = runGoCli(binary, args);
    assert.equal(go.status, typescript.status, `status mismatch for ${args.join(' ')}`);
    assert.equal(go.stderr, typescript.stderr, `stderr mismatch for ${args.join(' ')}`);
    assert.deepEqual(JSON.parse(go.stdout), JSON.parse(typescript.stdout), `JSON mismatch for ${args.join(' ')}`);
  }
});