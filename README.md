<div align="center">

<img src="assets/graft-hero.png" alt="Graft — open-source context layer for large codebases" width="100%"/>

### Turbocharge Claude Code, Cursor, Codex, Gemini & every coding agent: faster, cheaper, with contextual understanding specific to your codebase.

<a href="https://trendshift.io/repositories/92209?utm_source=trendshift-badge&utm_medium=badge&utm_campaign=badge-trendshift-92209" target="_blank" rel="noopener noreferrer"><img src="https://trendshift.io/api/badge/repositories/92209/daily?language=Go" alt="trailhq/Graft | Trendshift" width="250" height="55"/></a>

<p>
  <a href="https://github.com/NanoNets/Graft"><img src="https://img.shields.io/github/stars/NanoNets/Graft?style=for-the-badge&logo=github&logoColor=white&label=Star%20on%20GitHub&color=FFC83D" /></a>
  <a href="https://trailhq.com/graft"><img src="https://img.shields.io/badge/website-trailhq.com/graft-E5484D?style=for-the-badge" /></a>
  <a href="https://discord.gg/zxmKweAA29"><img src="https://img.shields.io/badge/Discord-join-5865F2?style=for-the-badge&logo=discord&logoColor=white" /></a>
  <a href="https://www.npmjs.com/package/@nanonets/graft"><img src="https://img.shields.io/npm/v/%40nanonets%2Fgraft?style=for-the-badge&logo=npm&logoColor=white&label=npm" /></a>
  <a href="https://www.npmjs.com/package/@nanonets/graft"><img src="https://img.shields.io/npm/dm/%40nanonets%2Fgraft?style=for-the-badge&logo=npm&logoColor=white&label=downloads" /></a>
  <a href="https://nodejs.org"><img src="https://img.shields.io/node/v/%40nanonets%2Fgraft?style=for-the-badge&logo=nodedotjs&logoColor=white" /></a>
  <img src="https://img.shields.io/badge/Go-1.27-00ADD8?style=for-the-badge&logo=go&logoColor=white" />
  <img src="https://img.shields.io/badge/License-MIT-20C997?style=for-the-badge" />
  <a href="TELEMETRY.md"><img src="https://img.shields.io/badge/telemetry-anonymous%2C%20opt--out-546FFF?style=for-the-badge" /></a>
  <a href="https://scorecard.dev/viewer/?uri=github.com/NanoNets/Graft"><img src="https://img.shields.io/ossf-scorecard/github.com/NanoNets/Graft?style=for-the-badge&label=openssf%20scorecard" /></a>
  <a href="https://app.trailhq.com/get-started?step=pick"><img src="https://img.shields.io/badge/Trail%20Brain-try%20it-E5484D?style=for-the-badge&logoColor=white" /></a>
</p>

### Up to **4× cheaper** and **3× faster**, with better or no loss of correctness.

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
  <a href="https://app.trailhq.com/get-started?step=pick"><img src="https://img.shields.io/badge/Try%20Trail%20Brain%20%E2%86%92-E5484D?style=for-the-badge" alt="Try Trail Brain" height="34"/></a>
</p>

<p align="center">
  <img src="assets/graft-comparison-demo.gif" alt="Side-by-side comparison of a coding agent working with and without graft" width="820"/>
</p>

---

## Contents

- [Quick start](#quick-start)
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
- [Monorepos & multi-repo folders](#monorepos--multi-repo-folders)
- [Tested on your popular repos](#tested-on-your-popular-repos)
- [Development](#development)
- [Go migration](#go-migration)
- [License](#license)

---

## Quick start

```bash
npm install -g @nanonets/graft   # install the CLI, once
graft init                       # build the graph + wire it into Claude Code
```

That is the whole setup. `graft init` asks which of your coding agents to wire up, builds `graft/` from your code, and drops a statusline and hooks into `.claude/`, so from the next session on Graft rides along in Claude Code: it injects matching graph pointers into relevant prompts and rebuilds the graph in the background after every turn. No daemon, no re-indexing to remember, nothing to run or maintain by default — the graph is just files.

Nothing is written until you pick. Run `graft init --dry-run` to see every file it would touch first, or `graft init --agents claude` to skip the prompt and wire Claude Code alone.

`graft build` adds `graft/` to your `.gitignore` automatically — the graph is a local, regenerable cache (like `node_modules`), not something you commit. What you share is the wiring `init` dropped into `.claude/`; each teammate runs `graft build` to generate their own graph:

```bash
git add .claude && git commit -m "wire in graft"
```

Prefer not to install globally? `npx @nanonets/graft init` works the same way.

<p align="center">
  <img src="assets/graft-terminal.png" alt="Two commands — npm install and graft init — then Graft rides along in a Claude Code session, statusline synced" width="820"/>
</p>

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
- **A local cache, not a committed artifact.** `graft build` writes `graft/` and adds it to `.gitignore` — it is regenerable like `node_modules`. What you commit is the small wiring `graft init` adds to your agent configuration.
- **Always fresh, automatically.** Query commands refresh the structural graph against the working tree before answering, so uncommitted edits are included. `graft check` reports the remaining drift without changing files.
- **No model required.** `graft build`, `check`, `ask`, `grep`, `callers`, `skeleton`, `map`, and `blast` are local and deterministic. Only `graft blast --name` can make an optional one-call LLM request to label the affected areas.

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

- **On your machine, no key, no network:** every structural command, including `graft build`, `check`, `ask`, `grep`, `callers`, `skeleton`, `map`, and `blast`.
- **Optional provider call:** `graft blast --name` may use `GRAFT_API_KEY` (or the supported provider fallbacks) to label affected areas with one cached request. Without a key, Graft names areas after their hub symbols.
- **Anonymous usage stats** — a daily npm version check and one batched usage ping. The ping carries buckets and fixed labels only: never code, paths, repository names, symbols, queries, or error messages. [`TELEMETRY.md`](TELEMETRY.md) is the complete contract; `graft telemetry debug` prints exactly what would be sent. Turn it off with `graft telemetry disable`, `DO_NOT_TRACK=1`, or the `graft init` prompt.

See [`.env.example`](.env.example) for the full list of settings (model, base URL, graph directory).

---

## Agent integration

One command wires Graft into the coding agents you use:

```bash
npx @nanonets/graft init
# detects your agents and writes each one's native instruction file;
# Claude Code additionally gets the live statusline + hooks below
```

On a terminal, `init` shows you every agent it knows about — flagging the ones it detected (via their config directories) and listing the exact files each would write — and wires only the ones you select. Claude Code is pre-selected; nothing else is. Selected agents get a marker-fenced Graft section in their shared instruction file — `AGENTS.md` (Codex, OpenCode and other CLIs that read it), `GEMINI.md`, `.github/copilot-instructions.md` — or a wholly-owned rule/skill file for the agents that use one: `.claude/skills/graft/SKILL.md`, `.cursor/rules/graft.mdc`, `.kiro/steering/graft.md`, `.windsurf/rules/graft.md`, `.grok/skills/graft/SKILL.md` for Grok (xAI), `.adal/skills/graft/SKILL.md` for [AdaL](https://adal.sylph.ai). Claude Code is in the second group: `init` writes its own skill file and never touches your `CLAUDE.md`. Re-running only updates Graft's own section (or replaces the owned file) and never touches the rest of your content.

With no TTY to prompt on — CI, a Dockerfile, a piped shell — `init` writes **nothing** and prints the command to run instead. Pass `--agents <ids>` or `--yes` to make a scripted run explicit.

| Flag | Effect |
|---|---|
| `--agents <ids...>` | wire only these, no prompt — ids: `agents`, `cursor`, `gemini`, `grok`, `copilot`, `kiro`, `windsurf`, `adal`, `claude` |
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

Register it by hand if your agent needs it explicit:

```json
{ "mcpServers": { "graft": { "command": "npx", "args": ["-y", "@nanonets/graft", "mcp"] } } }
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
graft build [dir]                    # build graft/ from the code at [dir]: wiring graph + per-file cards
graft build --extensions .ts .py     # only include these code extensions
graft build --no-reuse               # re-parse every file instead of replaying unchanged ones from cache
graft build --follow-submodules      # include initialized submodules; persist the choice for builds + MCP refresh
graft build --no-follow-submodules   # exclude submodules again and persist that choice (the default)
graft build --follow-nested-repos    # include nested git clones the index doesn't track; persist the choice
graft build --no-follow-nested-repos # exclude nested clones again and persist that choice (the default)

graft ask "<task>" [dir]             # query the graph — ranked nodes + exact file:line (no LLM, no key)
graft ask "<task>" --json            # machine-readable result
graft ask "<task>" --in <scope>      # narrow to one sub-project of a monorepo/multi-repo folder (see below)

graft skeleton <file> [dir]          # every signature in one file, no bodies — the API surface for ~1/10th the tokens (no LLM, no key)

graft callers <symbol> [dir]         # who calls/references/imports/implements/extends a symbol (no LLM, no key)
graft callers <symbol> --direction out  # the reverse: what the symbol itself calls/references (was `graft callees`)
graft callers <symbol> -d N          # walk transitively out to depth N — full blast radius (was `graft impact`)

graft grep "<regex>" [dir]           # exhaustive regex search over indexed files, grouped by enclosing symbol (no LLM, no key)
graft grep "<regex>" --in <path>     # narrow to files at or under this path prefix
graft grep "<regex>" -i --fixed      # case-insensitive; treat the pattern as a literal string, not a regex

graft map [dir]                      # token-budgeted repo orientation — dir clusters, hubs, hotspots (no LLM, no key)
graft map --max-dirs N               # raise/lower the number of directories shown

graft blast [dir]                    # blast radius of a diff: what depends on the lines this change touched (no LLM, no key)
graft blast --base origin/main       # diff against the merge base with HEAD — what a PR job runs
graft blast --format markdown        # a PR comment: the areas a change can reach, per-symbol detail collapsed under it
graft blast --base origin/main --name  # name those areas with one optional cached LLM call
graft blast --no-owners              # skip "who to tag" — by default git history names the people behind each area
graft blast --depth all --format json  # the full transitive closure, machine-readable

graft check [dir]                    # fail (exit 1) if graft/ has drifted from the code (never auto-refreshes — it's the drift report)
graft check --json                   # print the drift report as JSON

# ask / skeleton / callers / grep / map / blast all refresh the graph first if the working tree moved:
#   --no-refresh                     # answer from the graph exactly as it is on disk
#   GRAFT_NO_REFRESH=1               # same, for every command
#   GRAFT_REFRESH=hash               # hash every file instead of trusting size+mtime

graft init [dir]                     # pick which agents to wire (prompts on a terminal; writes nothing until you choose)
graft init --dry-run                 # list every file it would touch, then exit
graft init --agents cursor kiro      # wire only these agents, no prompt (ids: agents, cursor, gemini, grok, copilot, kiro, windsurf, adal, claude)
graft init --yes                     # no prompt; wire every detected agent
graft init --no-global               # skip writes outside this repo (~/.codex/ config + hooks)
graft init --no-statusline           # skip Claude Code statusLine (same as GRAFT_NO_STATUSLINE=1)
graft init --no-build                # wire the files only; don't build the graph
graft init --all-agents              # wire every known agent, detected or not
graft init --list-agents             # list known agent ids and exit

graft uninstall [dir]                # remove every file and config entry graft wrote here (the inverse of init)
graft uninstall -y                   # actually remove (without -y it prints what it would remove and exits)
graft uninstall --keep-cache         # wiring only; leave graft/ and the .gitignore entry
graft uninstall --no-global          # leave out-of-repo files alone (~/.codex, ~/.gemini)

graft version                        # print the installed + latest published npm version
graft upgrade                        # npm install -g the latest published version
                                     # a new version is announced automatically (checked once a day);
                                     # after upgrading, the next session refreshes this repo's wiring itself

# global
graft --dir <path>                   # use a context dir other than <repo>/graft
graft --version, -v                  # print the installed version and exit
```

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

The published package is a thin npm launcher around the native Go binary. Go 1.27 is required to build from source.

```bash
git clone https://github.com/NanoNets/context-graph-engine.git && cd context-graph-engine
npm install
npm run build       # builds bin/graft-<platform>-<arch>
npm test            # plain-JS launcher, shim, and postinstall tests

go build ./...
go test ./...
```

The Go gate is authoritative for product behavior: `go build ./...`, `go vet ./...`, `go test -race ./...`, `golangci-lint run ./...`, `gofmt -l .`, and `go fix -diff ./...`.

---

## Go migration

The migration is complete. Go is the only implementation behind the CLI, MCP server, host hooks, statusline, graph extraction, upkeep, and telemetry. The former TypeScript behavior was frozen into Go-owned golden fixtures before removal; `docs/cli-contract.json` is now checked directly against `programSpec` and the Go environment inventory.

The package contains only `bin/graft.js`, the platform-native binary, the postinstall script, and package metadata. There is no JavaScript library API, viewer, GitHub App, deep meaning layer, or TypeScript fallback.

---

## License

MIT. See [LICENSE](LICENSE).
