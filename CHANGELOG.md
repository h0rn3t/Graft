# Changelog

## Unreleased

### Added

- **Subagents start with graft's note.** Claude Code runs no SessionStart hook for a subagent and gives an Explore subagent no CLAUDE.md, so a delegated search began wherever the subagent's own prompt pointed it, often at Grep. `graft init` now adds a `SubagentStart` hook that gives every subagent the note a session starts with: the line pointing at graft's MCP tools, or the CLI guide and repo map when the MCP server is not wired. On the eight benchmark questions about this repository, each delegated to an Explore subagent (Sonnet 5.5, 3 runs per question per arm), the subagent's first call was a graft tool in 24 of 24 runs instead of 15 of 24, and no run went without graft (7 of 24 had). Subagents used 21% fewer tokens and sessions 12% fewer; every answer was correct in both arms. Existing setups get the hook when upkeep rewrites the wiring after the upgrade.

## 0.5.1-beta.1 - 2026-10-10

### Changed

- **A symbol you can already name is `graft read`, not `grep`.** The instruction block and the session-start note told agents to `graft grep` a symbol they could already name. On Composer 2.5 that turned one lookup into several `graft grep` rounds, so the multi-file task used no fewer rounds than reading the files. `graft grep` and `graft_find_all` stay for every occurrence. A known symbol is `graft read`, several of them in one call with `--also`; a known file is `graft skeleton`, then read.
- **Cursor gets the search nudge.** After Grep or a shell search, Cursor's `postToolUse` hook returns `additional_context` with the same replacement Claude Code already shows, once per session. Cursor's Shell tool is recognized next to Bash.

## 0.5.0 - 2026-10-10

### Added

- **Go functions used as values are referenced.** A function or method passed
  or stored as a value — a call argument (`mux.HandleFunc(p, handle)`,
  `slices.ContainsFunc(xs, isSpace)`), a composite literal's field or element
  (`RunE: run`), the right side of `=`, `:=` or `var`, a returned value, a
  method value (`t.Cleanup(srv.Close)`) or an imported package's function
  (`kit.Helper`) — gets an inferred `references` edge from the code that names
  it. Handlers, cobra commands and callbacks had no callers, so `callers` and
  `blast` missed their users and `dead` listed them. A name resolves only to
  the one function of the caller's package, a method only through the type
  bound to its receiver, and a parameter or local of the same name hides the
  function. On this repository, 38 edges are added, each checked against its
  source line as a real use, and `dead --all` lists 78 functions instead of
  90, every one dropped a function passed as a value.
- **Go types are referenced by the code that uses them.** A parameter, result,
  field or `var` type, a composite literal (`&Store{}`), a conversion
  (`ID(n)`, `kit.Option(f)`), a type assertion or switch case, `new(T)` and a
  defined type's underlying type (`type IDs []ID`) each give an inferred
  `references` edge to the in-repo type, so `callers` and `blast` of a struct
  or interface list its users; before, only its methods and embeddings
  reached it. A type parameter of the same name hides the type, a method's
  receiver is no use of its own type, an embedded field stays `extends`, and
  predeclared and external types link nothing. On this repository, 2,641
  edges are added (12,638 instead of 9,950 in all); a sample of 30 checked
  against their source lines were all real uses.
- **Go method calls resolve in the package of the receiver's type.** A
  receiver typed `*kit.Client` binds `c.Do()` to `Client.Do` of the package
  the file imports as `kit`, never to a `Client` another package or the
  caller's own declares; a receiver of a standard-library or other external
  type (`*sql.DB`, `*testing.T`, `bytes.Buffer`) links nothing and counts as
  an external package call. A type assertion types its value, in
  `v.(*kit.Client).Do()` and `c, ok := v.(*kit.Client)`, and a field chain
  follows each field's declared type in its struct's package, `s.store.Get()`
  and promoted fields of embedded types included. `t, ok := t.(T)` keeps t's
  own type outside its block. On golang.org/x/tools, 445 ambiguous member
  calls drop to 0 and 1,958 calls of unknown receiver gain one. Outside
  testdata, 522 edges are added (14 sampled against their source lines were
  all real calls) and 423 removed. In 382 of the removed, the calling file
  names a same-named type of another package, which the old edge confused
  with the repository's: `t.Run` landed on a testdata `T.Run`, `buf.String`
  on an internal `Buffer.String`. Of 15 sampled from the other 41, 13 were
  such misses and 2 were right, found through `w := pw.NewEncoder()`: a
  `NewX` method of a variable no longer types its result. On this
  repository, 24 call edges are added and none removed. The extractor
  version (`go-v17`) changed: the first query after upgrading re-parses the
  repository once.

### Changed

- **`graft_find_all` shows the source around hits in up to six
  definitions.** Each definition first shows the lines near its hits, then
  the ones with the most hits, the most called first, grow to their whole
  source or the widest window that fits; `⋮ L60-L82` stands for skipped
  lines. Such an answer may use 10,000 bytes, not 8,000, unless hits were cut
  at the cap. Before, more than three definitions got no source at all, and a
  definition too long to fit kept only its hit line, so the agent's next round
  read it: 0.5 rounds per recorded Sonnet 5.5 session, 18.5% of its tokens,
  only re-read what the search had named. Replaying the 1,423 recorded
  `graft_find_code`/`graft_find_all` calls of Haiku, Sonnet and Opus sessions,
  the symbol the agent read next is now whole in the answer 125 times of 255
  for Sonnet (48 before), 65 of 451 for Haiku (42) and 30 of 47 for Opus (21),
  and the line range it read is shown 20 times of 106 (4); answers grow from
  3,187 to 4,656 characters on average for Sonnet. Ten definitions gained one
  more for Sonnet but doubled Haiku's answers that nothing read after. In a
  Sonnet 5.5 sweep, a question every session opened with the same
  `graft_find_all` took one round instead of two, 29,800 tokens instead of
  43,800, in 6 runs of 6; with Opus 5.5, 29,900 instead of 48,300. Where the
  agent needed nothing more, the larger answer costs about 1,500 tokens a
  session. Over eight questions, six runs each, sessions used 0.94 of the
  tokens with Sonnet 5.5, 0.90 with Opus 5.5 and 0.98 with Haiku 5.5, whose
  sessions seldom reached a changed answer.
- **Ranked search keeps tests behind when a query sets them apart.**
  `graft_find_code` and `graft ask` stop placing tests and copies after
  production code when a query names them ("test", "testdata", "vendor"),
  unless it also contrasts them with production code: "prefer the production
  definition over testdata copies", "non-test", "skip", "exclude". Such a
  query asks for the code that tells them apart, and ranked the test about it
  first. On 88 recorded queries, the expected symbol ranks first in 38
  instead of 25 (in 12 of 15 queries about `graft read` choosing a
  definition, instead of 0), and recall rises from 0.411 to 0.456; two other
  frozen graphs give 36 for 23 and 33 for 22, with recall 0.397 to 0.442 and
  0.546 unchanged. Of 339 recorded `graft_find_code` queries, the 47 with
  such contrast change and the rest answer byte for byte as before. Opus 5.5
  sessions opening with such a query answered after it, 26,100 tokens and
  one call, where they took 56,000 tokens and three calls.
- **Ranked search ignores type uses.** `graft_find_code` and `graft ask`
  spread relevance along calls and references, and a type most functions
  take would tie them all together: with type uses in that walk, recall on
  88 recorded queries fell from 0.549 to 0.535. They are left out of it, and
  the 88 queries rank exactly as before. `graft map` hubs and the coupling
  count in `graft_find_all` do count them, so central types such as a
  graph's node and edge structs now appear among the hubs.
- **`graft_trace_calls` stays within the answer budget.** A type used across
  the code has hundreds of users, past the size Claude Code shows inline. The
  answer lists production definitions and edges first, tests and copies
  after, up to the 8,000 bytes `graft_find_all` also keeps to, then counts the
  rest per file: `and 174 more: internal/graph/extract.go (5), …`.
  `graft callers` on the command line still lists every edge.

## 0.5.0-rc.8 - 2026-10-10

### Changed

- **`graft_find_all` completes the shortest definitions first.** When the
  hits fall in up to three definitions that do not all fit the 8,000-byte
  answer, the shorter ones come whole and a long one keeps its hit lines.
  Before, a long definition ranked first could take the budget, and the agent
  read the shorter ones it crowded out in another round. Replaying 339
  recorded calls, answers are 10% shorter for Claude Haiku 5.5's searches,
  21% for Sonnet 5.5's and 29% for Opus 5.5's. The definitions agents read
  next are as often already complete as before.
- **`graft_find_all` names the symbols holding the hits it leaves out.** The
  closing note gives a file's symbols when its remaining hits lie in one or
  two of them, as in `cmd/graft/hook_metrics_test.go (12 in
  TestHookPriceTable)`, so an agent can read that definition directly or
  answer from its name. A file spread over more symbols is only counted.

### Fixed

- **Tests and copies left out of a `graft_find_all` answer no longer count
  as truncated.** The answer said "(truncated: N more hits beyond the cap —
  narrow with --in or refine the pattern)" for hits in tests, testdata and
  generated code that it lists only in the closing note, though nothing had
  reached the cap. In recorded sessions, agents followed it with another
  search narrowed to the test file.

## 0.5.0-rc.7 - 2026-10-10

### Added

- **Answers open with the call flow among their hits.** When three or more
  nodes of one call chain link the functions a `graft_find_code` answer (or
  `graft ask --source`) ranks, or a `graft_read_symbol` batch reads, the
  answer starts with one line such as `call flow: Start (flow/flow.go:L4) →
  prepare (flow/flow.go:L10) → [normalize (flow/flow.go:L22)] → finish
  (flow/flow.go:L25)`. A step may pass through one function outside the set,
  shown in brackets; a `(dispatch)` step goes from an interface method to its
  implementation, and inferred steps say so. The functions in the chain get
  their complete source first. Agents used to walk such chains one
  `graft_trace_calls` call per round.
- **`graft_find_all` shows the code around a narrow search.** When the hits
  fall in at most three definitions, each comes whole with its matching lines
  marked `▸`; when every hit lies in one file of at most 220 lines, that file
  comes once, whole, after one line per enclosing symbol. Answers stay within
  the 8,000-byte cap, and a wider search keeps its hit lines only.

### Changed

- **Ranked answers spend their budget on complete source.** `graft_find_code`
  and `graft ask --source` showed an 8-line excerpt of each hit and stopped
  near 40% of the budget; in recorded sessions, 47 of 57 reads that followed a
  `graft_find_code` call asked for a definition the answer had just shown in
  part. Now, best hit first, an excerpt becomes its complete definition while
  the answer fits — below the top hit and off the call flow, only a
  definition within a fifth of the budget, which covers nine in ten of the
  definitions agents went on to read; a file of at most 220 lines whose hits
  span at least 60% of it is shown once, whole; and an unnamed implementation
  of an interface with three or more implementations, off the call flow, shows
  only its signature. A query that names the top hit no longer decides
  whether it comes whole. Edit intent and `full: true` are unchanged.
- **Measured.** Replaying 247 recorded calls, the definitions agents read next
  were already complete in 46% of `graft_find_code` answers (was 12%) and 20%
  of `graft_find_all` answers (was 0%), for answers about 1.6× and 1.9× as
  long. On Claude Haiku 5.5 (8 tasks, 48 sessions per build, all answers
  correct), sessions took 17% fewer rounds and 17% fewer tokens (95% CI
  9–24%) and cost 11% less (95% CI 1–21%), with Read calls down from 0.25 to
  0.15 per session. On Claude Opus 5.5 at low effort, sessions took 12% fewer
  rounds (95% CI 9–15%) and 6.5% fewer tokens (95% CI 2–10%); cost fell 6%,
  within noise. On Claude Sonnet 5.5 at low effort, which answers in two to
  four rounds, the result was neutral: 9% fewer rounds, tokens and cost within
  3% of before.

## 0.5.0-rc.6 - 2026-10-09

### Added

- **Generated code is recognized by its header.** A file whose first lines say
  a tool wrote it — Go's `// Code generated … DO NOT EDIT.`, the `@generated`
  tag, protoc's `Generated by … DO NOT EDIT!` — has its nodes marked
  `generated` in the graph. Ranked search and `graft_find_all` place it after
  production code like other copies, and `dead`, `complexity`, `hotspots`,
  `cycles` and `routes` leave it out. Path rules (`.pb.go`, `zz_generated`,
  `generated/`) still apply; the header catches sqlc, mockgen, stringer and
  ent output they miss.
- **A misspelled symbol gets the closest names.** `graft read`,
  `graft callers`, `graft path` and their MCP tools add `(Did you mean …?)`
  when a name the graph lacks is one or two edits from names it has, such as
  `Alpha` for `alpha`.

### Changed

- **Expected misses over MCP are answers, not errors.** A missing graph, a
  name that resolves to no single definition, a definition over budget, an
  unknown symbol in `graft_trace_calls` and an unknown file in `graft_file_api`
  return their guidance with `isError: false`: after one or two error results
  an agent stops calling the server for the rest of its session. Malformed
  requests, an unreadable graph and other faults stay errors. The CLI's exit
  codes are unchanged.
- **The prompt hook skips messages Claude Code writes itself.** A background
  task's `<task-notification>` and a subagent's `<agent-message>` hand-back no
  longer trigger a retrieval; such reports often mention tests, which pulled
  test copies into the injected context. Hook output is also capped below the
  10,000 characters Claude Code shows inline.

### Fixed

- **Go imports bind the name goimports assumes.** An unaliased import of
  `gopkg.in/yaml.v3` binds `yaml`, `k8s.io/klog/v2` binds `klog` and
  `github.com/mattn/go-sqlite3` binds `sqlite3`, where graft used the last
  path element (`yaml.v3`, `v2`, `go-sqlite3`). Calls into an in-repo `/vN`
  module now resolve, and calls into external packages count as external
  instead of as receivers of unknown type. The extractor version (`go-v15`)
  changed: the first query after upgrading re-parses the repository once.
- **Language-server edges survive an upgrade.** The first query after a new
  extractor version re-parses every file, and that refresh dropped all
  `lsp_resolved` call edges until the next explicit `graft build`; on this
  repository, 168 of them. A refresh now keeps an edge whenever its calling
  file's content is unchanged, whether or not the previous fingerprint is
  readable.
- **Go `implements` follows Go's method-set rules more closely.** A method must
  match the interface method's result count as well as its parameters, and a
  method two embedded types promote at the same depth belongs to neither, so
  such a type no longer implements interfaces through it.

## 0.5.0-rc.5 - 2026-10-09

### Changed

- **A Claude Code session with graft's MCP server starts with one line.**
  When the repo's `.mcp.json`, or `~/.claude.json` at user or project scope,
  registers graft's MCP server, the session-start hook sends a short note on
  the MCP tools instead of the CLI guide and the repo map; the server's own
  instructions already carry the tool guide. Other hosts, and Claude Code
  without the server, keep the full orientation.
- **Shorter MCP instructions and tool descriptions.** With the session note,
  every model call carries about 3,800 fewer characters (≈950 tokens), keeping
  the hints that change how a model works: batch reads with `also:`, no search
  for a known symbol, one tool per need.
- **`graft_find_all` counts tests and copies instead of listing them** while
  production code matches: hits in tests, testdata, fixtures, generated and
  vendored code go into the closing note with a count per file, production
  files first, unless the pattern itself asks for tests or copies. In this
  repository, whose testdata mirrors its own source, they were 38% of
  `graft_find_all` output.
- **Identifiers split at acronyms.** Ranked search reads `inputUSDPerMtok` as
  input, USD, per, Mtok and `HTTPServer` as HTTP, server, while `IDs` and
  `URLs` stay whole, so a question about the statusline's dollar amount finds
  `inputUSDPerMtok`. The ask index format and the extractor version
  (`go-v14`) changed: the first query after upgrading re-parses the
  repository once.
- Graft against the same eight questions without it: on Claude Haiku 5.5 at
  medium effort (48 runs against 144), 37% fewer tokens and 50% lower cost
  (0.5.0-rc.4: 37% and 48%); on Claude Sonnet 5.5 at low effort (24 runs
  each), 9% fewer tokens and 15% lower cost (0.5.0-rc.4: 4% and 20%); on
  Claude Opus 5.5 at low effort (24 runs each), 27% fewer tokens and 32% lower
  cost (0.5.0-rc.4: 18% and 33%). Every answer named every required symbol.
  Questions that one grep for a rare word answers still read more tokens with
  graft on the larger models: two on Opus, one on Sonnet.

## 0.5.0-rc.4 - 2026-10-09

### Added

- **A type hit lists its methods.** A struct, class or interface in
  `graft_find_code` or `graft ask --source` text gets a `methods:` line with up
  to ten names and their spans, including methods in other files of its
  package, so the next `graft_read_symbol` names them instead of guessing or
  calling `graft_file_api` first.

### Changed

- **A file that answers the question fills several places.** After the top
  hit, ranked search takes hits by score, each discounted by 10% for every hit
  its file already placed, instead of one hit per file in turn. Asked about Go
  route groups, it now returns `goGroupCall`, `goPrefix` and `joinRoute` from
  `routes.go`, where it returned one `routes.go` hit beside seven weak matches
  from other files. A file's own node, which carries no code, waits behind
  that file's symbols, in workspaces too.
- **Common words in a name no longer lift it.** The name-coverage tiers from
  0.2.0 counted every query word a name matched, so `goTypeName` led a
  question that said "Go" and "name". They are gone. On 88 queries Claude
  Haiku 5.5 sent while benchmarking, the expected symbols among the hits rose
  from 24% to 50%; asked by its doc sentence without its name, a documented
  function is among the top eight hits 96% of the time, up from 38%.
- On Claude Haiku 5.5 at medium effort, the same eight questions, 72 runs with
  graft against 144 without: graft now uses 37% fewer tokens and costs 48%
  less than the runs without it (0.5.0-rc.3 against the same runs: 18% and
  31%), in 4.6 model rounds against 7.4. Every question now reads fewer tokens
  than without graft, and all 72 answers named every required symbol.

## 0.5.0-rc.3 - 2026-10-09

### Added

- **`graft_read_symbol` reads several symbols in one call.** `also: [...]` takes
  up to seven more selectors beside `symbol`, sharing its budget; a span inside
  another requested definition comes once, as with CLI `--also`. Reading one
  symbol per call cost a model round each.

### Changed

- **`graft_find_all` answers are capped near 2000 tokens** instead of about
  10,000. Past the cap the answer names the files holding the remaining hits,
  most first, so the next call narrows with `in:` rather than carrying a broad
  pattern's tail through every later round.
- **An MCP `in` that covers no indexed file widens instead of failing.**
  `graft_find_code` and `graft_find_all` given such a prefix, often `graft/`
  itself, search every indexed file and say so, instead of returning an error
  that costs a round. Workspaces and the CLI keep the error.
- On Claude Haiku 5.5 at medium effort, eight questions about this repository,
  48 runs with graft against 120 without: graft now uses 19% fewer tokens and
  costs 31% less than the runs without it (0.5.0-rc.2: 8% and 12%), in 26%
  fewer model rounds; 47 of the 48 answers named every required symbol. Two
  multi-file questions still read more tokens than without graft, at a lower
  cost.

## 0.5.0-rc.2 - 2026-10-09

### Added

- **`graft routes` knows Fiber.** `All`, `Connect`, `Trace` and the
  method-first `Add(fiber.MethodPut, "/users", h)` register routes, as do
  gin's `Handle("GET", …)`, echo's `Add` and chi's `Method`, with the method
  read from a string or an `http.Method*`-style constant.
- **Go route groups prefix their routes.** `api := app.Group("/api")`, nested
  groups, `app.Group("/admin").Post(…)` and chi's
  `r.Route("/articles", func(r chi.Router) { … })` put their prefix on the
  routes registered through them, within the function that declares the group.

## 0.5.0-rc.1 - 2026-10-09

### Added

- **Code-health and architecture commands.** `graft path <from> <to>` prints
  the shortest chain of calls, references and imports between two symbols, a
  call into an interface method continuing to its implementations. `graft dead`
  lists functions and methods nothing calls, as high (unexported, name used
  nowhere else), medium (exported) or, with `--all`, low confidence with a
  reason; entry points and interface implementations are never listed.
  `graft complexity` ranks functions by cyclomatic complexity, and
  `--threshold N` exits 1 when one exceeds N, for CI. `graft hotspots` ranks
  files by recent git commits × summed complexity. `graft cycles` finds
  dependency cycles between directories or, with `--level file`, files.
  `graft routes` lists HTTP routes and their handlers for Go net/http, gin,
  echo and chi, Flask, FastAPI and Django, Express and NestJS, and Spring.
  Every one refreshes the graph first and takes `--in` and `--json`.
- **`graft_trace_calls` takes `to`.** With a second symbol the MCP tool answers
  with the shortest call chain instead of edges, the same answer as `graft path`.
- **The graph records complexity.** Every function and method with a body
  carries its cyclomatic complexity, in all eight languages.
- **Go interfaces have methods and implementations.** An interface's methods
  are graph nodes, embedded structs and interfaces are `extends` edges, and a
  type `implements` each in-repo interface whose method set it has — names and
  arity compared, so those edges are `inferred`. Interfaces that embed a type
  outside the graph are skipped rather than guessed.
- **The build counts the calls it could not bind.** `meta.unresolvedCalls` in
  `wiring.json` holds counts by reason — receiver type unknown, member or name
  ambiguous or not in the graph, external package — and `graph-quality`
  reports them, so resolver gaps can be measured.
- **`graft callers --json` hits carry their edge `confidence`.**
- **The reviewed oracle assesses `implements`.** The Go fixture gains a
  `go-implements` partition, and `Saver.Save` joins its `go-contains` facts.
- **Reviewed tough-case fixtures for Python, TypeScript and Java.** Each pins
  its current score: the known gaps — `super` and module-alias calls, a
  factory's return type, `new` in TypeScript, class-qualified Java calls,
  method-level `implements` outside Go — are listed in the manifests, so the
  resolver fix that closes one shows up as a test change.

### Changed

- **Go calls resolve across packages without a language server.** `pkg.F()`
  resolves through the file's imports, and typed parameters, `var x pkg.T` and
  `x := pkg.NewT()` give `x.M()` its receiver. On graft's own source the static
  pass binds 4,453 calls instead of 3,048. With gopls installed, `graft build`
  ends with about the same calls as before — gopls had been finding these, and
  now resolves 168 instead of 1,622 — but the edges no longer depend on a
  language server, and they survive the refresh before each query, which keeps
  no language-server edge for a file that changed.
- **An unqualified Go name resolves only within its own package.** A bare call
  no longer binds to a same-named function of another package, and a copy of
  the code elsewhere in the tree (testdata) no longer makes a package's own
  calls ambiguous.
- The extractor version is `go-v13`; the first build after upgrading re-parses
  every file once.

### Fixed

- **`Direct -> A.Save` resolves.** The call through a typed parameter that the
  oracle fixture recorded as a known false negative is now found, and
  `graph-quality --strict` passes on the fixture.

## 0.4.0-rc.5 - 2026-10-09

### Fixed

- **The statusline counts what every graft tool saved.** `graft_find_all`,
  `graft_trace_calls`, `graft_repo_map`, `graft_file_api` and
  `graft_read_symbol`, plus CLI `skeleton` and `read`, now record their savings
  like `ask`, `grep`, `callers` and `map` already did, so a session that leans
  on them no longer shows no `tok saved` at all.
- **Savings land in the session that made the call, as soon as it returns.**
  The Claude Code `tool-savings` hook now also runs after graft MCP tools
  (matcher `Grep|Bash|mcp__graft__.*`) and credits the shared pending ledger
  right away instead of at the end of the turn. The running total updates
  mid-turn, and parallel sessions in one repository no longer pick up each
  other's savings. Existing wiring is rewritten by upkeep on upgrade.

## 0.4.0-rc.4 - 2026-10-03

### Changed

- **Oversized `graft read` failures are self-recoverable.** The error now
  names the exact budget to retry with (`retry once with --budget N or
  higher`), and the skill card, agent instructions and MCP tool descriptions
  document the one-retry rule with fallback to reading the `file:line` range
  directly, so agents recover from `needs N estimated tokens` instead of
  stalling.

## 0.4.0-rc.3 - 2026-09-29

### Fixed

- **`graft uninstall` and re-init remove Cursor's hooks.** Retraction covers
  `.cursor/hooks.json` and the legacy `.cursor/hooks/graft-hooks.cjs` shim, so
  `graft uninstall` and `graft init --agents <subset>` no longer leave live
  graft hook entries in a committed config. Graft's entries go, foreign entries
  and a user-chosen schema version stay, and the file is deleted once nothing
  is left.
- **`graft init --dry-run` lists what a run with the same flags writes.** The
  plan honors `--no-mcp`, `--no-hooks` and `--no-global` instead of listing
  every file regardless; Claude Code keeps its own MCP server and hooks, as in
  a real run.
- **`--agents` accepts comma-separated ids.** `--agents cursor,gemini` reads
  like `--agents cursor gemini`, and an unknown id is still rejected before any
  write.

## 0.4.0-rc.2 - 2026-09-29

### Changed

- **Cursor wiring ships with the repository.** `.cursor/rules/graft.mdc`,
  `.cursor/mcp.json` and `.cursor/hooks.json` are committed instead of
  gitignored, so opening the repo in Cursor picks up the Graft instructions, MCP
  server and hooks from a fresh clone without running `graft init` first. `graft
  init` keeps the three files in sync idempotently and preserves hooks or MCP
  servers it does not own.

## 0.4.0-rc.1 - 2026-09-28

### Changed

- **`graft build` shows a progress bar on terminals.** When stderr is a terminal, the parsing line repaints in place with a bar and percent (`parsing 3/9: [██████░░░░░░░░░░░░░░]  33% src/store.ts`). Piped or redirected output keeps the plain `parsing i/n: file` line, so logs and scripts are unchanged.

## 0.4.0-beta.1 - 2026-09-27

### Added

- **Reviewed graph-quality oracle.** The standalone `graph-quality <fixture-root> --oracle <manifest.json>` command verifies pinned source hashes, builds an isolated structural graph, and reports scoped TP, FP, FN, precision, recall, and exact mismatched facts. `--strict` fails on mismatches; ordinary graph-quality output is unchanged.
- **Reproducible quality baseline.** A reviewed Go fixture tests same-name methods, recursion, interface and callback boundaries, and cold versus incremental builds. The baseline records local build/query latency, memory, and response bytes on pinned inputs without claiming repository-wide semantic accuracy.

### Known limitation

- The structural extractor still misses the statically named `Direct -> A.Save` call in the fixture. The oracle reports it as one FN; the existing `resolvedPct` metric is target resolution, not semantic precision.

## 0.3.0-rc.2 - 2026-09-27

### Changed

- **Hooks and the statusline no longer need Node.js.** `graft init` writes Claude Code, Codex and Cursor hook entries that run the binary directly as `graft _hook <sub>` and the Claude statusline as `graft _statusline`. Repo-level entries (`.claude/settings.json`, `.cursor/hooks.json`) call `graft` by name, and `init` warns when it is not on `PATH`. Machine-level entries (`~/.claude/settings.json`, `~/.codex/hooks.json`) name the absolute path of the binary that wrote them. `init`, `uninstall` and upkeep delete the `graft-hooks.cjs` and `graft-statusline.cjs` shims earlier releases wrote and leave a file with other content alone. Upkeep migrates existing wiring on the next session.
- **The user-level Claude Code hook still yields to the project's.** The user-level entry passes `--user`, so it stays silent when the project registers graft for the same event. `GRAFT_HOOK_SHIM` is still honored for shims that have not been migrated yet.

## 0.3.0-rc.1 - 2026-09-26

### Changed

- **`graft build` uses an installed language server by default.** When `gopls`, `rust-analyzer`, `clangd`, `pyright-langserver` or `typescript-language-server` is on `PATH` for a language in the graph, the build adds compiler-resolved `lsp_resolved` call edges without `--lsp`. With no matching server the build is unchanged and prints no LSP progress line. `--no-lsp` or `GRAFT_NO_LSP=1` skips enrichment; `--lsp` is still accepted. Hook-driven background syncs and pre-query refreshes never start a language server.

## 0.3.0-beta.4 - 2026-09-25

### Fixed

- **`go install` works without the public Go proxy.** Upstream deleted the `v0.25.0` tag of `github.com/tree-sitter/go-tree-sitter`, so resolving 0.3.0-beta.3 with `GOPROXY=direct` or a VCS-backed corporate proxy failed with `unknown revision v0.25.0`. graft now pins the tagged `go-tree-sitter v0.24.0` and the grammars that match its ABI 14 runtime: `tree-sitter-c v0.23.6`, `tree-sitter-go v0.23.4` and `tree-sitter-rust v0.23.3`. The extractor ID changed, so existing graphs rebuild once on the next query.

## 0.3.0-beta.3 - 2026-09-25

### Added

- **Exact symbol reads.** `graft read <symbol>` and the MCP tool `graft_read_symbol` return a known symbol's complete source, current span and source hash without a preceding search. Select by name, node ID or `path::name`. A read also returns the symbol's direct callees in the same directory: whole when they fit the budget (default 2000 estimated tokens), otherwise as pointers. On the CLI, repeated `--also` reads up to eight symbols under one shared budget. Oversized or stale definitions fail instead of returning partial source.
- **Forgiving selectors.** A name shared by one production definition and only testdata, fixture, generated or test copies reads the production one. A `path::name` whose file lacks the name falls back to the name. A `note` says which applied; other ambiguous names still return candidate IDs.

### Changed

- **A ranked query that names its top hit returns that definition whole.** `ask --source` and `graft_find_code` inline the complete top definition instead of an excerpt when a query word equals its name and it fits half of the budget. Descriptive queries keep excerpts.
- **Local MCP tools share a cached graph snapshot.** File API, call tracing, grep, map and exact reads reuse one decoded graph, and the ask index loads only for ranked queries. Source freshness is still checked before every query.
- **Claude Code hooks run once.** When `graft init --global` wired both the project and the user settings, every hook ran twice. The shim now passes its path in `GRAFT_HOOK_SHIM`, and the user-level hook yields to the project's registration for the same event. Upkeep rewrites existing shims on the next session.
- **Hooks are quieter.** The prompt hook no longer injects the "no strong match" notice, and the raw-search nudge is shown once per session instead of three times.
- On Claude Opus 5.5 at low effort, over four side-by-side runs per task against 0.3.0-beta.2's retrieval, a named-function question took 44% fewer tokens and one model request fewer. A multi-file question was 13% cheaper, within run-to-run variance.

## 0.3.0-beta.2 - 2026-09-25

### Fixed

- **The Claude Code statusline shows session savings again.** Since retrieval output dropped its `[graft] tokens saved` footer, the hook had nothing to add up and the `~N tok saved · ~$X` segment never appeared. `ask --source`, `grep`, `callers` and `map` (CLI and MCP) now record what they saved in `graft/.cache/savings-pending`, and the Stop hook credits it to the session. Agent-visible output is unchanged. Cursor sessions still report no savings.

## 0.3.0-beta.1 - 2026-09-25

### Changed

- **Symbols carry their doc comments.** A build attaches the comment block directly above each definition to that symbol's `summary`, in every supported language. Tool directives (`//go:`, `//nolint`, `# noqa`, `eslint-` and similar), license headers and C/C++ preprocessor lines are left out. The graph still has no LLM-generated summaries.
- **Ranked retrieval matches what the code is documented to do.** `ask`, `graft_find_code` and prompt-hook retrieval find a symbol through words that appear only in its doc comment. Name and path matches still rank at least as high. On 30 plain-English queries over this repository, mean reciprocal rank rose from 0.24 to 0.32 and top-5 hits from 11 to 15. Name-based queries did not change.
- **Documented hits show their first doc line.** CLI `ask` and `graft_find_code` print it after the signature and before the excerpt, and it is the first thing dropped when an answer exceeds its budget. Ask JSON adds an optional `doc` field; `snippet` stays the signature. `skeleton` and `graft_file_api` append ` — <doc line>`, and graft cards list the doc line instead of the signature. Prompt-hook pointers and caller/callee answers are unchanged.
- The extractor ID changed, so existing graphs rebuild once on the next query.

## 0.2.1 - 2026-09-24

### Removed

- **The npm distribution.** `@nanonets/graft`, its Node launcher, and its build and postinstall scripts are gone. Install with `go install github.com/h0rn3t/Graft/cmd/graft@latest`.

### Changed

- **The version is stamped into the binary.** Release builds set it with `-ldflags "-X main.version=<v>"`. Otherwise `graft --version` reports the module version that `go install ...@vX.Y.Z` records, instead of reading `package.json`.
- **Hook and statusline shims run the graft binary directly.** They call the executable that wrote them, or `graft` on PATH, instead of searching npm package roots. Hooks now work for `go install` users. The Claude allowlist no longer adds `Bash(npx graft:*)`.
- **Grammar updates.** tree-sitter-c is now 0.24.2 and tree-sitter-go 0.25.0. The extractor ID changed, so existing graphs rebuild once on the next query.
- The README is now in Ukrainian. The English version moved to `README.en.md`, and each file links to the other. The logo no longer shows a version number.

## 0.2.0 - 2026-09-24

### Changed

- **Ranking tolerates regular English inflections.** Query words and indexed terms fold plural `-s`/`-es`/`-ies`, `-ing` and `-ed`, so `timeouts` finds `hookTimeout`. An exact match still ranks at least as high as a folded one. `grep` and `graft_find_all` keep matching literally.
- **Symbols whose names match more query terms are listed first** among the selected hits, ahead of graph centrality and file order. Tests, fixtures and copied source are not lifted unless the query asks for them. The ask index format changed, so existing graphs rebuild once on the next query.
- **Retrieval text is shorter.** `ask`, `graft_find_code`, `skeleton` and `graft_file_api` no longer repeat the signature above an excerpt that starts with it, echo the query, or tag hits with their kind. A truncated excerpt ends with `… +N lines (--full)`. Excerpt windows skip blank and bracket-only lines, bare file hits take one line, and skeleton entries read `<span> <signature>`. JSON output is unchanged.
- **Weak answers name a concrete next step.** When no hit's name covers the query's distinctive terms, the answer ends with one notice naming the tool and a term to try: MCP tool names over MCP, CLI commands on the CLI.
- **Exhaustive search lists production code first.** `grep` and `graft_find_all` put production groups before testdata, fixture, generated and vendored groups. Every hit and the totals are unchanged.
- **Hooks repeat less and cost less.** The post-edit blast radius is shown once per file and agent context until its dependents change. The prompt hook inlines code only for a strong top hit and gives the rest as one-line pointers. For Claude Code, the per-call PostToolUse hook now runs only for `Grep|Bash`. Graft and source-read counts come from the transcript at `Stop` and the new `SubagentStop` hook. Upkeep rewrites existing installations and keeps unrelated user hooks.
- `graft init` shows the graph's node and edge counts under the `graft` lettering, beside the gopher's last line.
- The generated agent instructions and the graft skill are shorter, and they say when to use each tool.

## 0.1.3 - 2026-09-24

### Changed

- Ranked retrieval prefers production code over fixture, generated, and vendored copies, with explicit category and path queries retaining access.
- `ask --budget` and MCP `budget` bound the complete response in estimated tokens (default 2000); compact excerpts select up to eight source lines. `--intent edit` adds bounded direct callers, dependencies, and related tests.
- Retrieval no longer repeats savings banners. JSON source-size metadata and explicit session statistics remain available.
- Hooks track source revisions per agent and reset on session-start/compaction. MCP callers can opt into reusable content references through `seen`.
- The MCP server reuses decoded graph/index snapshots while retaining freshness checks and invalidation on file changes.
- **Claude Code sees graft's search tools from the first turn.** `graft_find_code`, `graft_find_all`, `graft_trace_calls` and `graft_file_api` carry `_meta["anthropic/alwaysLoad"]`, so they are not deferred behind a tool search, a step that led the model to use the grep it already had. `graft_repo_map` and `graft_check_freshness` stay deferred.
- **A raw code search gets a pointer to its graft equivalent.** When a Claude Code session runs a recursive `grep`, `git grep`, `rg`, `ag` or `ack`, or the Grep tool, over code in an indexed repo, the PostToolUse hook adds the matching call, for example `graft_find_all {"pattern":"Foo|Bar","in":"internal/graph"}`. The pattern is translated from basic regex syntax where needed. This happens at most three times per session. Searches that filter piped output, read one file, reach outside the repo, or search `graft/` get no note. The hook is already installed, so existing setups pick this up without running `graft init` again.
- The MCP server instructions and the `graft_find_code` and `graft_find_all` descriptions now say plainly that the tools replace grep, rg, find and file reads for this repo's code.

## 0.1.1 - 2026-09-24

### Fixed

- **One malformed SQL statement no longer unindexes its whole file.** The statement is dropped on its own, the file's other statements are still indexed, and the build reports `N of M SQL statements not indexed (...)`. A semicolon inside unclosed parentheses now ends the broken statement instead of swallowing the ones after it.
- **SQL written for a preprocessor indexes as the SQL it expands to.** Build-time placeholders such as `@extschema@.fn` read as a schema name. psql meta-commands (`\set`, `\if`, `\copy`, `\qecho`) are skipped to the end of their line. psql variables (`:name`, `:'name'`, `:"name"`) and Jinja or Django expressions (`{{ ... }}`) stand for a dynamic name: they define nothing and raise no warning. Only the first branch of a template `{% if %}` chain decides parentheses and statement bounds, and every branch still contributes references.
- **`graft_find_all` stays within the host's tool-result limit.** Each answer caps its hit groups at 40,000 bytes, keeps the top-ranked hits, and counts the rest as truncated. Before this, a workspace search merged up to 300 hits from every child repository with no overall cap.
- **`in` narrows workspace searches.** `graft_find_all` and `graft grep --in` at a workspace root read the first path segment as the child repository and the rest as a path inside it, as `graft_find_code` does. Before this, `in` was silently ignored.

## 0.1.0 - 2026-09-24

### Breaking

- **Go is the only implementation.** The CLI, MCP server, graph extraction, host hooks, statusline, and upkeep now run entirely through the native Go binary. The TypeScript backend, JavaScript library exports, GitHub App, browser viewer, and TypeScript launcher fallback were removed.
- **Deep meaning and visualization are gone.** `graft build --deep`, `-j/--concurrency`, `--allow-partial`, `graft viz`, and `graft blast --export-viz`/`--title` are rejected as unknown input. Structural `graft build` and `graft blast` remain.
- **Language support is narrower.** Kotlin, Swift, PHP, R, Ruby, and the former breadth-tier languages were removed. The native extractor supports Go, Python, TypeScript, JavaScript, Java, Rust, PostgreSQL, C, and C++.

### Removed

Graft is now fully local: every external service integration is gone, and the removed commands and flags are rejected as unknown input.

- **Trail Brain:** `graft brain connect|pull|push|status|disconnect`, `graft init --brain`, the hidden `_brain-refresh` command, brain rules in `graft ask --source` output, and the `GRAFT_BRAIN_ID`, `GRAFT_BRAIN_TOKEN`, `GRAFT_BRAIN_URL` and `GRAFT_NO_BROWSER` variables. `graft uninstall` still strips the legacy `<!-- graft:brain:start -->` blocks from instruction files.
- **LLM naming:** `graft blast --name`, its `areas.json` naming cache, the global `--provider`, `--model`, `--api-key` and `--base-url` options, and the `GRAFT_PROVIDER`, `GRAFT_API_KEY`, `GRAFT_MODEL`, `GRAFT_BASE_URL`, `GRAFT_LLM_RETRIES`, `OPENROUTER_API_KEY`, `OPENROUTER_BASE_URL`, `GRAFT_OPENROUTER_MODEL`, `ORCAROUTER_API_KEY`, `ORCAROUTER_BASE_URL` and `ORCAROUTER_MODEL` variables. `graft blast` areas keep their symbol names.
- **Telemetry:** `graft telemetry`, the hidden `_telemetry-flush` and `_install` commands, the first-run notice, the `graft init` usage-stats row, session summaries, `TELEMETRY.md`, and the `DO_NOT_TRACK`, `GRAFT_POSTHOG_KEY` and `GRAFT_POSTHOG_HOST` variables.
- **Update checks:** `graft upgrade`, the hidden `_update-check` command, `~/.graft/update-check.json`, and the "graft X → Y available" nudge on the CLI, MCP startup and hooks. `graft version` prints the installed version only.
- **Blast workflows:** the `blast.yml` and `blast-cache.yml` GitHub workflows and the `graft-blast` composite action. The `graft blast` command stays.

### Changed

- The npm package is launcher-only and has no runtime or development dependencies. Its scripts build the host-native Go binary and run plain-JS launcher, shim, and postinstall tests.
- Claude Code, Codex, and Cursor hooks plus the Claude statusline now execute the native binary through plain-JS shims.
- `docs/cli-contract.json` is Go-owned and checked against the full command tree and production environment inventory.

## 0.19.0

### Added

- **Push a repo's brain from the terminal** (#422): a loopback handoff hands the
  browser a finished brain instead of a half-built one — the push waits for the
  build, then lands the browser on the result.
- **A public repo can be read without installing the App** (#349): access is
  answered on its own, without first reading the repo.

### Fixed

- **The finished push lands in Trail** (#417), not on a local page, and signup
  goes to Trail's own front end rather than the shared agents host.
- **A repo read runs in a child process** so the app keeps answering while it
  works, and the digest finishes sending before the child disconnects.
- **Public reads borrow an installation token** instead of falling back to the
  anonymous rate limit.

## 0.18.0

### Added

- **Trail Brain integration** (#322): graft can build a *brain* from a repo and
  carry its rules into every `ask` — a two-way link, so retrieval is shaped by
  the team context a brain accumulates, not the code graph alone.
- **A brain verifies the checkout before it mines** (#344): graft checks the
  working copy against the repo a brain expects, so rules are never mined from
  the wrong tree.

### Fixed

- **A brain refreshes its rules from upkeep** (#343), so a single empty pull no
  longer leaves it stuck without rules.

## 0.17.0

### Added

- **Claude Code sessions now report the dollar value of what graft saved** (#282),
  not just the token count — the running total on the statusline is priced, so
  the payoff of routing a lookup through the graph instead of reading files whole
  is visible in the terminal.
- **`graft init` also writes the Claude Code wiring into `~`** (#276), so
  worktrees and fresh shells created off the same home keep graft available
  instead of losing the hooks and statusline the moment you leave the repo root.

### Fixed

- The **PR-review GitHub App** keeps its review pages on disk (#278) so posted
  links survive a restart, reviews **merged and closed PRs** via the head ref
  diffed against the merge base (#279, #280) so a merged PR keeps a working graph
  link, and **runs each review in its own process** (#281) so one slow review no
  longer blocks the server.
- **Green main** (#277): the telemetry test clears every CI environment variable
  before asserting, and the CodeQL action pins are realigned.

## 0.16.0

### Added

- **`graft init --no-statusline`** (and `GRAFT_NO_STATUSLINE=1`) skips writing
  Claude Code's `statusLine` / `subagentStatusLine`. A custom bar — in the
  project's `.claude/settings.json` or in `~/.claude/settings.json` — stays in
  front: a project-level field would otherwise hide the user-level one. The
  choice is recorded in the wiring stamp, so a later session refresh cannot
  put Graft's bar back. Graft still recognises its own helper
  (`graft-statusline.cjs`) and will update that command on re-init.

## 0.15.0

### Added

- **Swift gets full-fidelity (depth-tier) extraction**, promoted from the
  breadth tier the same way Kotlin was (#130) — whose swift tags query had no
  call captures at all, so Swift repos indexed symbols with zero wiring.
  `tree-sitter-swift` (native, `^0.7.1` — the first release whose install
  compiles the shipped parser instead of regenerating it, which broke under
  npm's hoisting; a root `overrides` entry pins its `tree-sitter` peer for dev
  installs) parses `.swift`. One `class_declaration` node covers `class` /
  `struct` / `enum` / `actor` / `extension`, told apart by their own keyword:
  class and actor → `class`, struct → `struct`, enum → `enum`, `protocol` →
  `interface`, `typealias` → `type`, top-level `let`/`var` → `variable`. An
  `extension Point` node takes the extended type's own name, so its members
  mint as Point methods and member calls on a Point receiver resolve to them.
  The extension node itself is kind `module`, not a second same-named class —
  otherwise every `Point()` call and `: Point` heritage target would go
  ambiguous and drop. `init` becomes a method named after its type (like a
  Java constructor); `func` is a method inside any type and a function
  elsewhere. Calls resolve via `call_expression` with `navigation_expression`
  / `self` / `super` receivers. A bare lowercase call inside a type body is
  ONE edge carrying both of its readings in Swift's own inner-scope-first
  order: the member reading first (owner-qualified index + the in-repo
  ancestor chain — a stdlib call like `contains` inside `extension Set`
  drops instead of binding to an unrelated type's only same-named method, a
  false positive dogfooding on swift-composable-architecture caught), then
  the free-function reading only when no member exists on the chain — so a
  name defined as both yields the member edge alone, as Swift dispatches it.
  `super.method()` resolves against the declaration's own superclass (the
  first `:` entry, which Swift's grammar puts before any protocol), climbing
  past the current class's override. Overloads disambiguate by declared
  arity vs call-site argument count (Java's exact mechanism — defaults and
  variadics make the arity a minimum); an overload set arity can't split
  (`save(Int)` vs `save(String)`) DROPS rather than taking the same-file
  tiebreak, which would stamp whichever overload appears first `extracted`.
  An initializer call (`Animal(legs: 4)` — an ordinary call node, no `new`)
  falls back to class/struct/enum targets once functions find nothing,
  Python's constructor-fallback shape. The `:` inheritance clause yields `extends`
  edges (bare names — Swift can't say syntactically which specifier is the
  superclass), `import` declarations yield module-path import edges, and
  visibility maps to `exported` as only `private`/`fileprivate` hidden —
  Swift's default `internal` is module-wide, which for a one-module repo is
  the API surface. A Swift bindings collector types receivers from the
  confident, syntax-local clues: typed parameters (`func feed(animal:
  Animal)`, argument labels handled), typed properties, initializer-call
  assignments (`let vet = Vet()` — UpperCamelCase callee, the same convention
  trust as Go's `NewX`), and fields bound both bare and `self.`-prefixed —
  so `vet.check()`, `keeper.wave()`, `self.repo.save()`, and type-member
  calls (`Animal.census()`) all resolve through the owner-qualified method
  index. A receiver with no local clue (a chained call's result) stays
  unresolved rather than guessed.
### Fixed

- **`allowScripts` now names R by the identity npm actually matches on.** The
  entry was `tree-sitter-r@1.3.0`, the alias in `dependencies`, but npm derives
  the identity from the resolved package in the lockfile —
  `@davisvaughan/tree-sitter-r@1.3.0`. The old key matched nothing, so the
  grammar's install script counted as unreviewed and was blocked;
  `npm ci --strict-allow-scripts` failed on it. Harmless in practice only
  because the package ships prebuilds for every supported platform. Note the
  `overrides` key must stay the alias — the two fields key differently.
- **`graft check` no longer reports every container-tier node as `removed`.**
  `checkGraph` branched on the depth and breadth tiers but never on the container
  tier the build uses for `.vue`, so `genericLangOf` returned null, a `generic!`
  assertion threw, and the catch swallowed it as a parse failure — every `.vue`
  node fell through to `removed` on a clean build, and the `graft build` the
  check told you to run had already written them. The check now mirrors the
  build's three-way branch, warms the container grammars alongside the generic
  ones, and treats "no tier claims this file" as an explicit case rather than a
  non-null assertion, so the next tier added fails in the type checker instead of
  silently reporting drift. ([#236](https://github.com/trailhq/Graft/issues/236))

### Added

- **Telemetry can now tell "graft saved tokens" apart from "the user was told".**
  `saved_tokens_bucket` counted what graft *computed* — every
  `[graft] tokens saved ≈ N` footer the PostToolUse accumulator swept up — so a
  turn that saved 20,000 tokens in silence and a turn that saved nothing were the
  same number. `session_summary` now also carries `graft_turns_bucket` and
  `reported_turns_bucket`: of the turns that used graft, how many closed with the
  one-line tally `SKILL.md` and `SAVINGS_TURN_NUDGE` both ask for.

  The agent's own prose lives in one place a hook can reach, so at the end of a
  turn that used graft the Stop hook reads the tail of the host transcript named
  on its stdin and checks the reply for a "graft saved ~N tokens" line. Only a
  count of turns is kept, and only as a bucket — no reply, no fragment, not even a
  length. `TELEMETRY.md` documents the local read in full.

  A turn that can't be checked — a host whose Stop hook names no transcript, an
  unreadable file, a Stop racing the transcript write — is counted in *neither*
  total, so the ratio means "of the turns we could read" rather than deflating to
  zero on every editor but Claude Code. One reply is one turn however often Stop
  fires, guarded by the id of the last reply examined.

- **`scripts/tally-audit.mjs`**, the same ratio offline and at full resolution:
  which turns were silent, whether the number the agent reported matched graft's
  own footers, and whether it named the call count. Run it when the aggregate says
  something surprising. It also flags per-turn savings estimates large enough to be
  artifacts rather than savings.


- **`graft init` converges instead of accumulating, and `graft uninstall` removes
  graft entirely.** `init` wrote the files the selected agents needed and never
  looked at the rest, so a repo wired by an older version — or by the same version
  with different `--agents` — kept that run's files forever, and the session-start
  refresh then kept them *up to date*. `init` now retracts every agent it isn't
  about to write, and `graft uninstall` retracts the lot.

  Only graft's own contribution is touched: inside a shared file just the
  marker-fenced block, inside a config just the `graft` key — foreign MCP servers,
  hooks, statuslines and ignore entries survive byte for byte. A file left holding
  nothing is deleted rather than truncated to an empty shell, and the directories
  that empties are pruned. An unparseable config is reported and left alone.
  `uninstall` is dry-run until `-y`.

  The target list is derived from the same registries `init` writes through, so a
  host added later is retractable for free; only a host *removed* from the registry
  needs a hand-written entry, and `LEGACY_TARGETS` says so. Exclusion is by path as
  well as by host id: three hosts write `AGENTS.md`, and keeping any one of them has
  to spare that block.

### Fixed

- **A stale `[mcp_servers.graft]` is now replaced instead of skipped.** The TOML
  writer returned early the moment the header existed, which froze the launch
  command at whatever the first `init` wrote — a repo wired when graft wasn't on
  `PATH` kept the slow `npx` form forever, and no upgrade could correct it. Codex
  and Grok both went through that path. Foreign tables are untouched either way.

- **`.claude/settings.json` no longer accumulates graft's own entries.** The
  allowlist and `footerLinksRegexes` were append-only, so a renamed invocation form
  stayed in the user's settings beside its replacement with nothing able to remove
  it. Both now drop graft's prior entries before adding the current set, the same
  way the hooks merge already did. Scoped to the forms graft is actually invoked as,
  so a hand-written `Bash(graft-mytool:*)` survives.

- **`graft blast` suggests who to tag.** The comment already named the areas a
  diff changes and the areas it can affect; it now names the people behind them,
  read from git history with no API call and no config file. One `git log` per
  area over that area's own files, weighted towards recent work (120-day
  half-life), with areas the diff only *reaches* counted at 0.6 against a changed
  area's 1.0 — the person whose code your change can break is exactly the
  reviewer the diff alone would never surface. The markdown report gains a `Tag:`
  line under the tests line and a collapsed `Who knows this code` table; the
  exported page gains initials badges on each bubble, a *Who knows this* block in
  the detail panel, and a `people` legend toggle.

  A handle is never guessed: only a GitHub noreply commit address resolves to
  `@mention`, and anyone else is printed as a plain unlinked name, because a
  guessed mention pings a stranger. A repo can fix that for good with a
  `.mailmap` entry, which git applies to the names `blast` reads. Merge commits,
  bots, everyone who authored a commit in the diff range, and — for a local run
  with no `--base` — your own git identity are all excluded. `--no-owners` turns
  the layer off; `--pr-author <who...>` takes logins, names or emails; the
  bundled action gains a `suggest-reviewers` input, defaulting true.

## 0.13.0

### Added

- **Grok (xAI) is a first-class `graft init` host.** Detects `~/.grok` or a
  repo `.grok/` and writes `.grok/skills/graft/SKILL.md` plus a repo-level
  `[mcp_servers.graft]` block in `.grok/config.toml` (Grok's MCP config).
  Select it with `graft init --agents grok`.
- **R language support.** `tree-sitter-r` (`npm:@davisvaughan/tree-sitter-r`;
  the unscoped npm name is a squatted placeholder) parses `.R`/`.r` files.
  Plain functions: every `name <- function(...)` / `name = function(...)` /
  `function(...) -> name` assignment becomes a `function` node (the grammar's
  `function_definition` has no name field, so the name comes from the
  enclosing assignment; right-assign has its own AST shape). Classes — R's
  class systems are library convention, not syntax, so they are recognised by
  call idiom: **R6** (`R6::R6Class(...)` — the class node, `public =`/
  `private =`/`active =` entries as methods, `inherit =` heritage, and `self$`/
  `private$`/`super$` calls resolving to the class or its parent), **S4**
  (`setClass()`/`setMethod()` with `contains =` heritage), **S3**
  (`generic.Class <- function()` only when `generic` is registered locally via
  `UseMethod()` or is one of a small curated base-R set — false negatives over
  false positives), and plain-list mixin bundles (`Foo <- list(public =
  list(...), ...)`) that are spliced across classes instead of inherited. An
  untyped `obj$method()` resolves by bare name to a uniquely-named method
  (ambiguous drops). Visibility: a roxygen `#' @export` tag wins; a roxygen
  block without it means "not exported"; no roxygen falls back to the
  leading-dot convention; R6 `private =` members are unexported.
  `library()`/`require()`/`source()` calls are the import edges. Known gaps:
  S3 generics registered in another file aren't seen (per-file pass); S4
  `signature()` multiple dispatch isn't handled; R6 active bindings are
  ordinary methods.
- **Resumable `graft build --deep`.** The concept phase now checkpoints
  summaries to disk atomically as it runs, so a build interrupted by a session
  or rate limit resumes where it stopped on the next run (content-hash cached,
  no repeated LLM cost) instead of restarting from zero.
- **Kotlin gets full-fidelity (depth-tier) extraction**, and **Lua** and **Nix**
  join the breadth tier. **Dart** now indexes top-level functions and consts.
- **Hermes Agent** is a first-class `graft init` host.
- **Every reported edge quotes the source line** where the call or reference
  happens, in the PR comment, the CLI, and one shared helper.

### Fixed

- **PHP.** Enums with array consts stay in the graph; trait-inherited method
  calls (`$this->traitMethod()`) resolve through the `implements` edge;
  attribute usage is wired as `references` edges; anonymous classes are minted
  as nodes with their `implements` edges.
- **Java.** Generic type arguments (`Base<Item>`) no longer become bogus
  `extends`/`implements` edges or poison call resolution; anonymous-class
  methods no longer take the enclosing type's owner, so calls resolve to the
  real method.
- **Python.** Constructor calls (`Foo()`) resolve to the class instead of being
  dropped.
- **Deep tier.** An empty meaning reply is no longer cached as a permanent
  `pending` state, and per-file failures surface instead of the build exiting
  successfully.
- **`graft ask`.** Distinct files are ranked ahead of one file's sibling spans
  under bounded output, and multi-scope workspaces score comparably across
  scopes.
- **Claude hooks, sync-run, and statusline respect `GRAFT_DIR`**, and the hook
  timeout is also read from user-level settings.
- **Ingest** skips the `_build/` directory (the underscore spelling of a build
  tree).

## 0.12.0

### Added

- **Kotlin moves to full-fidelity extraction.** `.kt` and `.kts` files are now
  parsed by a hand-written tree-sitter extractor — the same tier as TypeScript,
  Python, Go, and Java — instead of the generic breadth grammar, so Kotlin
  symbols, call edges, heritage, and imports resolve with scope awareness. The
  kind mapping now matches tree-sitter-kotlin's real node types (the earlier
  attempt reused Java's, which do not exist in the Kotlin grammar and emitted no
  symbols at all): `class_declaration` is re-read off its own keyword into
  class / interface / enum / annotation, `object` and `companion object` become
  classes, secondary constructors and member functions become methods,
  `typealias` becomes a type, and top-level `val`/`var` become variables.
- **Kotlin edges.** Calls resolve through `call_expression` (member calls via
  `navigation_expression`, with `this`/`super` receivers), the `:` heritage
  clause yields `extends` edges, `import_header` yields import edges, and
  `internal`/`private`/`protected` visibility maps to the exported flag.
### Changed

- **graft now collects anonymous usage stats, and the README no longer says it
  doesn't.** We had no way to tell whether a repo ever got past `graft build`,
  or whether an agent reaches for graft over grep once it has — npm downloads
  answer neither. Six events, all buckets and fixed enums: `first_run`,
  `init_completed`, `build_completed`, `build_failed`, `query`,
  `session_summary`. Never your code, file paths, repo name, symbols, queries,
  prompts, or error messages — [`TELEMETRY.md`](TELEMETRY.md) is the complete
  contract and `src/telemetry/contract.ts` enforces it as a hard allowlist, so
  a property that is not in the document cannot be sent even by accident.

  Identity is two random UUIDs (one per machine, one per checkout), derived from
  nothing; events are anonymous in PostHog with no person profile. Nothing is
  sent from a command you run — events queue locally and a detached process
  posts them at most once a day, so no query ever waits on the network.

  Off if you uncheck the box in `graft init`, run `graft telemetry disable`, set
  `DO_NOT_TRACK`, are in CI, or built from source (the key is stamped in only at
  publish time, so forks never send). `graft telemetry debug` prints the exact
  batch your machine would send, and sends nothing.

## 0.11.0

### Fixed

- **Node 24 no longer aborts breadth-tier builds with `Fatal process out of
  memory: Zone`.** The broader `tree-sitter-wasm` grammar bundle avoids the V8
  Turboshaft failure triggered by the previous bundle. CI now exercises every
  breadth grammar under Node 24 to keep that runtime compatibility pinned
  ([#122]).
- **`npm install -g @nanonets/graft@latest` could silently do nothing.** The
  generated shims (`.claude/helpers/graft-*.cjs`, and Codex's
  `~/.codex/hooks/graft/`) locate the installed package at runtime from four
  candidates, and took the *first* one that existed. The first is the absolute
  `dist/claude` path graft happened to be running from when `graft init` ran, so
  switching Node versions (nvm/volta) or moving the install left that directory
  on disk — still first, still winning — and the upgrade replaced a directory
  the shim never looked at. The upgrade appeared to succeed and changed nothing.
  The shims now read each candidate's `package.json` and load the
  **highest-versioned** one. The `npm root -g` subprocess is still reached only
  when all three cheap candidates miss, so hook latency is unchanged.

### Added

- **The wiring now follows the binary.** `graft init` writes files *into* a repo
  (hooks, shims, skill, rule files) and into `~/.codex`; upgrading the npm
  package replaced the binary and touched none of them, so a repo wired by 0.7
  kept 0.7's prompts and 0.7's hook timeouts indefinitely — and nothing
  agent-facing ever mentions `graft init`, so no agent would think to re-run it.
  `graft init` now records a stamp (version, hosts, flags) in
  `graft/.cache/wiring-stamp.json`; every entry point compares it against the
  running binary and re-runs the writes on a mismatch. Hosts come from the union
  of the stamp and what's on disk, so a rule file that went missing is restored
  rather than dropped from all future refreshes. The init flags are replayed, so
  a repo wired with `--no-global` or `--no-hooks` keeps that choice. The refresh
  never builds the graph (it runs at session start, where a rebuild would stall
  the first turn) and is fail-soft throughout.
- **An upgrade nudge.** A machine-global 24h cache
  (`~/.graft/update-check.json` — one registry request a day per machine, not
  per repo), filled by a detached `graft _update-check` child, feeds a one-line
  "newer version available" notice. Hooks only ever *read* that cache; the CLI
  and the MCP server are the fillers, because a hook that shelled out to
  `npm view` would spend its whole timeout on the network.
- Both run from one shared code path, called at three entry points so no host
  is left out: Claude Code's `SessionStart` hook, the MCP server's `initialize`
  (the only channel that reaches Cursor, which has no hooks — the lines ride in
  `instructions`, since stdout carries protocol messages only), and a CLI
  `preAction` hook for every command except `version`, `upgrade`, `mcp` and
  `_update-check`.

## 0.9.0

### Added

- **`graft build --include-dir <name>`** — an explicit, persisted override for
  `SKIP_DIRS` (repeatable: `--include-dir build --include-dir tools`). Some
  ecosystems keep genuine hand-written source under a directory name graft
  otherwise treats as build output (e.g. a `build/` that isn't generated).
  The override is persisted per repo in Git-ignored `.graft/config.json`: set
  it once and every later no-flag `graft build`, plus the hooks/refresh path
  (which never sees CLI flags at all), include it identically. It lifts only
  graft's own skip list — in a
  Git repository, Git's ignore rules stay authoritative, so a directory that
  is both skip-listed and gitignored needs un-ignoring (or `git add -f`) too,
  the same contract indexing already applies to tracked-but-ignored files.
  Dot-directories are never overridable. Reaches the wiring graph, the Tier-2
  markdown/concept pipeline, Go module discovery, and workspace child builds
  alike, and is validated up front (a bare directory name only — no paths, no
  dot-prefixes).

### Fixed

- **`graft map` no longer promotes unrelated methods into hubs and hotspots.**
  A member call with an unknown receiver could be wired to the repository's
  only method with the same bare name, so built-ins such as `Map.set()` inflated
  an unrelated user-defined `set` method. Member calls now require an
  owner-qualified receiver-type match; unresolved calls are dropped rather
  than guessed ([#35]).

- **Indexing now respects `.gitignore`.** In Git repositories, graft indexes
  tracked files plus untracked files that Git does not ignore, so generated
  output such as `Scripts/bundles/` and `Scripts/transpiled/` is no longer
  parsed merely because its extension is supported. Nested ignore files,
  negations, and global Git excludes follow Git's own rules; non-Git directories
  retain the existing filesystem walk and built-in skip list ([#39]).

- **`graft ask` no longer lets normalization undo test-file de-ranking.** Test
  files were penalized before lexical scores were normalized, but the strongest
  test match was still normalized back to the maximum score. The test prior now
  also applies to the final lexical/graph blend, while test-seeking queries keep
  the existing unpenalized behavior ([#37]).

- **`callers` now includes imported functions used as values.** Named imports that
  are passed, returned, or stored are reported as weaker `references` edges,
  while direct invocations remain `calls` ([#34]).

- **The build banner and repo map now name the language, not its parser.** JavaScript
  files (`.js`, `.mjs`, `.cjs`) use the TypeScript grammar internally, and `.jsx`
  uses the TSX grammar, but reporting those parser names made indexed files look
  absent. Coverage now reports `javascript` and `jsx` alongside the existing
  `typescript`, `tsx`, `python`, and `go` labels ([#36]).

- **README: `init` does not write a `CLAUDE.md` section.** Claude Code receives the
  wholly-owned `.claude/skills/graft/SKILL.md`; existing `CLAUDE.md` content is
  never touched ([#36]).

- **Windows: `graft upgrade` no longer reinstalls over an npx run.** The npx-cache check
  matched `/_npx/` against a path that arrives with the platform separator, so it was
  always false on Windows and `graft upgrade` ran `npm install -g` instead of explaining
  that npx already fetches the latest build on every run.

- **Windows: a git worktree kept the graph it was seeded with, but not the record that
  makes it cheap.** The seed's copy filter dropped every sidecar next to the graph — the
  freshness fingerprint included — so the query right behind the seed found no
  fingerprint, could not diff against the parent checkout, and re-parsed the whole repo.
  It answered correctly the whole time, which is why nothing reported it; the only
  visible trace was a `(? files changed)` note instead of a count.

- **`graft init` printed one path with two separators** on Windows (`~\.codex/`), from a
  `/` concatenated onto an otherwise native display path.

- The `windows-latest` CI leg now **gates** rather than merely reporting. The 21 failures
  it shipped with are resolved: two were the real bugs above, most of the rest were tests
  asserting `/` in paths that are deliberately printed with the native separator or
  pointing a child process at `HOME` (Windows reads `USERPROFILE`), and four are now
  explicit named skips — nothing in Node's `fs` can deny a *read* on Windows, and there
  is no exec bit or `SIGTERM` to test.

- **Windows: path scoping and `map` work again.** graft stores a repo-relative path
  for every indexed file — in node ids, `node.path`, the extract cache, the freshness
  fingerprint — and it was produced with `relative()`, which returns the *platform*
  separator. So on Windows every stored path was `src\gate.ts`, while the query layer
  parses those strings with `/` by hand. Nothing errored; it just matched nothing:

  - `ask --in <path>` reported `nothing indexed under "…"` for **every** prefix,
    making path scoping unusable on the platform ([#33]).
  - `map` saw one path segment instead of several, so it emitted one single-file
    "directory" per file — on a large repo spending its whole token budget describing
    ~16 arbitrary files instead of the repo's shape ([#35]).
  - `callers <file.ts>`-style filename lookups missed.

  Repo-relative paths are now normalized to posix once, where they are created
  (`src/util/paths.ts`), instead of defensively at each consumer. Mac and Linux are
  unaffected — the conversion is the identity there, and existing graphs are
  byte-identical. **On Windows every cache key changes**, so the first `graft build`
  after upgrading re-parses the repo once and `graft check` may report drift until it
  runs. One-time, and `graft/` is a local gitignored cache — nothing to migrate.

  CI now runs a `windows-latest` leg, because this whole class of bug is invisible to
  a posix-only matrix.

### Changed

- **`--in` means the same thing on every command.** `ask --in` matched a segment-aware
  path prefix while `grep --in` and `callers --in` matched a bare substring, so
  `grep --in src` also swept up `lib/mysrc/`. All three now use the prefix rule, and
  all three accept either separator (`--in server\src\gpu` works on Windows). A prefix
  matching nothing indexed is now a loud error on all three rather than — for `grep`
  and `callers` — empty output the caller had to interpret.

  This is stricter: a mid-path fragment like `--in gpu` for `server/src/gpu` no longer
  matches. Pass a real prefix (`--in server/src/gpu`), a full file path
  (`--in src/a.ts`), or use `grep`'s pattern to match on content.

- **Duplicate-named definitions no longer silently collide onto one graph node
  id.** A branch-guarded redeclaration, a reopened class, or any other same-name
  definition within a file used to mint the exact same node id as an earlier
  definition, so the second one silently overwrote the first in every id-keyed
  lookup (`callers`, `ask`, MCP tools). Every definition now mints a unique id
  (`~2`, `~3`, ... on a document-order duplicate), and a qualified query
  (`Class.method`) now matches every duplicate, not just the first.

- **UTF-16LE source is now decoded consistently everywhere graft reads repo
  source.** `graft build`'s parse, `check`'s and `fingerprint`'s drift hashes,
  the context summarizer's input, `ask --source`'s span slicer, and `grep` each
  read files with their own `readFileSync(file, "utf8")` — hashing what the
  parser actually sees wasn't guaranteed, and a UTF-16LE file (the common
  encoding Windows tooling writes) got silently mojibake'd by some readers and
  not others. All of them now share one `readSourceFile`, so a file decodes
  identically no matter which command reads it. UTF-16BE, unsupported by
  Node's built-in decoders, is a clean skip (an empty entry) rather than a
  mojibake read.

- **`graft callers`'s zero-hit note now says when the query name itself is
  ambiguous.** When a symbol name is defined more than once, name resolution
  drops a cross-file call to it rather than guessing which definition it means
  — so a zero-hit result could really mean "something calls this, but the edge
  was dropped for being ambiguous." The note now states how many definitions
  share the name.

[#33]: https://github.com/h0rn3t/Graft/issues/33
[#34]: https://github.com/h0rn3t/Graft/issues/34
[#35]: https://github.com/h0rn3t/Graft/issues/35
[#36]: https://github.com/h0rn3t/Graft/issues/36
[#37]: https://github.com/h0rn3t/Graft/issues/37
[#39]: https://github.com/h0rn3t/Graft/issues/39

## 0.8.2

### Fixed

- **`graft ask` no longer buries source under test files on pytest-style repos.** The
  test-de-rank (`isTestPath`) matched test directories (`tests/`, `spec/`) and suffix
  names (`_test`, `.test`, `.spec`) but missed Python's dominant `test_*.py` filename
  **prefix** and `conftest.py`. On repos whose tests live outside a `tests/`-named
  directory (e.g. a `t/unit/` layout), tests were not de-ranked and swamped `ask`
  results. The prefix and `conftest.py` are now recognized.

## 0.8.1

### Changed

- **Every graft query now refreshes the graph before it answers.** Freshness used to be the
  `Stop` hook's job — it rebuilt once the turn had ended — so every query an agent made
  between its first edit and the end of that turn answered from a graph that no longer
  matched the file it had just changed, and it stayed that way indefinitely if the
  background sync failed. Edits made outside the agent (your editor, a branch switch, a
  stash) set no flag at all, so the statusline read `✓ synced` while the graph was behind.

  `ask`, `grep`, `callers`, `skeleton` and `map` now stat the working tree against the last
  build's fingerprint (~3ms) and rebuild only if something moved. `check` is exempt — it is
  the drift report, and refreshing first would make it always say OK.

  A refresh writes only what a query reads: the wiring graph, the `ask` sidecar, and the
  freshness record. It does **not** rewrite the markdown cards, `INDEX.md`, or your
  `.gitignore` — a query is a read, and those stay the job of an explicit `graft build`
  (which is what the Claude Code `Stop` hook already runs at the end of a turn). So the
  retrieval tools are always current, while the markdown you might `grep` by hand can lag
  an edit until the turn ends.

  The refresh is structural and `$0`: it never calls the LLM, so `graft check` still reports
  concept-node drift and stale summaries until you run `graft build --deep` yourself. A
  refresh that fails answers from the graph on disk rather than failing the query.

  ```bash
  graft ask "..." --no-refresh     # answer from the graph exactly as it is on disk
  GRAFT_NO_REFRESH=1               # same, for every command in the process
  ```

### Added

- **Incremental extraction.** `graft build` memoizes each file's parse under `graft/.cache/`
  and replays the files whose bytes have not moved, so a rebuild costs roughly the files
  that changed: on this repo (124 files) **0.74s cold against 0.18s after one edit**. Output
  is byte-identical to a cold build. The memo is discarded automatically when the extraction
  code or the graft version changes, so a stale parse can't outlive an upgrade. `graft build`
  now reports `parsed: N of M files (K replayed from cache)`, and `graft build --no-reuse`
  forces a cold parse of everything.

  Only the *parse* is skipped — every file is still read and hashed on every build. A stat
  may decide whether a query bothers rebuilding; it may not decide what the rebuild itself
  looks at, or `graft check` (which always re-hashes) could report drift that the `graft
  build` it recommends refuses to repair.

- **`GRAFT_REFRESH=hash`** — confirm every file by hashing its contents instead of trusting
  size and mtime, for tooling that rewrites files while preserving both.

### Fixed

- **A git worktree is no longer blind.** `graft/` is gitignored, so `git worktree add`
  never checks it out — and the graph is the only thing the MCP tools read. Every tool in
  a fresh worktree answered `no matching nodes` / `no graph found` for the whole session,
  and `INDEX.md` and the cards were missing too, so `grep` and the repo map came up empty.

  A query in a worktree now copies the parent checkout's graph and query sidecars in, then
  treats the difference between the two checkouts as ordinary drift. The worktree's `.git`
  is a file naming its parent, so there is nothing to configure; the copy is $0 and
  offline, and the Tier-2 meaning layer survives it (a cold rebuild would have thrown away
  every summary you paid for and re-parsed the repo). `graft build` in a worktree starts
  from the same copy, so it is incremental too — and it is what writes the worktree's
  cards and `INDEX.md`, generated from *this* checkout's code rather than copied from the
  parent's branch. A query still writes only what a query reads.

  Reads the parent, never writes to it. No-ops unless there is genuinely a built parent
  checkout on disk — a fresh clone, CI, or a cloned (rather than worktree'd) cloud session
  behaves exactly as before. `GRAFT_NO_SEED=1` turns it off.

## 0.8.0

### Changed

- **`graft init` now asks which agents to wire, instead of writing files for every
  agent it detects.** Detection keyed off directories in `$HOME`, so anyone who had
  tried several coding CLIs got instruction files and MCP configs for all of them —
  plain `graft init` effectively behaved like `--all-agents`. On a terminal it now
  shows every known agent, which ones were detected, and the exact files each would
  write, and wires only what you select (Claude Code pre-selected).

  **Migration —** `graft init` in CI, a Dockerfile, or any non-interactive shell now
  writes **nothing** and prints the command to run instead. Add `--yes` for the old
  behaviour, or `--agents <ids>` to be explicit:

  ```bash
  graft init --yes                  # wire every detected agent (pre-0.8 default)
  graft init --agents claude        # or name them
  ```

### Added

- **`graft init --dry-run`** — print every path `init` would touch, then exit without
  writing. Out-of-repo writes get their own section.
- **`graft init --no-global`** — skip every write outside the repo. Selecting the
  `agents` host writes to `~/.codex/config.toml`, `~/.codex/hooks.json`, and
  `~/.codex/hooks/graft/`; those are user-level and apply to every repo you open with
  Codex, and previously nothing suppressed the `config.toml` write (`--no-hooks` only
  covered the other two). These are now labelled `machine-wide` in the picker.
- **`graft init --yes`** — wire every detected agent without prompting.

## 0.7.0

### Changed

- **`graft/` is now a local, git-ignored cache, not a committed artifact.** Every
  `graft build` adds `graft/` to the repo's `.gitignore` itself, so the graph is
  regenerated locally (like `node_modules`) rather than shared through git. Commit
  `.claude/` (hooks, skill, statusline, `.mcp.json`) so teammates' agents pick graft
  up; each teammate runs `graft build` for their own graph. `graft check` is now a
  local freshness signal rather than a CI merge gate.

### Removed

- The `bench/` benchmark harness is no longer part of the published repo.

## 0.6.0

Consolidates the structural-traversal surface and wires the MCP server into
Claude Code. **Breaking** — see migration below.

### Breaking

- **Removed `graft callees` and `graft impact`.** Both fold into `graft callers`:
  - `graft callees <symbol>` → `graft callers <symbol> --direction out`
  - `graft impact <symbol> -d N` → `graft callers <symbol> --depth N`
  - `graft callers` with no new flags is unchanged (defaults `--direction in --depth 1`).
- **Removed MCP tools `graft_callees` and `graft_blast_radius`.** The `graft_callers`
  tool now takes optional `direction` (`in`|`out`, default `in`) and `depth`
  (default `1`) parameters covering both:
  - callees → `graft_callers { direction: "out" }`
  - blast radius → `graft_callers { depth: N }` (accepts a file path or symbol,
    same file-seed aggregation the old `graft_blast_radius` did).

  Rationale: a coding-agent tool-selection experiment showed agents never picked
  `graft_blast_radius`/`impact` (they reconstructed it by calling `callers`
  repeatedly) and never picked `callees` (they read the named file instead). One
  well-named command with flags is selected more reliably than three.

### Added

- `graft callers --direction <in|out>` — walk incoming (callers, default) or
  outgoing (callees) edges.
- `graft callers --depth <n>` — walk transitively out to depth N for the full
  blast radius (default 1 = direct edges only). For a file seed at depth >1 the
  walk aggregates over the symbols the file defines.
- `graft init` now registers the graft MCP server in the project's `.mcp.json`
  for Claude Code (previously Claude Code got only hooks + statusline + skill).
  Restart Claude Code to load it. Existing `.mcp.json` servers are preserved.

### Changed

- `graft mcp --help` and docs now list the full tool set
  (`graft_ask`, `graft_callers`, `graft_grep`, `graft_skeleton`, `graft_map`,
  `graft_check`) instead of only three.
- The bundled Claude Code skill and other-agent instructions document the
  consolidated `callers` flags.
