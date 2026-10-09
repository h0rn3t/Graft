# Reviewed Python oracle fixture

`oracle.json` pins the raw SHA-256 of the four source files in the `shop` package. Review the source and expected facts together; do not generate expected facts from Graft output. For each added or removed expected fact, explain the source change or correction in the review. See [graph quality usage](../../../../docs/graph-quality.md).

The fixture collects hard cases for static call resolution. The expected facts state what the source really calls, including the calls Graft misses today. Those misses are listed in the manifest `limitations` and pinned by `TestOracleLanguageFixtures`, so a resolver change that fixes or breaks one shows up in review.

## Conventions

- Instantiating a class (`Store()`) is a call to the class node, not to its `__init__`.
- A call inside a comprehension or nested expression belongs to the nearest enclosing `def`. A nested function is its own symbol (`run.emit`), and its parent contains it.
- Calls to builtins and the standard library (`super`, `len`, `list.append`, `str.split`, `str.strip`) have no node in the fixture and are not facts.
- `contains` covers class, function and method declarations, including nested functions and decorated methods. Variables, attributes and parameters are not symbols.

## Cases in `python-calls` (`util.py`, `store.py`, `service.py`)

| Case | Source | Expected fact | Today |
| --- | --- | --- | --- |
| plain module call | `helper` calls `normalize(value)` | `helper -> normalize` | TP |
| call in a list comprehension | `[clean(part) for part in ...]` | `normalize -> clean` | TP |
| `self.m()` | `Store.put` calls `self.touch()` | `Store.put -> Store.touch` | TP |
| class instantiation | `self.store = Store()` in `Repo.__init__` | `Repo.__init__ -> Store` | TP |
| attribute typed in `__init__` | `self.store.put("row")` in `Repo.save` | `Repo.save -> Store.put` (not `Cache.put`) | TP |
| `super()` call | `super().save()` in `Repo.save` | `Repo.save -> BaseRepo.save` | FN |
| typed parameter | `cache: Cache`, then `cache.put("row")` | `Repo.flush -> Cache.put` (not `Store.put`) | TP |
| inherited method via `self` | `self.describe()` in `Repo.flush` | `Repo.flush -> BaseRepo.describe` | TP |
| instantiation in a `@staticmethod` | `return Repo()` in `Repo.build` | `Repo.build -> Repo` | TP |
| static method on the class name | `Repo.build()` in `run` | `run -> Repo.build` | FN |
| local from a factory | `repo = Repo.build()`, then `repo.save()` | `run -> Repo.save` | FN |
| nested function called by its parent | `[emit(name) for name in names]` | `run -> run.emit` | TP |
| `from .util import helper` | `helper(name)` in `run.emit` | `run.emit -> helper` | TP |
| typed parameter of an imported class | `target: Store`, then `target.put(...)` | `check -> Store.put` | TP |
| module alias | `from . import util as u`, then `u.helper("x")` | `check -> helper` | FN |
| `super()` through a qualified base | `super().put(item)` in `AuditCache(store.Cache)` | `AuditCache.put -> Cache.put` | FN |

`python-extends` covers all four files: `Repo(BaseRepo)` is found, while `AuditCache(store.Cache)` has a module-qualified base and gets no extends edge (FN). `python-contains` covers all four files and matches fully.

## Unassessed calls

`dynamic.py` exercises `getattr(target, name)()`, a dict of bound methods, `functools.partial` and a returned closure. Their runtime targets are not a single statically proven function, so this file is outside the assessed `calls` partition. Its declarations are still assessed by `python-contains` and `python-extends`.
