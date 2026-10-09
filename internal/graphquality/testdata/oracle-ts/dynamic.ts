import { Saver } from "./store";

export function each(values: string[], visit: (value: string) => void): void {
  for (const value of values) {
    visit(value);
  }
}

export function byName(target: Record<string, () => void>, name: string): void {
  target[name]();
}

export function saveOne(saver: Saver): string {
  return saver.save();
}
