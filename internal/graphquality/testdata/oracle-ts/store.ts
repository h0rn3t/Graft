export interface Saver {
  save(): string;
}

export class Store {
  private items: string[] = [];

  put(item: string): void {
    this.items.push(item);
    this.touch();
  }

  touch(): number {
    return this.items.length;
  }
}

export class Cache {
  put(item: string): string {
    return item;
  }

  describe(): string {
    return "cache";
  }
}
