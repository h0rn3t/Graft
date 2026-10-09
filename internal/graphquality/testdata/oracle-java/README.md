# Reviewed Java oracle fixture

`oracle.json` pins the raw SHA-256 of the four source files in package `shop`. Review the source and expected facts together; do not generate expected facts from Graft output. For each added or removed expected fact, explain the source change or correction in the review. See [graph quality usage](../../../../docs/graph-quality.md).

The fixture collects hard cases for static call resolution. The expected facts state what the source really calls, including the calls Graft misses today. Those misses are listed in the manifest `limitations` and pinned by `TestOracleLanguageFixtures`, so a resolver change that fixes or breaks one shows up in review.

## Conventions

- `new X(...)` is a call to the class node `X`, whether or not `X` declares a constructor. `new Listener() { ... }` instantiates the anonymous class, not the interface `Listener`, so its target is `Repo.anonymous.{anonymous}`.
- Overloads share a name; the second declaration's ID takes the `~2` suffix (`Util.format~2` is `format(String, int)`).
- A lambda is not a symbol: a call in its body belongs to the enclosing method, and its implementation of `Listener.onSave` is not a fact.
- Calls to JDK methods (`String.length`) have no node in the fixture and are not facts.
- `contains` covers types, interface methods, methods, constructors, and anonymous classes. Fields and variables are not symbols.
- `implements` links a type to the interface it names and, as in the Go fixture, each method to the interface method it implements.

## Cases in `java-calls` (`Model.java`, `Util.java`, `Repo.java`)

| Case | Source | Expected fact | Today |
| --- | --- | --- | --- |
| `this.m()` | `this.touch()` in `Store.put` | `Store.put -> Store.touch` | TP |
| unqualified overload, chosen by arity | `format(value, 0)` in `format(String)` | `Util.format -> Util.format~2` | TP |
| unqualified static call | `pad(value, width)` | `Util.format~2 -> Util.pad` | TP |
| `new` with an explicit constructor | `new Repo(new Store())` | `Repo.create -> Repo`, `Repo.create -> Store` | TP |
| typed field, unqualified and via `this` | `store.put(...)`, `this.store.put(...)` | `Repo.save -> Store.put` | TP |
| static call on the class name, overloads by arity | `Util.format("row")`, `Util.format("row", 4)` | `Repo.save -> Util.format`, `Repo.save -> Util.format~2` | FN, FN |
| `super.m()` | `super.save()` | `Repo.save -> BaseRepo.save` | FN |
| typed parameter, same method name in two classes | `cache.put("row")` | `Repo.flush -> Cache.put` (not `Store.put`) | TP |
| inherited method, implicit `this` | `describe()` | `Repo.flush -> BaseRepo.describe` | TP |
| lambda body | `item -> target.put(item)` | `Repo.listener -> Store.put` | TP |
| anonymous class instantiation | `new Listener() { ... }` | `Repo.anonymous -> Repo.anonymous.{anonymous}` | FN, plus FP `Repo.anonymous -> Listener` |
| `new` in an anonymous class method | `new Cache()` | `{anonymous}.onSave -> Cache` | TP |
| call on a new-expression receiver | `new Cache().put(item)` | `{anonymous}.onSave -> Cache.put` | FN |

`java-implements` finds `Repo implements Saver` and `{anonymous} implements Listener`, but not the method-level facts `Repo.save implements Saver.save` and `{anonymous}.onSave implements Listener.onSave` (FN, FN). `java-extends` and `java-contains` match fully.

## Unassessed calls

`Dynamic.java` exercises reflection (`getMethod(name)` and `Method.invoke`) and calls through interface-typed parameters (`saver.save()`, `listener.onSave(item)`). Their runtime targets are not a single statically proven method, so this file is outside the assessed `calls` partition. Its declarations are still assessed by the other partitions.
