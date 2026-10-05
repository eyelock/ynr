import { mkdtempSync, readdirSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

export function tempDir() {
  return mkdtempSync(join(tmpdir(), "spool-"));
}

export function files(dir) {
  try {
    return readdirSync(dir).sort();
  } catch {
    return [];
  }
}

/** Every line of every file in dir, parsed. Each must be a complete JSON object. */
export function lines(dir) {
  return files(dir).flatMap((f) =>
    readFileSync(join(dir, f), "utf8")
      .split("\n")
      .filter((l) => l !== "")
      .map((l) => JSON.parse(l)),
  );
}

export function attrs(list) {
  return Object.fromEntries((list ?? []).map((kv) => [kv.key, kv.value]));
}
