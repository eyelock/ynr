import { randomBytes } from "node:crypto";
import * as fs from "node:fs";
import * as path from "node:path";

/** Defaults from the spool format. */
export const DEFAULT_MAX_FILE_BYTES = 8 * 1024 * 1024;
export const DEFAULT_MAX_BYTES = 64 * 1024 * 1024;
export const DEFAULT_MAX_LINE_BYTES = 4 * 1024 * 1024;
export const DEFAULT_SYNC_TIMEOUT_MS = 2000;

const OPEN_SUFFIX = ".open.jsonl";
const CLOSED_SUFFIX = ".jsonl";

export interface SpoolWriterOptions {
  /** The writer folder. It is created on the first write. */
  dir: string;
  /** Service and instance id name the files: <service>-<instance id>-<seq>. */
  service?: string;
  instanceId?: string;
  /** Rotates the open file once the next line would pass it. */
  maxFileBytes?: number;
  /** Caps this writer's files on disk. Over it, records are dropped. */
  maxBytes?: number;
  /** Drops an export request larger than this; a reader skips longer lines. */
  maxLineBytes?: number;
  /** Bounds a flush to disk. */
  syncTimeoutMs?: number;
}

/** What a writer could not do. Nothing it counts is ever reported as an error. */
export interface SpoolStats {
  /** Records (spans, log records or metric data points) not written. */
  dropped: number;
  /** Filesystem operations that failed or timed out. */
  errors: number;
}

/** Keeps a name to characters that are safe in a file name. */
export function fileNamePart(s: string | undefined, fallback: string): string {
  const out = (s ?? "").replace(/[^A-Za-z0-9_.-]/g, "_");
  return out.replace(/[._]/g, "") === "" ? fallback : out;
}

// O_NOFOLLOW is absent on Windows, where O_EXCL already refuses an existing name.
const CREATE_FLAGS =
  fs.constants.O_WRONLY |
  fs.constants.O_CREAT |
  fs.constants.O_EXCL |
  fs.constants.O_APPEND |
  (fs.constants.O_NOFOLLOW ?? 0);

/**
 * Appends OTLP JSON lines to a writer's spool files. Each line is written with
 * one synchronous write, so a batch already written survives the process
 * being killed. One writer serves the trace, metric and log exporters.
 */
export class SpoolWriter {
  readonly dir: string;
  private readonly service: string;
  private readonly instanceId: string;
  private readonly maxFileBytes: number;
  private readonly maxBytes: number;
  private readonly maxLineBytes: number;
  private readonly syncTimeoutMs: number;

  private fd: number | undefined;
  private openPath = "";
  private seq = 0;
  private fileBytes = 0;
  private diskBytes = 0;
  private closedFiles: { path: string; size: number }[] = [];
  private shutdown = false;
  private closing: Promise<void> | undefined;
  private counts: SpoolStats = { dropped: 0, errors: 0 };

  constructor(opts: SpoolWriterOptions) {
    this.dir = opts.dir;
    this.service = fileNamePart(opts.service, "service");
    this.instanceId = fileNamePart(opts.instanceId ?? randomBytes(8).toString("hex"), "instance");
    this.maxFileBytes = positive(opts.maxFileBytes, DEFAULT_MAX_FILE_BYTES);
    this.maxBytes = positive(opts.maxBytes, DEFAULT_MAX_BYTES);
    this.maxLineBytes = positive(opts.maxLineBytes, DEFAULT_MAX_LINE_BYTES);
    this.syncTimeoutMs = positive(opts.syncTimeoutMs, DEFAULT_SYNC_TIMEOUT_MS);
  }

  /** What the writer has dropped and failed so far. */
  stats(): SpoolStats {
    return { ...this.counts };
  }

  /** Counts records dropped by an exporter that has shut down. */
  drop(records: number): void {
    this.counts.dropped += records;
  }

  /**
   * Appends one export request, encoded as JSON without a trailing newline,
   * holding `records` records. It never throws: what it cannot write, it counts.
   */
  writeLine(json: Uint8Array, records: number): void {
    const n = json.byteLength + 1;
    if (this.shutdown || n > this.maxFileBytes || n > this.maxLineBytes) {
      this.counts.dropped += records;
      return;
    }
    if (this.diskBytes + n > this.maxBytes) {
      this.reclaim();
      if (this.diskBytes + n > this.maxBytes) {
        this.counts.dropped += records;
        return;
      }
    }
    if (this.fd !== undefined && this.fileBytes + n > this.maxFileBytes) {
      this.rotate();
    }
    if (this.fd === undefined && !this.open()) {
      this.counts.errors++;
      this.counts.dropped += records;
      return;
    }
    const line = Buffer.allocUnsafe(n);
    line.set(json);
    line[n - 1] = 0x0a;
    let written = 0;
    try {
      // A regular file takes the whole buffer; loop only for a short write.
      while (written < n) {
        written += fs.writeSync(this.fd as number, line, written, n - written);
      }
    } catch {
      this.counts.errors++;
      this.counts.dropped += records;
      this.fileBytes += written;
      this.diskBytes += written;
      // A partial line ends this file; the reader skips it as malformed.
      this.rotate();
      return;
    }
    this.fileBytes += n;
    this.diskBytes += n;
  }

  /**
   * Flushes the open file to disk. It resolves within the sync timeout
   * whatever the filesystem does; a flush still running then is abandoned and
   * counted. It never rejects.
   */
  sync(): Promise<void> {
    if (this.fd === undefined) return Promise.resolve();
    return this.bounded(fsyncQuiet(this.fd, this.counts));
  }

  /**
   * Flushes, closes and renames the open file to .jsonl. Records written
   * afterwards are dropped and counted. Bounded like sync; never rejects.
   */
  close(): Promise<void> {
    if (this.closing) return this.closing;
    this.shutdown = true;
    const fd = this.fd;
    if (fd === undefined) {
      this.closing = Promise.resolve();
      return this.closing;
    }
    // Close and rename only once the flush finishes, even if the caller has
    // stopped waiting for it, so the descriptor is never closed under it.
    const done = fsyncQuiet(fd, this.counts).then(() => this.rotate());
    this.closing = this.bounded(done);
    return this.closing;
  }

  private bounded(op: Promise<void>): Promise<void> {
    return new Promise((resolve) => {
      const timer = setTimeout(() => {
        this.counts.errors++;
        resolve();
      }, this.syncTimeoutMs);
      timer.unref();
      op.then(() => {
        clearTimeout(timer);
        resolve();
      });
    });
  }

  private reclaim(): void {
    this.closedFiles = this.closedFiles.filter((c) => {
      try {
        fs.lstatSync(c.path);
        return true;
      } catch {
        this.diskBytes -= c.size;
        return false;
      }
    });
  }

  private open(): boolean {
    try {
      fs.mkdirSync(this.dir, { recursive: true, mode: 0o755 });
    } catch {
      return false;
    }
    // O_EXCL never reuses a file: a name already taken moves on.
    for (let i = 0; i < 100; i++) {
      this.seq++;
      const p = path.join(this.dir, `${this.baseName()}${OPEN_SUFFIX}`);
      try {
        this.fd = fs.openSync(p, CREATE_FLAGS, 0o644);
      } catch (err) {
        const code = (err as NodeJS.ErrnoException).code;
        if (code === "EEXIST" || code === "ELOOP") continue;
        return false;
      }
      this.openPath = p;
      this.fileBytes = 0;
      return true;
    }
    return false;
  }

  private baseName(): string {
    return `${this.service}-${this.instanceId}-${String(this.seq).padStart(6, "0")}`;
  }

  private rotate(): void {
    if (this.fd === undefined) return;
    try {
      fs.closeSync(this.fd);
    } catch {
      this.counts.errors++;
    }
    let closedPath = this.openPath.slice(0, -OPEN_SUFFIX.length) + CLOSED_SUFFIX;
    try {
      fs.renameSync(this.openPath, closedPath);
    } catch {
      this.counts.errors++;
      closedPath = this.openPath;
    }
    this.closedFiles.push({ path: closedPath, size: this.fileBytes });
    this.fd = undefined;
    this.openPath = "";
    this.fileBytes = 0;
  }
}

function positive(v: number | undefined, fallback: number): number {
  return v !== undefined && v > 0 ? v : fallback;
}

function fsyncQuiet(fd: number, counts: SpoolStats): Promise<void> {
  return new Promise((resolve) => {
    fs.fsync(fd, (err) => {
      if (err) counts.errors++;
      resolve();
    });
  });
}
