# Graph quality and oracle fixtures

`graph-quality` reports structural properties of an existing Graft graph. With no oracle, its output and `--strict` behavior are unchanged. In particular, `resolution.resolvedPct` is the share of `calls` edges whose target exists as a graph node; it is not semantic precision. `resolution.unresolvedCalls` repeats the build's count of the calls it bound to no node, by reason — receiver type unknown, member or name ambiguous or not in the graph, external package — and is absent for a graph built before graft recorded it. A falling count for a reason is the measurable effect of a resolver change.

## Run the reviewed fixture

From the repository root, with Go dependencies already available locally:

```sh
GOPROXY=off GOSUMDB=off go run ./cmd/graph-quality internal/graphquality/testdata/oracle-go --oracle internal/graphquality/testdata/oracle-go/oracle.json --json
```

The command verifies the fixture file hashes and selected-file inventory, checks Go syntax, builds a structural graph in a temporary directory, then scores only the manifest's declared partitions. It does not start a language server or change the fixture. The fixture currently matches its oracle in every assessed partition, so `--strict` passes; a new extraction or resolution change that breaks a reviewed fact makes it fail.

`go run ./cmd/graph-quality --help` prints both invocation forms. The original form accepts a repository directory or a graph JSON path. The oracle form requires a fixture root and `--oracle <manifest.json>`.

## Manifest format

The version 1 JSON manifest contains:

- `sources`: every selected source file, with a fixture-relative `path` and SHA-256 of its raw bytes. Paths must stay inside the fixture root.
- `build.extensions` and optional `build.onlyDirs`: the exact structural source selection. A fixture with persisted `.graft/config.json` is rejected because it could silently change that selection.
- `partitions`: each has a name, language, one relation, source files, and the complete expected `(source, relation, target)` facts for those files. Overlapping assessments, duplicate facts, and unknown relations are invalid.
- `limitations`: known cases that this oracle does not assess.

The [Go fixture manifest](../internal/graphquality/testdata/oracle-go/oracle.json) is a concrete example. The `go-calls` partition assesses `a.go` and `b.go`; `dynamic.go` is outside that call scope because interface dispatch and callback targets are not statically determined. `go-contains` assesses all three files, and `go-implements` the types in `a.go` and `b.go` that satisfy the interface in `dynamic.go`.

The Python, TypeScript and Java fixtures (`oracle-python`, `oracle-ts`, `oracle-java`) collect harder cases: calls through `self`/`this`/`super`, constructor-assigned and typed receivers, aliased imports, static and overloaded calls, inheritance and interface implementation, each language's dynamic calls kept outside the assessed calls partition. They do not match their oracles yet: every false negative and false positive is a known resolver gap, named in the manifest's `limitations` and the fixture README. `TestOracleLanguageFixtures` pins each partition's current TP, FP and FN, so a resolver change that fixes or breaks a case changes the test. Scores are separate by partition, so correct `contains` facts cannot hide a missing `calls` fact.

The JSON report has `structural` and `oracle` objects. Each oracle partition includes scope, TP/FP/FN, precision/recall, and exact mismatch identities. A zero metric denominator is JSON `null`, meaning not applicable. Unassessed relations and limitations are listed separately. On an unlabelled repository, use the structural report only; it cannot produce meaningful semantic precision or recall.

## Exit statuses and review

- `0`: valid report; without `--strict`, oracle mismatches are still reported.
- `1`: `--strict` found a structural invariant violation, an FP/FN, or a duplicate actual fact.
- `2`: invalid arguments, manifest, source hashes or inventory, unsafe path, or incomplete fixture build. No oracle score is published.

The oracle is hand reviewed from source, never regenerated from the current graph. For every change to an expected fact, explain the source change or the corrected interpretation in the review. Add a positive and a negative case for new relation extraction; record unsupported cases in `limitations` or outside an assessed partition. A changed golden file alone does not establish correctness.
