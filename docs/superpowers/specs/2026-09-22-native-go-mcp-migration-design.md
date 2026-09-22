# Native Go MCP Migration Design

Date: 2026-09-22

Status: approved design; implementation in progress (source discovery,
TypeScript/JavaScript graph extraction, native refresh, workspace federation,
cached MCP update notices, background brain-rule cache refresh, and brain-rule
host-file refresh). Wiring replay and the complete differential matrix remain.

## Goal

Continue the Go migration with a native Go implementation of the next MCP
slice:

- freshness and source drift detection;
- graph refresh and git-worktree seeding;
- workspace federation;
- MCP startup upkeep.

The public contract remains unchanged: MCP JSON-RPC messages, tool names and
aliases, JSON fields, human-readable text, diagnostics, protocol notifications,
and fail-soft behavior must remain compatible with the TypeScript
implementation. TypeScript remains the behavioral oracle and source of truth
until final cutover. The viewer and its TypeScript build remain out of scope.

## Scope boundary

The native refresh architecture will support language adapters. This slice
implements the TypeScript and JavaScript adapters first, matching the migration
plan's source-walking order. Existing GraphV1 files for other languages remain
readable by Go, but a refresh must never call an unsupported tree clean or
silently claim parity. The response records an explicit refresh limitation
until the corresponding adapter is implemented in a later migration slice.

There is no runtime Node/TypeScript bridge in the native path. TypeScript is
used only by differential tests and fixture generation. TypeScript CLI,
backend, and host integrations remain available throughout the migration.

## Components

### Source discovery and graph construction

Add native Go graph-building components behind the existing `GraphV1`, `NodeV1`,
`EdgeV1`, and `Write` contracts:

- a git-aware source walker that preserves tracked, untracked, ignored,
  nested-repository, submodule, extension, include-directory, and `--only-dir`
  behavior;
- source decoding with the current UTF-8 and UTF-16LE behavior;
- pinned official tree-sitter Go bindings and TypeScript/JavaScript grammars;
- extraction of file nodes, definitions, signatures, spans, body hashes,
  body text, exported state, containment, imports, calls, references, scopes,
  and unresolved edges;
- deterministic edge resolution and graph sorting before atomic writing;
- extraction-cache and fingerprint sidecars sufficient for incremental builds;
- a graph-only build mode that updates wiring, query sidecars, and freshness
  state without rewriting markdown cards or `.gitignore`.

The existing Go query cores in `internal/graph` remain the consumers of the
result. No duplicate GraphV1 model is introduced.

### Freshness, drift, refresh, and seeding

Implement the TypeScript refresh contract in native Go:

- `Fingerprint` and `Drift` sidecars with extractor identity, file size,
  mtime, content hash, and recorded `onlyDirs`;
- the stat fast path plus byte hashing for changed candidates;
- `GRAFT_REFRESH=hash` and `GRAFT_NO_REFRESH` semantics;
- a two-second lock wait with 50 ms polling on `<context>/.cache/.sync.lock`;
- re-probing after acquiring the lock to avoid rebuild stampedes;
- fail-soft refresh errors that preserve the current graph as a fallback;
- parent worktree detection and locked graph seeding;
- sequential child refresh for workspaces;
- `refreshNote` text and file-count semantics.

`graft_check_freshness` is excluded from the pre-query refresh gate. It reports
the graph state instead of repairing it.

### Workspace federation

Add native support for `graft/workspace.json`:

- validate version 1 and sort child names for deterministic processing;
- load each child graph from its own `<child>/graft/` directory;
- report missing child graphs instead of silently dropping them;
- prefix child paths and pointers in public output;
- federate ask using the existing Go ranking metadata and reciprocal-rank
  fusion, preserving the baseline top lock, file queues, weak-scope gate, and
  `--in` semantics;
- federate grep, map, callers, and freshness reports with the same child
  labels and coverage note as TypeScript.

### Upkeep

The Go MCP boot path will perform the same fail-soft maintenance sequence:

1. read the wiring stamp and replay only the selected host/options;
2. read the cached update answer and format the version nudge;
3. schedule stale update/rules work without blocking the MCP handshake;
4. include upkeep lines in `initialize.result.instructions` while keeping
   stdout protocol-only.

Native host target writers will preserve existing paths, scopes, formats, and
`--no-global`, `--no-mcp`, `--no-hooks`, and `--no-statusline` choices. A
failure to rewrite wiring is swallowed and does not fail MCP startup.

## MCP request flow

`runMCP` will keep its existing JSON-RPC boundary and add the following order:

1. resolve the repository and context directory;
2. run boot upkeep once;
3. parse each request, preserving notification behavior and parse errors;
4. canonicalize legacy tool aliases;
5. skip refresh only for `graft_check_freshness` and explicit refresh-disabled
   conditions;
6. refresh one graph or workspace children before retrieval;
7. dispatch to the native single-graph or federated query core;
8. emit exactly one JSON-RPC response on stdout.

Any unexpected internal error is converted to the existing soft tool error.
The process must not write diagnostics to stdout.

## Error and compatibility policy

- Preserve tool `isError` distinctions: a tool-level soft error is not a JSON-
  RPC transport error.
- Preserve `-32700` parse errors, `-32601` unknown methods, notification silence,
  and response IDs.
- Preserve current missing-graph, missing-child, stale, busy-lock, and
  unsupported-refresh wording where the TypeScript contract defines it.
- Use atomic writes for graph and sidecar replacement; remove temporary files
  on failure.
- Never make a failed refresh or upkeep action fail a query or handshake.
- Preserve existing GraphV1 JSON field names and optional/null behavior.

## Implementation order

Each step starts with a failing Go contract test, then the smallest native
implementation, followed by the TypeScript differential case where the public
contract is shared.

1. Add source-walker, decoder, fingerprint, and sidecar contracts.
2. Add TypeScript/JavaScript tree-sitter extraction and deterministic graph
   build output.
3. Add graph check, refresh locking, worktree seeding, and graph-only rebuild.
4. Add workspace loading, child refresh, federated query/report functions.
5. Add upkeep cache/stamp behavior and native host target writers.
6. Wire the MCP server and add the complete TS↔Go differential matrix.
7. Record verified commands and remaining language-adapter gaps in
   `docs/go-migration-plan.md`; do not remove the TypeScript implementation.

## Validation

Required evidence for the slice:

- Go contract tests for every new package and failure path;
- TS↔Go comparison of JSON, human output, exit/error state, stderr, and
  side-effects for clean, invalid, missing, stale, drifted, added, removed,
  worktree, workspace, and busy-lock cases;
- `npm test`;
- `go test -race ./...`;
- `go build ./...`;
- `go vet ./...`;
- clean `gofmt -l .` and scoped `go fix -diff`;
- `golangci-lint run ./...`, naming checks, review checks, and documentation
  checks with pre-existing findings separated from new findings.

The migration slice is complete only when the native path passes its acceptance
matrix. Compilation alone does not mark refresh, federation, or upkeep complete.

## Non-goals

- removing or disabling the TypeScript CLI;
- changing the npm package entrypoint or deployment cutover;
- migrating the browser viewer;
- changing public tool names or adding new MCP tools;
- implementing Python, Go, or other language extractors before the planned
  TypeScript/JavaScript adapter slice is verified;
