# Graph-quality baseline, 2026-09-27

This is a descriptive local baseline, not an accuracy or speed target. The repeatable procedure is [run-graph-quality-baseline.sh](run-graph-quality-baseline.sh); its raw outputs are in [graph-quality-baseline/raw](graph-quality-baseline/raw). From the repository root on macOS, run:

```sh
bash docs/run-graph-quality-baseline.sh docs/graph-quality-baseline/raw
```

The script compiles the current working tree's `graft` and `graph-quality` binaries with `GOPROXY=off` and `GOSUMDB=off`, sets `GRAFT_NO_LSP=1`, and uses no external service. It archives the pinned Git revision `ec2c1aa2089bdc7a6f47f8f1191b0d381069327e` as the representative repository input. Each build sample uses a fresh copy of that archive and no extraction cache. Query samples use one graph built from the same archive; `ask` and `read` use `--no-refresh`. The fixture input is pinned separately by [oracle.json](../internal/graphquality/testdata/oracle-go/oracle.json) and [raw file hashes](graph-quality-baseline/raw/fixture-sha256.txt). The measurement binaries were compiled from the implementation source included with this change; [binary hashes](graph-quality-baseline/raw/binary-sha256.txt) identify the exact executables used for these numbers.

Environment: Go `go1.27.1 darwin/arm64`; macOS Darwin kernel 27.0.0 on ARM64. The script uses `/usr/bin/time -l -p`; `max_rss_bytes` is its maximum resident set size for each process. `real_seconds` has 0.01 s display resolution. Output byte counts include stdout only, not stderr or tool startup compilation. Build timing includes the CLI process and graph build, not the initial Go binary compilation. Samples were run sequentially with a warm OS file cache.

| Operation | Samples | Median real time | Median max RSS | Stdout bytes |
| --- | ---: | ---: | ---: | ---: |
| Fresh archive `graft build` | 3 | 1.44 s | 107,839,488 | 399 |
| `graft ask "graph quality report" --source --no-refresh` | 5 | 0.08 s | 62,226,432 | 4,026 |
| `graft read internal/graphquality/report.go::Analyze --no-refresh` | 5 | 0.01 s | 25,100,288 | 4,515 |

The ask and read stdout hashes were stable across their five samples. Build stdout hashes differed because the output names its temporary directory. The pinned query graph SHA-256 is recorded in [query-graph-sha256.txt](graph-quality-baseline/raw/query-graph-sha256.txt). The full rows, including wall time, RSS, bytes, and output hash, are in [samples.csv](graph-quality-baseline/raw/samples.csv); medians can be recalculated with `statistics.median` grouped by `kind`.

The [fixture oracle report](graph-quality-baseline/raw/oracle.json) has 13 nodes and 13 edges. Structural invariants pass. `resolvedPct` is 100% because all three recorded call targets are graph nodes; this is not semantic accuracy. In the reviewed `go-calls` scope (`a.go`, `b.go`), TP=3, FP=0, FN=1, precision=1, recall=0.75. The missing fact is `a.go#Direct calls a.go#A.Save`. In the separate `go-contains` scope, TP=10, FP=0, FN=0. Interface dispatch and function value calls in `dynamic.go` are outside the assessed call scope. No semantic precision or recall is claimed for the archived repository because it has no reviewed complete oracle.

These numbers combine a specific binary, host, input, scope, and command selection. In particular, the 0.01 s read median is at the timer's display resolution, and response bytes are not model token counts. No improvement percentage is inferred from this single baseline.

## Implementation checks

Completed on the implementation working tree: `go test ./...`, `go vet ./...`, `go build ./...`, `go test ./cmd/graft -run '^TestGraphQualityGoldensMatchGo$' -count=1`, `go test -race ./internal/graphquality ./cmd/graph-quality`, `golangci-lint run ./internal/graphquality ./cmd/graph-quality` (0 issues), `go fix -diff ./internal/graphquality ./cmd/graph-quality` (no diff), and `openspec validate extend-graph-quality-baseline --strict` (valid). No environment-dependent checks were skipped. `govulncheck` was not run because this change adds no dependencies and is not a release.
