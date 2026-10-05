import assert from "node:assert/strict";
import { chmodSync, existsSync, lstatSync, mkdirSync, rmSync, statSync, symlinkSync } from "node:fs";
import { join } from "node:path";
import { test } from "node:test";
import { fileNamePart, SpoolWriter } from "../dist/index.js";
import { files, tempDir } from "./helpers.mjs";

const enc = new TextEncoder();
const x39 = enc.encode("x".repeat(39)); // 40 bytes with the newline

test("rotates at the file size and renames on close", async () => {
  const dir = tempDir();
  const w = new SpoolWriter({ dir, service: "svc", instanceId: "inst", maxFileBytes: 100 });
  for (let i = 0; i < 5; i++) w.writeLine(x39, 1);
  assert.deepEqual(files(dir), ["svc-inst-000001.jsonl", "svc-inst-000002.jsonl", "svc-inst-000003.open.jsonl"]);
  await w.close();
  assert.equal(files(dir)[2], "svc-inst-000003.jsonl");
  for (const f of files(dir)) assert.ok(statSync(join(dir, f)).size <= 100);
  assert.deepEqual(w.stats(), { dropped: 0, errors: 0 });
});

test("nothing is created until the first write", async () => {
  const dir = join(tempDir(), "spool");
  const w = new SpoolWriter({ dir });
  await w.sync();
  await w.close();
  assert.equal(existsSync(dir), false);
});

for (const tt of [
  { name: "under the cap", lines: 3, records: 2, maxBytes: 200, dropped: 0, onDisk: 120 },
  { name: "over the cap", lines: 5, records: 2, maxBytes: 100, dropped: 6, onDisk: 80 },
]) {
  test(`cap: ${tt.name}`, async () => {
    const dir = tempDir();
    const w = new SpoolWriter({ dir, service: "s", instanceId: "i", maxFileBytes: 50, maxBytes: tt.maxBytes });
    for (let i = 0; i < tt.lines; i++) w.writeLine(x39, tt.records);
    await w.close();
    assert.equal(w.stats().dropped, tt.dropped);
    const total = files(dir).reduce((n, f) => n + statSync(join(dir, f)).size, 0);
    assert.equal(total, tt.onDisk);
  });
}

test("a closed file the reader removed stops counting against the cap", async () => {
  const dir = tempDir();
  const w = new SpoolWriter({ dir, service: "s", instanceId: "i", maxFileBytes: 40, maxBytes: 80 });
  w.writeLine(x39, 1);
  w.writeLine(x39, 1);
  w.writeLine(x39, 1);
  assert.equal(w.stats().dropped, 1);
  rmSync(join(dir, "s-i-000001.jsonl"));
  w.writeLine(x39, 1);
  assert.equal(w.stats().dropped, 1);
  await w.close();
});

test("an oversized line is dropped", async () => {
  const dir = tempDir();
  const w = new SpoolWriter({ dir, service: "s", instanceId: "i", maxLineBytes: 10 });
  w.writeLine(enc.encode("x".repeat(10)), 3);
  w.writeLine(enc.encode("{}"), 1);
  await w.close();
  assert.equal(w.stats().dropped, 3);
  assert.equal(files(dir).length, 1);
});

test("an unwritable folder costs counted errors, never a throw", { skip: process.platform === "win32" || process.getuid?.() === 0 }, async () => {
  const parent = join(tempDir(), "readonly");
  mkdirSync(parent, { mode: 0o500 });
  try {
    const w = new SpoolWriter({ dir: join(parent, "spool") });
    w.writeLine(enc.encode("{}"), 3);
    await w.sync();
    await w.close();
    assert.equal(w.stats().dropped, 3);
    assert.ok(w.stats().errors > 0);
  } finally {
    chmodSync(parent, 0o700);
  }
});

test("never writes through a planted link", { skip: process.platform === "win32" }, async () => {
  const dir = tempDir();
  const target = join(tempDir(), "target");
  symlinkSync(target, join(dir, "s-i-000001.open.jsonl"));
  const w = new SpoolWriter({ dir, service: "s", instanceId: "i" });
  w.writeLine(enc.encode("{}"), 1);
  await w.close();
  assert.equal(existsSync(target), false);
  assert.ok(lstatSync(join(dir, "s-i-000002.jsonl")).isFile());
});

test("writes after close are dropped", async () => {
  const dir = tempDir();
  const w = new SpoolWriter({ dir });
  w.writeLine(enc.encode("{}"), 1);
  await w.close();
  w.writeLine(enc.encode("{}"), 2);
  assert.equal(w.stats().dropped, 2);
});

test("file name parts", () => {
  for (const [input, want] of [
    ["ynm", "ynm"],
    ["../etc", ".._etc"],
    ["a/b", "a_b"],
    ["", "fallback"],
    ["..", "fallback"],
    [undefined, "fallback"],
  ]) {
    assert.equal(fileNamePart(input, "fallback"), want);
  }
});
