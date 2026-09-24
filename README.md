<div align="center">

<img src="./a328474a-ba6a-42b1-9746-a1358d0a449d.png" alt="Graft" width="320" />

### Native Go context for Claude Code, Cursor, Codex, Gemini, and every coding agent

<p><strong>Build a deterministic map of your codebase locally. Find the right code with less searching, reading, and token spend.</strong></p>

<p>
  <img src="https://img.shields.io/badge/Go-1.27-00ADD8?style=for-the-badge&logo=go&logoColor=white" alt="Go 1.27" />
</p>

### Up to **4× cheaper** and **3× faster**, with better or no loss of correctness

| Metric | Cold Claude Code | Claude Code with graft |
|---|---|---|
| Tool-call reduction | Baseline | **+46%** |
| Token savings | Baseline | **+42%** |
| Time savings | Baseline | **+60%** |
| Correctness | 54% | **66% (+12 pts)** |

</div>

<p align="center">
  <b>This works beyond code too.</b><br/>
  A living skill file that learns from every task and gets sharper the more your team works.
</p>

<p align="center">
  <img src="assets/graft-comparison-demo.gif" alt="Side-by-side comparison of a coding agent working with and without graft" width="820"/>
</p>

---

## Contents

- [Installation and quick start](#installation-and-quick-start)
- [The problem](#the-problem)
- [What Graft does](#what-graft-does)
- [Benchmark](#benchmark)
- [SWE-bench Verified](#swe-bench-verified)
- [How the graph gets built](#how-the-graph-gets-built)
- [Supported languages](#supported-languages)
- [What's in the graph](#whats-in-the-graph)
- [What runs where](#what-runs-where)
- [Agent integration](#agent-integration) — [MCP server](#mcp-server) · [Claude Code](#claude-code)
- [CLI](#cli)
- [Search & orient](#search--orient-graft-grep--graft-map) (`graft grep` / `graft map`)
- [Monorepos, submodules & multi-repo folders](#monorepos-submodules--multi-repo-folders)
- [Tested on your popular repos](#tested-on-your-popular-repos)
- [Development](#development)
- [Native Go architecture](#native-go-architecture)
- [License](#license)

---

## Installation and quick start

Graft is a native Go application. The recommended installation does not require Node.js.

### Install with Go

Requirements: **Go 1.27 or newer** and a working C toolchain, because the Tree-sitter bindings use cgo. Make sure `$(go env GOPATH)/bin` is on your `PATH`.

```bash
go install github.com/h0rn3t/Graft/cmd/graft@latest
graft init
```

`graft init` asks which coding agents to wire, builds the local graph, and adds the selected agent instructions, MCP entry, hooks, and statusline where supported. It requires no API key: the full structural workflow is local and deterministic.

Nothing is written until you choose an agent. Preview the plan with:

```bash
graft init --dry-run
```

You can also wire a specific host without a prompt:

```bash
graft init --agents claude
```

`graft build` adds `graft/` to `.gitignore` automatically. The graph is a regenerable local cache, while the small agent wiring created by `init` is what belongs in version control. For Claude Code, for example:

```bash
git add .claude .mcp.json .gitignore
git commit -m "wire in graft"
```

Each teammate generates their own local graph with `graft build` or `graft init`.

To update a Go installation, run the same `go install ...@latest` command.

### npm distribution (alternative)

If you prefer the prebuilt npm package, it still runs the same native Go binary:

```bash
npm install -g @nanonets/graft
graft init
```

Node.js 20 or newer is required only by this npm launcher. A one-off run is also available:

```bash
npx -y @nanonets/graft@latest init
```

---

## The problem

Every task, your coding agent starts blind. Before it changes anything, it re-explores the repo: grep a term, open a file, follow an import, back out, try again. It is rebuilding a picture of a codebase it mapped an hour ago and threw away. That rediscovery burns most of a run's tool calls, tokens, and latency, and it is pure overhead:

- **Repeated.** Every task pays the exploration cost again, from zero.
- **Discarded.** Whatever the agent figured out dies with the session.
- **Unshared.** The next teammate, and their agent, start from scratch too.

Humans onboard to a codebase once. Agents onboard every single time.

<p align="center">
  <img src="assets/graft-site-act-demo.gif" alt="A no-map agent's exploration trail wandering file to file before it finds what it needs" width="820"/>
</p>

---

## What Graft does

Graft builds a deterministic structural map of your codebase once and writes it into your repo as a regenerable local cache.

- **Symbols and real wiring.** Tree-sitter extracts functions, classes, types, imports, calls, and inheritance; Graft resolves them into an exact file-and-edge graph.
- **A local cache, not a committed artifact.** `graft build` writes `graft/` and adds it to `.gitignore` — it can be deleted and regenerated at any time. What you commit is the small wiring `graft init` adds to your agent configuration.
- **Always fresh, automatically.** Query commands refresh the structural graph against the working tree before answering, so uncommitted edits are included. `graft check` reports the remaining drift without changing files.
- **No model required.** `graft build`, `check`, `ask`, `grep`, `callers`, `skeleton`, `map`, and `blast` are local and deterministic.

<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/graft-cold-vs-graft-dark.png">
    <img src="assets/graft-cold-vs-graft.png" alt="The same task, 'fix the auth bug', run two ways. A cold Claude Code session re-reads the repo and wanders file to file; Claude Code + graft loads its map once and rides the hooks to one clean pass. With Graft: 46% fewer tool calls, 42% fewer tokens, 60% less time, +22% more SWE-bench instances resolved." width="880"/>
  </picture>
</p>

---

## Benchmark

An agent that reads the graph should be cheaper and faster without getting more answers wrong. That's the whole claim, so we measured it instead of asserting it.

The harness ran three variants of the same Claude Sonnet 5 agent with the same file tools: **cold** (explores from zero), **Graft** (a `graft ask --source` bundle pushed up front), and **pull** (graft_find_code/graft_file_api tools, nothing injected — context paid for only when asked). An Opus 4.8 judge scored correctness with a required-keyword floor, so a fast-but-wrong answer couldn't win by being fast. Cost is cache-aware: reads ≈0.1×, writes 1.25×, the billing model agents actually run under.

162 runs, two repos (graft itself and a real Node/Express auth service), 3 trials each, tasks split between single-file and multi-file questions.

| Metric (mean/task) | Cold Claude Code | Claude Code with graft |
|---|---|---|
| Cost savings ($) | 0.0429 | **0.0292 (+32%)** |
| Token savings | 8,070 | **4,650 (+42%)** |
| Tool-call savings | 4.2 | **2.3 (+46%)** |
| Latency savings (s) | 39.8 | **15.8 (+60%)** |
| Correctness | 93% | 93% (equal) |

Graft never answered worse than cold, on any corpus. The pull variant gave up most of that speed for something bigger: correctness jumped to 98%, +5 points over cold, the strongest single result in the sweep. Push when speed is what you need; pull when being right matters more.

---

## SWE-bench Verified

The sweep above is our harness measuring our mechanism. So we ran the industry-standard one too — **SWE-bench Verified**, real GitHub issues from real repos, graded by the official `swebench` harness. No judge model, no similarity score: your patch is applied, the maintainers' own tests are run, and you either flip the failing test without breaking the passing ones or you don't.

**50 instances**, same model on both arms — **Claude Sonnet 5** — same Docker images, same turn limits. The only difference is whether graft is wired in.

| Correctness & efficiency | Cold Claude Code | Claude Code with graft | Improvement |
|---|---|---|---|
| Correctness | 27 / 50 (54%) | **33 / 50 (66%)** | **+12 pts** |
| Token savings | 142.0M | **109.4M** | **+23%** |
| Cost savings | $52.34 | **$42.43** | **+19%** |
| Tool-call savings | 1,370 | **1,031** | **+25%** |
| API-request savings | 2,455 | **1,875** | **+24%** |
| Wall-clock savings | 13,094s | **8,922s** | **+32%** |

graft resolved **33 of 50 instances** against Cold Claude Code's 27 — and got there with 25% fewer tool calls, 23% fewer tokens, and 32% less wall-clock time. Every correctness win has the same shape: the baseline patches one file and misses its siblings. On `django-11532` it patched 1 of the 5 files the fix requires and broke 18 previously-passing tests, twice over. On `django-16263` it patched 1 of 4 and scored 102 / 103. graft found the rest — and on `django-16263` did it in half the tokens and half the time.

Two harnesses, two claims: the controlled sweep says graft is cheaper and faster, SWE-bench says it's also more correct.

<sub>Correctness over all instances; tokens, cost and calls over the instances both arms resolved, for a like-for-like comparison. Official SWE-bench Verified images and official `swebench` 4.1.0 grader, native x86_64.</sub>

---

## How the graph gets built

Graft builds a structural graph entirely on your machine:

1. **Parse supported source files** with tree-sitter and extract symbols, spans, signatures, imports, calls, and inheritance.
2. **Resolve the graph** across files, scopes, monorepo workspaces, and supported import forms.
3. **Write two views**: `graft/.graph/wiring.json` for machine queries and per-file markdown cards for agents to read and grep.

```mermaid
flowchart LR
    S[Source files] --> T["tree-sitter extraction<br/>no model, no network"]
    T --> R["Cross-file resolution"]
    R --> W["graft/.graph/wiring.json"]
    R --> C["graft/*.md cards"]
```

Every parse is cached by content hash, so a second build only re-reads files that changed. `graft build --no-reuse` forces a cold parse.

That cheapness is what lets **every query refresh the graph before it answers**. Graft compares the working-tree bytes with its fingerprint and rebuilds only when something moved, so `ask`/`grep`/`callers`/`skeleton`/`map`/`blast` describe uncommitted, unstaged, and staged edits alike. Turn refresh off per command with `--no-refresh`, or globally with `GRAFT_NO_REFRESH=1`.

---

## Supported languages

Graft's native extractor is deterministic and local. It supports:

- **Go**: `.go`
- **Python**: `.py`, `.pyi`
- **TypeScript / JavaScript**: `.ts`, `.tsx`, `.mts`, `.cts`, `.js`, `.jsx`, `.mjs`, `.cjs`
- **Java**: `.java`
- **Rust**: `.rs`
- **PostgreSQL**: `.sql`
- **C**: `.c`, `.h`
- **C++**: `.cpp`, `.cc`, `.cxx`, `.hpp`, `.hh`

`graft build --lsp` can add compiler-grade call edges when `rust-analyzer`, `clangd`, `gopls`, `pyright`, or `typescript-language-server` is installed. A file with an unsupported extension is skipped and reported rather than guessed.

- **Generic extraction** — Rust, C, and C++ use tags queries for symbols and calls.
- **Compiler-grade edges (opt-in)** — `graft build --lsp` adds precise
  `lsp_resolved` call edges (member calls the static pass can't type) when a
  language server is on your `PATH`: `rust-analyzer` (Rust), `clangd` (C/C++),
  `gopls` (Go), `pyright` (Python), `typescript-language-server` (TS/JS).
  It's best-effort — with no server installed the graph is unchanged.

---

## What's in the graph

Each node records the symbol's identity and exact source location:

- **ID and name** — stable, file-scoped identity such as `internal/graph/build.go#runBuild`
- **Kind** — file, function, method, class, struct, interface, enum, type, or variable
- **Path and span** — exact file and `Lstart-Lend` range
- **Signature** — declaration text without the body where the grammar provides it
- **Exported flag** — whether the declaration is public in its language
- **Body hash and searchable body** — deterministic change detection and local retrieval
- **Edges** — `contains`, `calls`, `references`, `imports`, `extends`, and `implements`

The same graph is serialized to `graft/.graph/wiring.json` and rendered as per-file markdown cards under `graft/`. Both views are regenerated by `graft build`; neither contains generated prose.

---

## What runs where

- **Everything runs on your machine, with no key and no network:** every command, including `graft build`, `check`, `ask`, `grep`, `callers`, `skeleton`, `map`, `blast`, and the MCP server. Graft itself makes no network calls: no LLM provider, no usage telemetry, no update check.

See [`.env.example`](.env.example) for local graph, refresh, and host-wiring settings.

---

## Agent integration

One native Go command wires Graft into the coding agents you use:

```bash
graft init
# detects your agents and writes each one's native instruction file;
# Claude Code additionally gets the live statusline + hooks below
```

If Graft is available only through npm, use `npx -y @nanonets/graft@latest init` instead.

On a terminal, `init` shows you every agent it knows about — flagging the ones it detected (via their config directories) and listing the exact files each would write — and wires only the ones you select. Claude Code is pre-selected; nothing else is. Selected agents get a marker-fenced Graft section in their shared instruction file — `AGENTS.md` (generic agents, Codex, Hermes, Antigravity, and other CLIs that read it), `GEMINI.md`, `.github/copilot-instructions.md` — or a wholly-owned rule/skill file for the agents that use one: `.claude/skills/graft/SKILL.md`, `.cursor/rules/graft.mdc`, `.kiro/steering/graft.md`, `.windsurf/rules/graft.md`, `.grok/skills/graft/SKILL.md` for Grok (xAI), `.adal/skills/graft/SKILL.md` for [AdaL](https://adal.sylph.ai). Claude Code is in the second group: `init` writes its own skill file and never touches your `CLAUDE.md`. Re-running only updates Graft's own section (or replaces the owned file) and never touches the rest of your content.

With no TTY to prompt on — CI, a Dockerfile, a piped shell — `init` writes **nothing** and prints the command to run instead. Pass `--agents <ids>` or `--yes` to make a scripted run explicit.

| Flag | Effect |
|---|---|
| `--agents <ids...>` | wire only these, no prompt — ids: `agents`, `adal`, `cursor`, `gemini`, `grok`, `hermes`, `antigravity`, `copilot`, `kiro`, `windsurf`, `claude` |
| `--yes`, `-y` | skip the prompt and wire every **detected** agent |
| `--dry-run` | print every file `init` would touch, then exit without writing |
| `--all-agents` | write instruction files for every known agent, detected or not |
| `--no-agents` | Claude Code wiring only; skip other agents |
| `--list-agents` | print the known agent ids and exit |
| `--no-mcp` | skip MCP server registration |
| `--no-hooks` | skip hook installation |
| `--no-statusline` | skip writing Claude Code `statusLine` (same as `GRAFT_NO_STATUSLINE=1`) |
| `--no-global` | skip writes outside this repo (the `~/.codex/` entries below) |

#### Writes outside the repo

Selecting the `agents` host also touches your **user-level** Codex config, when `~/.codex/` exists:

| Path | What changes |
|---|---|
| `~/.codex/config.toml` | registers the Graft MCP server (`[mcp_servers.graft]`) |
| `~/.codex/hooks/graft/graft-hooks.cjs` | the post-edit hook shim |
| `~/.codex/hooks.json` | a `PostToolUse` entry matching `Write\|Edit\|MultiEdit` |

Both configs are user-level, so they apply to **every** repo you open with Codex, not just this one. The picker labels these `machine-wide`, `--dry-run` lists them in their own section, and `--no-global` skips them while still wiring `AGENTS.md`.

### MCP server

`graft init` also registers Graft's MCP server with agents that support it, so these six tools appear natively, no shell required. Claude Code gets this too: `graft init` writes the server into the project's `.mcp.json` (restart Claude Code to load it). Skip with `--no-mcp`; run it manually with `graft mcp [dir]`.

| Tool | Takes | What it's for |
|---|---|---|
| `graft_find_code` | a question | Ranked nodes with file:line, source inlined — usually the full answer, no follow-up read needed. |
| `graft_file_api` | a file path | Every signature in that file, no bodies — the API surface for a tenth of the tokens. |
| `graft_trace_calls` | a symbol | Who depends on it, or what it depends on with `direction: out`, N levels deep for blast radius. |
| `graft_find_all` | a regex | Every hit, grouped by enclosing symbol, ranked by how coupled that symbol is. |
| `graft_repo_map` | nothing | A first look at an unfamiliar repo: directory clusters, hubs, hotspots. |
| `graft_check_freshness` | nothing | Whether the local graph has drifted from the code. |

Register it by hand if your agent needs it explicit and `graft` is on `PATH`:

```json
{ "mcpServers": { "graft": { "command": "graft", "args": ["mcp"] } } }
```

For an npm-only installation, use:

```json
{ "mcpServers": { "graft": { "command": "npx", "args": ["-y", "@nanonets/graft@latest", "mcp"] } } }
```

Where a CLI agent supports user-level `hooks.json`, `init` also installs Graft's post-edit hook — blast-radius warnings and automatic `$0` graph re-sync after edits (skip with `--no-hooks`).

### Claude Code

`graft init` always wires up Claude Code, and Claude Code gets more than the skill file above. From then on, any Claude Code session opened in the repo gets:

- **a live statusline** — graph size, freshness, context usage, and a stale warning when the code has moved ahead of the graph
- **auto-sync** — every graft query brings the graph up to date first, so an answer always describes the code as it is right now, uncommitted edits included. A query refreshes only what it reads; the markdown under `graft/` is refreshed by the background rebuild at the end of a turn that touched code. Both are structural and `$0` — auto-sync never calls the LLM on its own
- **context on tap** — each prompt pulls the matching nodes into the session; editing a file surfaces what depends on it ("blast radius"); new sessions start with the repo map

<p align="center">
  <img src="assets/graft-hooks-demo.gif" alt="How Claude Code hooks wire graft in: install, graft init, then the hooks loop (session start, user prompt, post tool use, stop) keeps the graph built, read, and committed automatically" width="820"/>
  <br/><sub>install → init → hooks keep the graph fresh every session</sub>
</p>

`graft init` is idempotent and never clobbers your existing `.claude/settings.json` — it merges its blocks and leaves the rest alone. A `statusLine` that is not Graft's (anything whose command does not name `graft-statusline.cjs`) is left untouched; re-running `init` refreshes Graft's own helper if it is already installed. Pass `--no-statusline` (or `GRAFT_NO_STATUSLINE=1`) to skip installing one — a project-level `statusLine` would otherwise hide a custom one in `~/.claude/settings.json`.

---

## CLI

```bash
graft build [dir]                    # build the graph and per-file cards
graft build --extensions .ts .py     # restrict source extensions
graft build --include-dir <name>     # re-include a normally excluded dot-directory
graft build --only-dir <path>        # build one repository-relative subtree
graft build --lsp                    # add best-effort compiler-resolved call edges
graft build --no-reuse               # re-parse every file instead of replaying the extraction cache
graft build --follow-submodules      # include initialized submodules and persist the choice
graft build --no-follow-submodules   # restore the default submodule boundary
graft build --follow-nested-repos    # include nested git clones not tracked by the superproject
graft build --no-follow-nested-repos # restore the default nested-repository boundary
graft build --no-gitignore           # do not add graft/ to .gitignore
graft build --no-ignore              # do not add graft's re-include entries to .ignore

graft ask "<task>" [dir]             # ranked nodes with exact file:line (no LLM, no key)
graft ask "<task>" --source          # inline source excerpts; add --full for complete definitions
graft ask "<task>" -n 10 --json      # limit hits and return machine-readable JSON
graft ask "<task>" --in <scope>      # narrow to one scope in a monorepo or multi-repo folder

graft skeleton <file> [dir]          # signatures only: the cheapest view of a file's API
graft skeleton <file> --json         # machine-readable signatures
graft callers <symbol> [dir]         # who calls or references a symbol
graft callers <symbol> --direction out  # what that symbol calls or references
graft callers <symbol> -d all        # transitive dependencies; -d N sets an exact depth
graft grep "<regex>" [dir]           # exhaustive search grouped by enclosing symbol
graft grep "<regex>" --in <path>     # restrict the search to a path prefix
graft grep "<regex>" -i --fixed      # case-insensitive literal search
graft map [dir]                      # directory clusters, local hubs, and global hotspots
graft map --max-dirs N --json        # choose the detail level and return JSON
graft blast [dir]                    # structural blast radius of the working-tree diff
graft blast --base origin/main       # compare the merge base with HEAD, as a PR check would
graft blast --format markdown        # text, markdown, mermaid, or json
graft blast --no-owners              # skip reviewer suggestions derived from git history
graft check [dir]                    # exit 1 when the graph is missing or stale; never writes
graft check --json                   # machine-readable drift report
graft stats [dir]                    # session usage mix and estimated tokens saved
graft stats --json
graft mcp [dir]                      # serve the six graph tools over MCP stdio

graft init [dir]                     # pick which agents to wire; nothing is written before confirmation
graft init --dry-run                 # list every file that would be touched
graft init --agents claude cursor    # wire only these hosts without prompting
graft init --yes                     # wire every detected host
graft init --all-agents              # wire every known host
graft init --list-agents             # print the host ids
graft init --no-mcp                  # skip MCP registration
graft init --no-hooks                # skip host hook installation
graft init --no-statusline           # skip the Claude Code statusline
graft init --no-global               # skip user-level host configuration
graft init --no-build                # wire files without building the graph

graft uninstall [dir]                # preview removal; add -y to apply
graft uninstall -y --keep-cache      # remove wiring but retain graft/ and ignore entries
graft uninstall -y --no-global       # retain user-level host configuration

graft version                        # print the installed version

# global options
graft --dir <path> <command>         # use a context directory other than <repo>/graft
graft --version, -v                  # print the installed version
```

`ask`, `skeleton`, `callers`, `grep`, `map`, and `blast` refresh changed source before answering. Use `--no-refresh` or `GRAFT_NO_REFRESH=1` to query the graph exactly as stored; set `GRAFT_REFRESH=hash` to verify files by content instead of size and mtime.

To update, re-run the installation command: `go install github.com/h0rn3t/Graft/cmd/graft@latest`, or `npm i -g @nanonets/graft@latest` for the npm distribution.

Method calls resolve through the receiver's type — constructor assignments
(`self.router = APIRouter()`) and type annotations, not just the call-site
name — so `callers`/`grep --in` return calls bound to the right
type on method-heavy code, not every method anywhere with that name.

## Search & orient (`graft grep` / `graft map`)

`graft grep "<regex>"` is exhaustive over every indexed file and groups hits
by enclosing symbol, ranked by the same in-edge coupling `graft map` uses —
built for "every occurrence of this pattern" tasks where `graft ask`'s
ranked top-N isn't enough:

```
"NEEDLE" — 2 hits in 2 symbols across 1 files (searched 1 indexed files)

heavilyCalled · function · src/a.ts:L1-L3 · 3 in-edges
  L2: console.log("NEEDLE hit in heavilyCalled");

rarelyCalled · function · src/a.ts:L4-L6 · 0 in-edges
  L5: console.log("NEEDLE hit in rarelyCalled");
```

`graft map` is a token-budgeted first look at a repo — directory clusters
with file/symbol counts, each dir's local hubs, and the global hotspots —
all ranked by in-degree, no LLM, no key:

```
repo map — 94 files · 621 symbols · 1948 edges · go

cmd/graft/          32 files · 433 symbols   hubs: run (main.go, 31←), programSpec (commander.go, 18←), runWithInput (main.go, 13←)
internal/graph/     42 files · 163 symbols   hubs: BuildGraph (build.go, 12←), queryLexical (ask_lexical.go, 9←), buildIndex (resolve.go, 7←)
test/               3 files · 0 symbols

hotspots: run · function · cmd/graft/main.go:L99-L103 · 31←  programSpec · function · cmd/graft/commander.go:L114-L160 · 18←  runWithInput · function · cmd/graft/main.go:L109-L141 · 13←  ...
```

## Monorepos, submodules & multi-repo folders

Graft supports these layouts:

- **A monorepo with one `.git`** (a `pnpm-workspace.yaml`/`package.json`
  `workspaces`, or per-package `go.mod`/`pyproject.toml`/`Cargo.toml`) —
  `graft build` discovers each sub-project as a ranking scope. `ask`/`map`
  rank every scope on its own terms and fuse the results, so the biggest
  sub-project can't drown a small one; hits carry `[scope/]` labels, and
  `graft map` groups its directory clusters by scope first.
- **A Git superproject with initialized submodules** — submodules stay excluded
  by default. Run `graft build --follow-submodules` to fold initialized gitlinks
  into one graph, prefixing child paths (for example,
  `deps/parser/src/index.ts`) while honoring each submodule's own Git ignore
  rules. Visible untracked files are included too; uninitialized submodules
  remain absent until `git submodule update --init` checks them out. The choice
  is saved in `.graft/config.json`, so later no-flag builds and MCP automatic
  refreshes behave the same way. Run `graft build --no-follow-submodules` to
  restore and persist the default boundary.
- **A git repo with other repos cloned inside it** (no gitlink, no index entry) — the shape multi-repo manifest tools like `west`, `repo`, `gclient` and `tsrc` check dependencies out into, and the shape you get by cloning an upstream into the tree to patch it locally. `--follow-submodules` cannot reach these: they have no `160000` index entry to follow. Run `graft build --follow-nested-repos` to fold them into one graph, prefixing child paths (for example, `external/parser/src/index.ts`) while honoring each clone's own Git ignore rules. A clone at a git-ignored path stays absent, since Git never reports it. The choice is saved in `.graft/config.json` and is independent of `--follow-submodules` — neither flag implies the other. Run `graft build --no-follow-nested-repos` to restore and persist the default boundary. Prefer this over the multi-repo split below when the nested repos import from each other and you want those edges in one graph; prefer the split when you want each repo scored and refreshed on its own.
- **A folder of separate git repos** (no `.git` at the top) — `graft build`
  auto-splits: each child gets its own (git-ignored) `graft/`, and the parent
  gets a `graft/workspace.json` index. Queries from the parent federate across
  every child, always labeled `<child>/`. Run `graft build` inside a child to
  work on just that repo.

In every layout, narrow to one sub-project with `graft ask "<task>" --in <scope>/`
once you know where you're working.

`graft init` at the parent of a multi-repo folder wires **every child repo too**,
not just the parent — an agent session opens at a repo root and reads its
instruction files from there, so each child needs its own. A session started in
the parent gets the federated view; one started in a child sees that repo alone.

Commands also find the graph from a subdirectory: with no `[dir]` argument they
walk up to the nearest `graft/`, so `graft ask` works from a nested package
without a `cd` to the repository root.

---

## Tested on your popular repos

The [benchmarks](#benchmark) measure the mechanism. The real test is whether graft helps an agent **ship real changes** on code people actually run, not just answer questions. So we benchmark it on popular open-source repos: **15 tasks each**, 10 real developer questions plus **5 actual implementation tasks** (real merged pull requests, each re-implemented from its base commit and scored against the files the maintainers actually changed). Same agent (Claude Opus), same file tools; the only difference is whether graft is wired in.

Across these repos graft runs **up to 4× cheaper and 3× faster**, with better or no loss of correctness: it reproduces the real merged PRs by touching the same files the maintainers did. Per-repo detail below.

### PocketBase (Go, ~350 files)

| Aggregate over 15 tasks | Standard Claude Code | With graft |
|---|---|---|
| Cost | $13.91 | **$11.02 (−21%)** |
| Wall-clock | 2,044s | **1,762s (−14%)** |
| PRs reproduced | 5 / 5 | **5 / 5 (same files as the maintainers)** |

Cheaper and faster with no loss of correctness: graft reproduced all five merged PRs, touching the same files the maintainers did. The gap is widest on cross-file understanding — "how does auth work across OAuth2 providers" dropped from $2.19 to $0.84.

<details>
<summary><b>The 10 questions we asked</b></summary>

1. **Orientation** — Give me a map of PocketBase's architecture: the main subsystems and how an HTTP request flows through to the database.
2. **Entry-point trace** — Trace end-to-end what happens when a client creates a record via the REST API, from route handler to database write.
3. **Feature location** — I want to add a brand-new collection field type. Where do I hook it in, and which pieces must change?
4. **Bug localization** — Realtime subscriptions silently stop delivering events after a while. Where would you start looking, and why?
5. **Blast radius** — If I change the signature of the record-validation logic, what depends on it and what could break?
6. **Cross-file synthesis** — How does auth work across OAuth2 providers: where are tokens issued, validated, stored, and refreshed?
7. **Extensibility** — How do I use PocketBase as a Go framework to register a custom route plus an on-record-create hook?
8. **Security discovery** — Where is user input validated, and where are collection API access rules enforced before a query runs?
9. **Public API** — As an external app, how do I authenticate and then list and filter records over the REST API?
10. **Test verification** — Where are the tests for the record CRUD API, and what do they assert about access rules?

</details>

<details>
<summary><b>The 5 merged PRs we re-implemented</b></summary>

Each PR was reset to its base commit; graft's diff was scored against the files the merged PR changed.

| PR | Type | What it does | Files the maintainers touched |
|---|---|---|---|
| [#6744](https://github.com/pocketbase/pocketbase/pull/6744) | feat | Generate & serve WebP thumbnails | `apis/file.go`, `tools/filesystem/filesystem.go` |
| [#6947](https://github.com/pocketbase/pocketbase/pull/6947) | fix | Uniform char distribution in regex random strings | `tools/security/random_by_regex.go` |
| [#6690](https://github.com/pocketbase/pocketbase/pull/6690) | refactor | Patreon OAuth2 to use `x/oauth2/endpoints` | `tools/auth/patreon.go` |
| [#2726](https://github.com/pocketbase/pocketbase/pull/2726) | perf | Drop a redundant admin-count query on a hot middleware path | `apis/middlewares.go` |
| [#3192](https://github.com/pocketbase/pocketbase/pull/3192) | fix | Restore prior API rules on automigration rollback | `plugins/migratecmd/templates.go` |

</details>

<details>
<summary><b>Method</b></summary>

Two clones of PocketBase at the same commit: one wired with `graft init`, one untouched and verified graft-free. Each task run headless (`claude -p`, Claude Opus) with an empty MCP config. Understanding questions were graded by whether the answer pointed to the right files and functions; PR tasks were scored on whether the agent's diff touched the same files as the merged PR. Every transcript was audited to confirm graft was actually used in the graft arm and absent from the standard arm.

</details>

---

## Development

The product implementation is Go 1.27. Node.js is needed only for the optional npm packaging and launcher tests.

```bash
git clone https://github.com/h0rn3t/Graft.git
cd Graft

go build ./...
go test ./...
go run ./cmd/graft build .
```

The complete Go release gate is:

```bash
go build ./...
go vet ./...
go test -race ./...
golangci-lint run ./...
test -z "$(gofmt -l .)"
go fix -diff ./...
```

To verify the optional npm distribution, shims, and postinstall behavior:

```bash
npm install
npm run build       # builds bin/graft-<platform>-<arch> from Go
npm test
```

---

## Native Go architecture

Graft is one native Go application. The same binary implements the public CLI, graph extraction and queries, MCP server, host wiring, hooks, statusline, and upkeep.

- `cmd/graft` — public command surface and runtime entry points.
- `internal/graph` — deterministic extraction, resolution, cards, indexing, freshness, and workspace federation.
- `internal/hosts` — agent configuration, MCP registration, native shims, and init/uninstall behavior.
- `internal/upkeep` — wiring reconciliation when the installed version changes.
- `cmd/graft/testdata/goldens` — Go-owned regression fixtures for retained CLI behavior.

There is no TypeScript backend, JavaScript library API, browser viewer, GitHub App, deep meaning layer, or TypeScript launcher fallback. The optional npm package contains only a small launcher, the host-native Go binary, and packaging scripts.

`docs/cli-contract.json` is the public CLI contract and is checked against both `programSpec` and the Go environment inventory.

---

## License

MIT. See [LICENSE](LICENSE).
