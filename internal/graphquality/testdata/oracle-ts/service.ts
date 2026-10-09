import { Cache, Saver, Store } from "./store";
import { each } from "./dynamic";
import { normalize as clean } from "./util";

export class Repo implements Saver {
  constructor(private store: Store) {}

  save(): string {
    this.store.put(clean("row"));
    return this.label();
  }

  label(): string {
    return "repo";
  }

  flush(cache: Cache): string {
    cache.put("row");
    return cache.describe();
  }

  load(names: string[]): void {
    each(names, (name) => this.store.put(name));
  }
}

export class AuditCache extends Cache {
  put(item: string): string {
    return super.put(clean(item));
  }

  report(): string {
    return this.describe();
  }
}

export function run(names: string[]): string {
  const store: Store = new Store();
  store.touch();
  const repo = new Repo(store);
  repo.load(names);
  each(names, clean);
  return repo.save();
}
