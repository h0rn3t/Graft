## Graft — repo context graph

This repo is indexed in `graft/`: a prebuilt graph of every symbol, its exact
file:line span, and who calls what. Get context from graft before grepping or
opening source files. Every query refreshes the graph first, so results include
uncommitted edits; there is no need to run `graft build` after editing.

- `graft ask "<question>" --source`: how X works or where Y lives, as ranked
  hits with code excerpts inlined. For a known symbol, call `graft read` directly.
  Use `--in` when the relevant path is already known.
- `graft grep "<name or literal>"`: every occurrence over indexed files, grouped
  by enclosing symbol. Use it for exhaustive work and to jump to a symbol you
  can name; raw `grep -rn` is only for files graft doesn't index.
- `graft skeleton <file>`: a file's signatures and spans, ~10× cheaper than
  reading it.
- `graft read <symbol>`: first choice for a known symbol; no preceding search
  or skeleton call is needed. Returns the complete source plus its
  same-directory callees. Use `path::name` or a node ID to disambiguate.
  Collect related definitions together with repeated `--also <symbol>`.
- `graft callers <symbol>`: exact call edges. `--direction out` for
  dependencies, `--depth 2` before a rename or signature change, `--depth all`
  before a multi-file refactor.
- `graft map`: orientation when you're new to the repo.

Act on the answer: cite and edit from the returned spans without re-opening
files to confirm them. Weak hits mean switch tool, not re-ask the same question
reworded; a `[graft] weak match` notice names the tool and term to try. When an
excerpt ends in `… +N lines`, use `graft read path::name` to expand that symbol,
never the whole file. In a multi-repo workspace, hits carry `[scope/]`
labels; narrow with `--in <scope>/`. With the graft MCP server connected, the
same tools are `graft_find_code`, `graft_find_all`, `graft_file_api`,
`graft_trace_calls`, `graft_read_symbol` and `graft_repo_map`.
