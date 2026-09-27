# Reviewed Go oracle fixture

`oracle.json` pins the raw SHA-256 of all three source files. Review the source and expected facts together; do not generate expected facts from Graft output. For each added or removed expected fact, explain the source change or correction in the review. See [graph quality usage](../../../../docs/graph-quality.md).

The complete `go-calls` scope is `a.go` and `b.go`: `Direct` statically invokes `A.Save`, `Recur` invokes itself, and `Left`/`Right` invoke each other. `B.Save` has the same display name as `A.Save` but is not the target of `Direct`. The current structural extractor misses `Direct -> A.Save`, so the baseline records one FN. The `go-contains` scope covers the named declarations in all three files.

`dynamic.go` exercises an interface call and a function value call. Their runtime targets are not a single statically proven function, so this file is outside the assessed `calls` partition. They are neither TPs nor FNs in that partition. A test injects an edge from `dynamic.go` and verifies it does not affect the call score.
