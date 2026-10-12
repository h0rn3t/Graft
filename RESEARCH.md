# Research: how to make Graft faster, more accurate and cheaper in tokens

Date: 2026-10-12. Baseline: Graft 0.6.0-beta.4 (f18b7c3).

## What this covers and what it builds on

This note surveys code-context tools and papers for coding agents that we had not yet compared Graft with. It also adds new local measurements of Graft's own speed. The goal is a ranked list of what to build next.

It builds on the earlier comparison with codegraph, Aider's repo map, GitNexus, codebase-memory-mcp (CBM), Serena and claude-context. That comparison lives in `docs/codegraph-ideas-roadmap.md`, which is local and gitignored, and in the "Graft и аналоги" artifact. Those tools are not repeated here.

## The rule that orders everything

Our own Sonnet 5.5 analysis of 288 sessions found where the tokens go:

- 83.5% of session tokens are the fixed prompt prefix (about 11.9k) multiplied by the number of API rounds.
- Tool output that stays in context is only about 9%.
- Each round therefore costs about 13k tokens.

So the main lever on tokens, and on wall-clock time, is **fewer rounds**, not shorter answers. Haiku is the exception: for Haiku, output size matters too.

Every external source below that publishes a mechanism attacks the same thing: serial search rounds. Typical agentic search takes 10–20 serial turns (Cognition), and exploration is more than 56% of tool-use turns (FastContext).

---

## 1. Ranked recommendations

The table ranks ideas by expected effect per unit of effort. Status was checked against the code at f18b7c3.

| # | Idea | Axis | Evidence | Graft today | Effort |
|---|------|------|----------|-------------|--------|
| 1 | `read_symbol` on an ambiguous name answers with the definitions, not an error | rounds | codegraph returns all same-named definitions | `resolveReadSelector` (cmd/graft/read.go:L205) answers "ambiguous symbol (N matches); select an exact node ID". On x/tools, `read Load` lists 6 IDs and costs the agent a round | S |
| 2 | Several queries in one `find_code` call (`queries[]`) | rounds | Octocode (1–5 queries per call); SWE-grep (4→8 parallel calls cut turns 6→4); Codex prompt guide ("maximize parallelism"); Code Mode | `query` is a single string. `find_all` covers several terms with regex alternation; `read_symbol` batches with `also` | M |
| 3 | Incremental ask index | speed | Local profile, §2 | `WriteAskIndex` re-tokenizes every node on every refresh: 40% of a one-file refresh | M |
| 4 | Precision gate on the prompt hook | quality, tokens | SWE-grep and WarpGrep reward F0.5 (precision first); CodeGrep found a BM25 retriever at precision 0.375 *hurt* the agent, while one at 0.677 helped; Relace's "prefill" pattern | On HEAD, the prompt "summarize into RESEARCH.md" injects `mdText`/`mdCode`/`mdCodeCell`. The only matched term is `md`, and `coverageStrong` of 0.34 passes `askWeakMatch` | S |
| 5 | 1-hop neighbours on the top `find_code` hits (callers and callees, with call lines, hard cap k) | rounds, quality | LARGER (file Acc@5 89.1 vs 75.2 for Claude Code, 353K vs 522K tokens, 100 s vs 140 s; without the expansion 55.7 → 48.2); RepoGraph (+32.8% relative resolve rate); SpIDER (call edges +27% Recall@20); codanna | A call-flow line among the *named* symbols only, and a `methods:` line on types. Counter-evidence: callers in `find_all` group headers were rejected (3/73 Sonnet `trace_calls` targeted a shown header). Gate it on the replay kit | M |
| 6 | Skip the LSP pass on `graft build` when nothing changed | speed | Local measurement, §2 | A warm `graft build` with no changes takes 14.7 s with gopls vs 7.2 s cold without it. `carryOverLSPEdges` already carries edges on refresh, but not on build | S |
| 7 | External quality evals beyond our 88 Go queries | quality | semble bench (1,251 queries, 63 repos, 19 languages, NDCG@10; CBM 0.630, ck 0.642, probe 0.387, semble 0.854); ContextBench (1,136 tasks, human gold context); LocBench | Offline eval: 88 real Go queries, recall@limit 0.55. One more miss seen today: a query naming the error text "ambiguous … candidate IDs" did not rank `resolveReadSelector` in the top 5 | M |
| 8 | Embedding reranker over the top ~200 lexical+graph candidates (experiment only) | quality | SweRank: on issue text, BM25 36.9 → CodeRankEmbed (137M) 58.8 → SweRankEmbed-Small (137M) 74.5 function Acc@10. But semble's ablation on agent-style queries: heuristics carry 0.675 → 0.834, embeddings add only +0.02 | None. Cost: cgo/ONNX, a bundled model (Graft makes no network calls), index time | L |
| 9 | Explicit absence verdict ("no definition named X; N symbols scanned; nearest: …") and a zero-hit retry with split identifiers | rounds | jCodeMunch coverage contract; fff fuzzy retry | Did-you-mean on MCP misses (rc.6) and a `weak match` footer. Check `find_all` on zero hits | S |
| 10 | Opt-in `graft init` locator subagent (Haiku, graft tools only, returns `path:Lx-Ly` spans) | tokens, speed | Amp moved its finder to Haiku 4.5 (about 50% faster, about 3× cheaper); SWE-grep, WarpGrep, Relace and Sourcegraph Code Finder all return spans from an isolated context; FastContext (main-agent tokens up to −60%, explorer about 2%) | SubagentStart hook already gives every subagent graft's note (subagent tokens −21%) | M |

**What to do first.** Items 1, 4 and 6 are small, local and measurable without a sweep. Item 3 is the largest speed gain. Items 2 and 5 are the largest round savings and need the replay kit plus a live sweep. Judge those by per-call mechanism and replay, not by raw sweep ratios (see §6).

---

## 2. Local measurements (2026-10-12)

Graft HEAD (f18b7c3) was built to the scratchpad and run on a clone of this repository (344 files) and on golang.org/x/tools v0.51.0 (1,286 .go files). The machine has 8 cores.

| Operation | This repo | x/tools |
|-----------|-----------|---------|
| Cold `graft build`, gopls on PATH | 20.3 s | — |
| Cold `graft build`, no gopls | 7.2 s | 16.1 s |
| `graft build` again, nothing changed, gopls on PATH | **14.7 s** | — |
| MCP `find_code`, warm | 88–94 ms | 316–323 ms |
| MCP `find_all`, `read_symbol`, `trace_calls`, `file_api`, warm | 14–40 ms | 35–105 ms |
| First MCP call in a new server (load) | — | about 1.0 s |
| First query after editing one file (refresh) | **1.7–2.1 s** | **4.0–4.7 s** |
| CLI query, steady (process start included) | 0.11–0.40 s | — |
| `graft/.graph/wiring.json` | 8.2 MB | 20 MB |
| `graft/.cache` (extract cache, ask index) | 21 MB | 47 MB |

CPU profile of three one-file refreshes on this repository (`EnsureFreshGraph`, 1.67–1.76 s each):

| Phase | Share of the refresh |
|-------|----------------------|
| `WriteAskIndex` (re-tokenizes every ranked node: `askOrderedCounts` → `askTerms` → `askSplitWords`) | 40% |
| `BuildGraph` (cached extraction plus a full resolve) | 27% |
| JSON encode and decode (`wiring.json`, ask index, extract cache), overlapping the rows above | about 25% |

The refresh cost scales with repository size, not with the size of the edit. In an edit-then-query loop, every first query after an edit pays it.

What to do about it, in order:

1. **Make the ask index incremental.** Keep per-node term bags keyed by `BodyHash`, update document frequencies by delta, and re-tokenize only the nodes that changed. Expected effect: about −40% refresh time.
2. **Stop rewriting everything on each refresh.**
   - The MCP server is long-lived. It can apply the changed files to the in-memory graph and persist lazily.
   - Alternatively, use Cursor's shape: a commit-anchored base plus an overlay of dirty files.
   - A binary or sharded format would cut the JSON share.
3. **Reuse LSP edges by file content hash in `graft build` too** (recommendation 6).
4. **Do not build an n-gram index for `find_all` yet.** It answers in 35–105 ms here. Cursor built theirs because `rg` took more than 15 s in monorepos.

---

## 3. Already in Graft, so do not rebuild

- **Query-dependent ranking.** `askPageRankTS` is a personalized PageRank seeded from the lexical hits. That is the KGCompass and OrcaLoca recommendation ("β^distance from anchors" instead of global PageRank).
- **Path priors and saturation.** Tests are penalized (`testFactor`, `askTestContrastPattern`), and a file's hits decay by 0.9ⁿ, like semble's ×0.5 per extra chunk. Identifier splitting covers camelCase, snake_case and acronyms.
- **Session dedup.** Opt-in `seen` content refs (probe sessions, codedb `if_hash`, Ledger, AgentDiet). It stays **off by default**: codegraph turned theirs off because subagents share the MCP connection and compaction drops the source.
- **Skeletons** (`file_api`): Agentless's signature-only middle step.
- **Budgeting.** Budget fill, a 25K-character ceiling, named gaps and windowed oversized definitions.
- **Tool loading.** `anthropic/alwaysLoad` on 5 tools, with server instructions far under Claude Code's 2,048-character truncation.
- **Search nudge.** The PostToolUse nudge parses `grep`, `rg` and `git grep` inside Bash commands. This matters because native Claude Code builds route search through Bash.
- **Subagents.** A SubagentStart hook. It is the only channel into Explore, which skips CLAUDE.md.
- **Output format.** Compact line-oriented text, not JSON or TOON.

---

## 4. Measured as harmful or not worth it

| Idea | Why not |
|------|---------|
| TOON or other dense notations for tool results | Notation Matters (arXiv 2605.29676): up to −32% tokens but about −9 pp accuracy. Parse failures in multi-turn loops pushed one model 8–11% *above* the JSON token baseline |
| Perplexity-based token pruning (LLMLingua) on code | Breaks code, per LongCodeZip and SWE-Pruner |
| Exposing a query language (Cypher and the like) | CodexGraph: output length cannot be controlled (102k vs 76k tokens), and it collapses on weaker models (1.95%) |
| Paging search results | SWE-agent ACI: iterative or paged search scored 12.0%, below *no* search tool (15.7%), because agents page through every match |
| Hard-denying Read or Grep (PreToolUse deny or redirect, as in Serena and tokensave) | codegraph measured a Read deny: about 2× tool calls and 3× time, and the agent bypassed it with python. Our own check: PreToolUse `additionalContext` lands in the same place as PostToolUse's |
| A separate semantic-search tool | turbopuffer on Claude Code: the agent "did not reliably know when to call" it. If embeddings ever ship, fuse them into `find_code` |
| More steering text in instructions or tool descriptions | codegraph: no effect; a complete answer helped instead. The SWE-grep "issue calls in parallel" wording falls under this too. Prefer a schema change (recommendation 2) over wording |
| Model-tiered tool surfaces, `@N` path interning (jCodeMunch) | No evidence that the 7-tool, about 5.4k-character surface costs rounds. Hold until a Haiku sweep shows schema or output size matters |
| RL-trained retriever models (SWE-grep, WarpGrep, CodeScout, FuseSearch) | Need training infrastructure and hosted inference; Graft is local and deterministic. Borrow the *shape*: parallel calls, a turn cap, span output and a precision-first metric |
| Context editing or compaction tricks | Host-side (Claude Code `clear_tool_uses`, observation masking). Graft's part is to emit stable `path:Lx-Ly` and hash handles so a masked result is cheap to fetch again |

---

## 5. Sources

### 5.1 Products and host features

**Cognition SWE-grep / SWE-grep-mini (Windsurf Fast Context)**
- **Mechanism:**
  - RL-trained retrieval subagent.
  - At most 4 turns (3 to explore, 1 to answer), with up to 8 parallel grep, read and glob calls per turn.
  - Returns files and line ranges, rewarded by weighted F0.5.
- **Claims:**
  - Going from 4 to 8 parallel calls cut turns from 6 to 4.
  - Typical agentic search takes 10–20 serial turns.
  - Speed: more than 2,800 tok/s (mini) on Cerebras.
- **Source:** https://cognition.com/blog/swe-grep

**Morph WarpGrep**
- **Mechanism:**
  - An 8B RL model with grep, glob, list_dir and read.
  - v1 runs 8 calls × 4 turns; v2 runs up to 36 calls over 3 turns.
  - Rewarded by F0.5 on file and line spans.
- **Claims (vendor):**
  - F1 0.73 in 3.8 steps, vs Haiku's 0.72 in 12.4 steps.
  - SWE-bench with Opus 4.5: −39% input tokens, −26% turns.
- **Source:** https://www.morphllm.com/blog/fast-context-rl-retrieval.md

**Relace `relace-search`**
- **Mechanism:**
  - 4–12 parallel view and grep calls, ending in `report_back{path → [start, end]}`.
  - Can run as a "prefill" before the main agent, or as an on-demand tool.
- **Source:** https://docs.relace.ai/docs/fast-agentic-search/integration

**Cursor**
- **Semantic search.** Embeddings trained on agent traces.
  - Offline accuracy +12.5% (6.5–23.5% depending on the model).
  - Online code retention +0.3%, or +2.6% in repositories with more than 1,000 files.
  - Grep plus embeddings together worked best.
  - Source: https://cursor.com/blog/semsearch
- **Instant grep.** A sparse n-gram index anchored to a commit, with an overlay of edits.
  - Built because `rg` took more than 15 s in monorepos.
  - Source: https://cursor.com/blog/fast-regex-search
- **Indexing.** Merkle-tree sync and reuse of a teammate's index cut time to first query from a median of 7.87 s to 525 ms.
  - Source: https://cursor.com/blog/secure-codebase-indexing

**Sourcegraph Code Finder**
- A dedicated search-loop tool that returns a path, a range and a one-line note.
- Compared with it, an agent using local search took 2.19× the time.
- Their default MCP tool set was cut to 8.
- Source: https://sourcegraph.com/blog/code-finder-fast-code-search-for-agents

**Amp:** the finder subagent moved to Haiku 4.5, about 50% faster and about 3× cheaper. Source: https://ampcode.com/news/faster-search-agent

**Augment Context Engine MCP**
- A single `codebase-retrieval` tool.
- Claims 30–80% higher quality, with no turn or token numbers.
- Source: https://www.augmentcode.com/blog/context-engine-mcp-now-live

**JetBrains Context:** "up to" 68% fewer turns, 59% lower latency and 48% lower cost. These are best-case figures with no accuracy numbers. Source: https://blog.jetbrains.com/ai/2026/07/introducing-jetbrains-context-repository-intelligence-for-coding-agents/

**Greptile and Qodo**
- Greptile builds an AST graph with LLM docstrings per node, which are embedded.
- Qodo combines LSP, a knowledge graph and vectors.
- Neither publishes numbers.

**Claude Code**
- **Explore subagent.** Now runs on the main model and skips CLAUDE.md.
- **Search tools.** Native builds route search through Bash (`bfs`, `ugrep`).
- **LSP tool.** Definition, references, call hierarchy and diagnostics. It competes directly with `read_symbol` and `trace_calls`; Graft's edge is ranked search, inline code and budgets.
- **Tool search.** On by default; descriptions are truncated at 2,048 characters.
- **MCP output limits.** A warning at 10k tokens, a cap at 25k, and a spill to file over 50k characters.
- Sources:
  - https://code.claude.com/docs/en/tools-reference
  - https://code.claude.com/docs/en/mcp

**Anthropic tool use**
- **Tool search:** 77K → 8.7K tokens.
- **Programmatic tool calling:** −37% tokens.
- **Code execution with MCP:** 150k → 2k tokens. This is an illustrative example, not a benchmark.
- Sources:
  - https://www.anthropic.com/engineering/advanced-tool-use
  - https://www.anthropic.com/engineering/code-execution-with-mcp

**Cloudflare Code Mode:** the whole Cloudflare API behind `search()` and `execute()` in about 1k tokens. Source: https://blog.cloudflare.com/code-mode-mcp/

**Codex CLI**
- No index; it uses `rg`.
- Its prompt guide says to "maximize parallelism" and decide all needed files up front.
- Source: https://developers.openai.com/cookbook/examples/gpt-5/codex_prompting_guide

### 5.2 Open-source tools

Star counts are from 2026-10-12.

| Tool | Stack, stars | Technique worth noting | Verdict for Graft |
|------|--------------|------------------------|-------------------|
| [semble](https://github.com/MinishLab/semble) | Python, 6.2k | BM25 plus Model2Vec, fused with RRF; heuristic reranking (definition boost, identifier stems, path penalties, ×0.5 saturation); a public bench of 1,251 queries over 63 repos; p50 0.9 ms | Run Graft on its bench (adapters are 115–330 lines; a CBM adapter exists) |
| [Octocode](https://github.com/bgauryy/octocode) | TS + Rust, 950 | Every tool takes `queries[]` (1–5 per call); YAML output; `concise` returns paths only; about 2× fewer characters than `gh` | Recommendation 2 |
| [probe](https://github.com/probelabs/probe) | Rust, 725 | No index: rg, then tree-sitter whole AST blocks; boolean queries; session dedup keyed by MD5; merges blocks within 5 lines | Dedup already exists as opt-in `seen` |
| [codanna](https://github.com/bartolli/codanna) | Rust, 755 | `semantic_search_with_context` returns the symbol plus callers, callees and depth-2 impact in one reply | Recommendation 5 |
| [jCodeMunch](https://github.com/jgravelle/jcodemunch-mcp) | Python, 2.8k | Tool tiers by model (17 tools for Haiku, 79 for Sonnet); confidence from the gap between the top two scores; an absence "coverage contract"; a `redelivery_rate` metric; MUNCH CSV format | Recommendation 9; redelivery as a replay metric |
| [codedb](https://github.com/justrach/codedb) | Zig, 1.4k | "mini" profile of 5 one-shot tools; `read if_hash` answers "unchanged"; change detection by mtime then wyhash | Nothing new beyond `seen` |
| [fff](https://github.com/dmtrKovalenko/fff) | Rust, 11k | Frecency ranking, definitions first, git-status tags, fuzzy retry on zero hits | Recommendation 9; a boost for git-dirty files is worth an eval |
| [pgr](https://github.com/entireio/pgr) | Rust, 62 | Analysis of 202k tool calls from 766 sessions: 48.8% are search-related. Better search alone moved rounds into Read (8.55 → 9.98 reads, total calls flat) | Confirms bodies must come with the hits; trace-derived weak labels for the replay kit |
| [mgrep](https://github.com/mixedbread-ai/mgrep) | TS, 4.4k | Cloud embeddings; prints `path:start-end` only; a "MANDATORY: replaces Grep" skill | Nothing new |
| [trace-mcp](https://github.com/nikolai-vysotskyi/trace-mcp) | TS, 190 | Honest PR-review bench: −72.7% input tokens, but false positives 0.58 → 0.80 | A model for honest reporting |
| [tokensave](https://github.com/aovestdipaperino/tokensave) | Rust, 661 | PreToolUse blocks Explore and redirects symbol-shaped grep | See §4 (hard deny) |
| ck, ast-grep MCP, mcp-language-server, repomix, kit, Sourcebot, Potpie, code-graph-rag, ChunkHound | — | ast-grep: text output about −75% tokens vs JSON; miru: `truncated:true` plus `expand(file, line)`; headroom: code-search output compresses only 21% (vs 57% for logs) | Nothing new; Graft's output is already dense |

Recurring patterns across these tools:

- one-shot "task context" tools with graph neighbours attached;
- narrow tool surfaces;
- hooks steering agents away from Grep;
- not resending what was already shown;
- several queries per call;
- path priors against tests;
- a search subagent that returns spans.

### 5.3 Papers

**Graph-based localization**

- **LocAgent** (ACL'25, https://arxiv.org/abs/2503.09089). Closest design to Graft.
  - On SWE-bench Lite: file Acc@5 94.2 and function Acc@10 77.4, in 7 rounds vs 13 for OpenHands.
  - Ablation of function Acc@10 (full system 71.5):

    | Removed | Function Acc@10 |
    |---------|-----------------|
    | Entity search | 53.3 |
    | BM25 index | 60.2 |
    | Graph traversal | 66.1 |
    | Multi-hop (one hop only) | 66.8 |

  - **For Graft:** name and ID index quality matters about 3× more than traversal.
- **LARGER** (https://arxiv.org/abs/2605.16352)
  - **Mechanism:** grep hits mapped to graph anchors, plus up to 10 neighbours with confidence ≥ 0.5. Edge confidences: import 0.95, inheritance 0.9, tested_by 0.75, docs 0.6, fuzzy name 0.5. A deterministic "Callers/Callees [conf]" block is appended to each result.
  - **Numbers:** see recommendation 5.
  - **For Graft:** the most directly applicable paper.
- **SpIDER** (EMNLP'26, https://arxiv.org/abs/2512.16956): dense seeds expanded along graph edges under a budget. Call edges give at least +27% Recall@20, containment edges at least +13%.
- **RepoGraph** (ICLR'25, https://arxiv.org/abs/2410.14684): the k-hop ego-graph around keywords, +32.8% relative.
- **KGCompass** (https://arxiv.org/abs/2503.21710): S = 0.6^dist · (0.3·cosine + 0.7·Levenshtein). Graft's seeded PageRank already covers this.
- **OrcaLoca** (https://arxiv.org/abs/2502.00350): decay by graph distance.
- **CodexGraph** (https://arxiv.org/abs/2408.03910): a negative lesson, see §4.
- **Agentless** (https://arxiv.org/abs/2407.01489): validates skeletons.
- **Not applicable** (fine-tuning, or an LLM in the indexing loop):
  - CoSIL (https://arxiv.org/abs/2503.22424)
  - BugCerberus (https://arxiv.org/abs/2502.15292)
  - LingmaAgent (https://arxiv.org/abs/2406.01422)
  - CoRet (https://arxiv.org/abs/2505.24715)
  - CodeScout, FuseSearch, CodeGrep and RepoNavigator (RL-trained)

**Retrieval models**

- **SweRank** (ICLR'26, https://arxiv.org/abs/2505.07849). Function Acc@10 on SWE-bench Lite:

  | Model | Size | Acc@10 |
  |-------|------|--------|
  | BM25 | — | 36.9 |
  | Jina-code-v2 | 161M | 52.2 |
  | CodeRankEmbed | 137M | 58.8 |
  | SweRankEmbed-Small | 137M | 74.5 |
  | SweRankEmbed-Large | 7B | 82.1 |
  | Large plus the 32B reranker | — | 88.7 |

  Queries are full issue text. Graft's agent-written queries are short and full of identifiers, so expect a smaller gap.
- **Other models:**
  - jina-code-embeddings 0.5B and 1.5B: https://jina.ai/news/jina-code-embeddings-sota-code-retrieval-at-0-5b-and-1-5b
  - Qwen3-Embedding 0.6B, 4B and 8B: https://arxiv.org/abs/2506.05176
  - SWE-Tester (https://arxiv.org/abs/2601.13713): reranking BM25 top-30 with a 0.6B model is about as good as with 8B.
- **Do hybrids win?**
  - Plain BM25 plus dense fusion with RRF is mixed on code: MRR@5 .473 vs .508 for dense alone, with a cross-encoder best at .522 (https://arxiv.org/abs/2607.05443).
  - Graph expansion on top of seeds wins consistently.
  - Agent Retrieval Bench (https://arxiv.org/abs/2607.24882): no single retrieval family dominates, and a repo map gives the best yield at an 8K budget.

**Tokens and context**

- **SWE-agent ACI** (https://arxiv.org/abs/2405.15793):
  - A 100-line window beats a whole file (18.0% vs 12.7%).
  - Keeping only the last 5 observations beats the full history (18.0% vs 15.0%).
  - Paged search is worse than no search.
- **Ledger** (https://arxiv.org/abs/2608.00808): returning still-valid earlier results gave +8 pp resolve rate and −29% cost on GPT-5-mini.
- **AgentDiet** (FSE'26, https://arxiv.org/abs/2509.23586): trimming the trajectory cut input tokens by 40–60% with an unchanged resolve rate.
- **The Complexity Trap** (https://arxiv.org/abs/2508.21433): masking old observations halves cost and matches LLM summarization.
- **SWE-Pruner** (https://arxiv.org/abs/2601.16746): a goal-conditioned 0.6B skimmer cut tokens by 23–54%. The interface (a `focus` argument) is portable even though the model is not.
- **Context Rot** (Chroma, https://www.trychroma.com/research/context-rot) and **Lost in the Middle** (https://arxiv.org/abs/2307.03172): semantically close distractors hurt most. Near-miss excerpts are the worst filler, which argues for dropping weak hits rather than filling the budget with them.
- **Bai et al.** (https://arxiv.org/abs/2604.22750): input tokens dominate cost even with caching, and the same task varies up to 30× in cost.
- **MCP overhead** (https://arxiv.org/abs/2508.12566): MCP context inflates input 3.25–236.5×.

**Do structural tools beat grep?**

- **Code Isn't Memory** (https://arxiv.org/abs/2606.22417). The closest analogue to Graft: a tree-sitter, BM25, vector and call-graph index, used by Opus 4.7 on Go, Java and Python.

  | Metric | With index | Without index |
  |--------|------------|---------------|
  | Resolve rate | 50.4% | 41.9% (p = .003) |
  | Localization acc@5 | 84.5 | 44.3 |
  | Turns | 28.3 | 36.2 (−22%) |
  | $ per solved task | $2.30 | $2.84 |

  - Against a grep-based OpenCode agent, the resolve gain is not significant, but turns still drop from 36.0 to 28.3.
  - Gains are largest for Go and for changes touching 3+ files.
- **Repository Intelligence Graph** (https://arxiv.org/abs/2601.10112): build and test edges gave +12.2% accuracy and −53.9% completion time. Graft has no build or test edges.
- **CodeScout** (https://arxiv.org/abs/2603.17829): a bash-only agent beats LocAgent. Graft must beat strong grep, not a strawman.
- **"Does a Language Server Save Tokens?"** (https://arxiv.org/abs/2608.13568):
  - Results that give locations only *raise* tokens by 19%; adding inline text lowers them by 19%.
  - Given a free choice, agents picked LSP 0–6% of the time.
  - Grep caught references in comments that LSP missed.

---

## 6. Method and caveats

- **How the survey was done.** Three research passes on 2026-10-12: products, open-source tools and papers. Each read primary sources where it could, and shallow clones of semble, probe and Octocode were read without running them. Claims that a feature is already in Graft were checked against the code at f18b7c3 (`askPageRankTS`, `relevantHookRetrieval`/`askWeakMatch`, `resolveReadSelector`, the MCP tool schemas, the hook search nudge).
- **Vendor numbers are unverified.** That covers Cognition, Morph, Relace, Augment, JetBrains, Sourcegraph, Cursor, jCodeMunch and mgrep, and many exact values exist only in charts. The arXiv 2606 and 2608 papers were read through aggregator summaries, not the PDFs.
- **The local timings are single-machine wall clock** (8 cores), from a scratchpad build and clones. Refresh times are three runs on this repository and two on x/tools.
- **Before acting on recommendations 2, 5 or 8,** run them through the existing offline ranking eval and the replay kit (next-read coverage) before paying for a sweep. With n=6 a sweep cannot resolve per-call effects, and the first-query wording dominates raw ratios.
