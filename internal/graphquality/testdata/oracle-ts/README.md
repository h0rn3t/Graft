# Reviewed TypeScript oracle fixture

`oracle.json` pins the raw SHA-256 of the four source files. Review the source and expected facts together; do not generate expected facts from Graft output. For each added or removed expected fact, explain the source change or correction in the review. See [graph quality usage](../../../../docs/graph-quality.md).

The fixture collects hard cases for static call resolution. The expected facts state what the source really calls, including the calls Graft misses today. Those misses are listed in the manifest `limitations` and pinned by `TestOracleLanguageFixtures`, so a resolver change that fixes or breaks one shows up in review.

## Conventions

- `new X()` is a call to the class node `X`, as Graft records for Java `new` and Python `X()`, whether or not `X` declares a constructor.
- A call inside an anonymous arrow function belongs to the nearest enclosing named symbol. A function value passed as an argument (`each(names, clean)`) is not called by the code that passes it.
- Calls to built-in library methods (`Array.prototype.push`, `String.prototype.trim`) have no node in the fixture and are not facts.
- `contains` covers classes, interfaces, methods (constructors included), interface method signatures, function declarations, and functions assigned to a `const`. Fields and variables are not symbols.
- `implements` links a class to the interface it names and, as in the Go fixture, each method to the interface method it implements.

## Cases in `typescript-calls` (`store.ts`, `util.ts`, `service.ts`)

| Case | Source | Expected fact | Today |
| --- | --- | --- | --- |
| `this.m()` | `Store.put` calls `this.touch()` | `Store.put -> Store.touch` | TP |
| constructor parameter property | `constructor(private store: Store)`, then `this.store.put(...)` | `Repo.save -> Store.put` | TP |
| aliased named import of an exported arrow `const` | `import { normalize as clean }`, then `clean("row")` | `Repo.save -> normalize` | FN |
| `this.m()` on own class | `this.label()` | `Repo.save -> Repo.label` | TP |
| typed parameter, same method name in two classes | `cache: Cache`, then `cache.put("row")` | `Repo.flush -> Cache.put` (not `Store.put`) | TP |
| typed parameter | `cache.describe()` | `Repo.flush -> Cache.describe` | TP |
| named import | `each(names, ...)` | `Repo.load -> each` | TP |
| call in a callback arrow | `(name) => this.store.put(name)` | `Repo.load -> Store.put` | TP |
| `super.m()` | `super.put(clean(item))` in `AuditCache` | `AuditCache.put -> Cache.put` | FN |
| aliased import inside an argument | `clean(item)` | `AuditCache.put -> normalize` | FN |
| inherited method via `this` | `this.describe()` in `AuditCache.report` | `AuditCache.report -> Cache.describe` | TP |
| `new` with a typed local | `const store: Store = new Store()` | `run -> Store` | FN |
| typed local | `store.touch()` | `run -> Store.touch` | TP |
| `new` with an explicit constructor | `new Repo(store)` | `run -> Repo` | FN |
| local inferred from `new` | `repo.load(names)`, `repo.save()` | `run -> Repo.load`, `run -> Repo.save` | TP |
| function value passed as a callback | `each(names, clean)` | `run -> each` only | TP |

`typescript-contains` misses `Saver contains Saver.save`: interface method signatures are not symbols (FN). `typescript-implements` finds `Repo implements Saver` but not `Repo.save implements Saver.save` (FN). `typescript-extends` matches `AuditCache extends Cache`.

## Unassessed calls

`dynamic.ts` exercises a call through a callback parameter (`visit(value)`), an element-access call (`target[name]()`) and an interface-typed receiver (`saver.save()`). Their runtime targets are not a single statically proven function, so this file is outside the assessed `calls` partition. Its declarations are still assessed by the other partitions.
