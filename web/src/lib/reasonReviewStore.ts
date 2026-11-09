import type { ErasureReasonItem } from "../types";

// The walk's result for the contact-request reason review (T07B-2, F6): a
// framework-free store so a review of tens of thousands of reasons costs O(page)
// to append and O(1) to choose, and nothing is rebuilt per click. Ids are kept
// as canonical decimal strings (a request id is a signed 64-bit value), review
// hashes as raw bytes in one growing array, the choices as one byte each
// (0 none, 1 keep, 2 remove). It never sorts: a page that does not continue the
// ascending signed order is a fail-closed error, because the server's digest is
// over exactly that order.

export const KEEP = 1;
export const REMOVE = 2;
export type ChoiceCode = typeof KEEP | typeof REMOVE;

const INT64_MIN = -(2n ** 63n);
const INT64_MAX = 2n ** 63n - 1n;
const HASH_LENGTH = 64;
const LOWER_HEX = /^[0-9a-f]+$/;

/**
 * The id as a bigint when it is exactly a canonical signed decimal inside int64,
 * else null. The check is the round trip BigInt(id).toString() === id, not a
 * pattern: BigInt trims whitespace and line terminators (so "5\n" parses), reads
 * "0x10" and "" and accepts "+1" and "01", and each of those fails the round
 * trip. No line terminator, space, sign other than "-", leading zero or "-0"
 * survives it.
 */
function canonicalId(id: unknown): bigint | null {
  if (typeof id !== "string") return null;
  let v: bigint;
  try {
    v = BigInt(id);
  } catch {
    return null;
  }
  if (v.toString() !== id || v < INT64_MIN || v > INT64_MAX) return null;
  return v;
}

/** A review hash is exactly 64 lowercase hex characters (the length is checked, not implied by a pattern's end). */
const validHash = (h: unknown): h is string => typeof h === "string" && h.length === HASH_LENGTH && LOWER_HEX.test(h);

/** Thrown for a page the walk cannot trust (order, duplicates, ids, hashes). */
/** @public tested directly: the store's refusals. */
export class ReasonStoreError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "ReasonStoreError";
  }
}

export type PriorChoice = { hash: string; choice: ChoiceCode };

export interface StoreCounts {
  total: number;
  keep: number;
  remove: number;
  unchosen: number;
  changedOpen: number;
}

/** What a press sends: views of ids and hashes (the walk cannot run during a press) and a copy of the choices. */
export interface FrozenReview {
  version: number;
  count: number;
  ids: string[];
  hashes: Uint8Array;
  choices: Uint8Array;
}

const hexByte = (s: string, i: number) => parseInt(s.slice(i * 2, i * 2 + 2), 16);

export class ReasonReviewStore {
  private ids: string[] = [];
  private pages: ErasureReasonItem[][] = [];
  private starts: number[] = [];
  private index = new Map<string, number>();
  private hashes = new Uint8Array(64 * 32);
  private choices = new Uint8Array(64);
  private changed = new Uint8Array(64);
  private last: bigint | null = null;
  private prior: Map<string, PriorChoice> | null = null;
  private keep = 0;
  private remove = 0;
  private changedOpen = 0;
  private version = 0;
  private cached: StoreCounts & { version: number } = { version: 0, total: 0, keep: 0, remove: 0, unchosen: 0, changedOpen: 0 };
  private listeners = new Set<() => void>();
  private ops = 0;


  get total(): number {
    return this.ids.length;
  }

  /**
   * A running count of the per-item steps the store has taken (items appended,
   * carried, scanned or returned, and one for each choice). Tests read it to
   * show that choosing is O(1) and that appending is O(page), whatever the
   * length of the review.
   */
  get operations(): number {
    return this.ops;
  }

  getVersion = (): number => this.version;

  /** The same object until something changes, so useSyncExternalStore can compare it. */
  getSnapshot = (): StoreCounts & { version: number } => {
    if (this.cached.version !== this.version) {
      const total = this.ids.length;
      this.cached = { version: this.version, total, keep: this.keep, remove: this.remove, unchosen: total - this.keep - this.remove, changedOpen: this.changedOpen };
    }
    return this.cached;
  };

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  };

  itemAt(ordinal: number): ErasureReasonItem | undefined {
    // The pages are final once appended, so a lookup is a binary search over their starts.
    let lo = 0;
    let hi = this.starts.length - 1;
    while (lo < hi) {
      const mid = (lo + hi + 1) >> 1;
      if (this.starts[mid] <= ordinal) lo = mid;
      else hi = mid - 1;
    }
    return this.pages[lo]?.[ordinal - this.starts[lo]];
  }

  slice(from: number, to: number): ErasureReasonItem[] {
    const out: ErasureReasonItem[] = [];
    for (let i = Math.max(0, from); i < Math.min(to, this.ids.length); i++) {
      const it = this.itemAt(i);
      if (it) out.push(it);
      this.ops++;
    }
    return out;
  }

  ordinalOf(requestId: string): number {
    return this.index.get(requestId) ?? -1;
  }

  choiceAt(ordinal: number): 0 | ChoiceCode {
    return (this.choices[ordinal] ?? 0) as 0 | ChoiceCode;
  }

  isChanged(ordinal: number): boolean {
    return this.changed[ordinal] === 1;
  }

  /** The next ordinal after `from` (wrapping once) for which `want` holds, or -1. */
  private nextWhere(from: number, want: (i: number) => boolean): number {
    const n = this.ids.length;
    for (let step = 1; step <= n; step++) {
      this.ops++;
      const i = (from + step + n) % n;
      if (want(i)) return i;
    }
    return -1;
  }

  nextUnchosen(from: number): number {
    return this.nextWhere(from, (i) => this.choices[i] === 0);
  }

  nextChanged(from: number): number {
    return this.nextWhere(from, (i) => this.changed[i] === 1 && this.choices[i] === 0);
  }


  private bump(): void {
    this.version++;
    for (const l of [...this.listeners]) l();
  }

  /** Starts a walk over. `prior` is what a reload may keep: an item keeps its choice only when its hash is unchanged. */
  reset(prior: Map<string, PriorChoice> | null = null): void {
    this.ids = [];
    this.pages = [];
    this.starts = [];
    this.index = new Map();
    this.hashes = new Uint8Array(64 * 32);
    this.choices = new Uint8Array(64);
    this.changed = new Uint8Array(64);
    this.last = null;
    this.prior = prior;
    this.keep = this.remove = this.changedOpen = 0;
    this.bump();
  }

  carry(): Map<string, PriorChoice> {
    const m = new Map<string, PriorChoice>();
    for (let i = 0; i < this.ids.length; i++) {
      this.ops++;
      const c = this.choices[i];
      if (c === KEEP || c === REMOVE) m.set(this.ids[i], { hash: this.hashHex(i), choice: c });
    }
    return m;
  }

  private hashHex(i: number): string {
    return Array.from(this.hashes.subarray(i * 32, i * 32 + 32), (b) => b.toString(16).padStart(2, "0")).join("");
  }

  private grow(need: number): void {
    if (need <= this.choices.length) return;
    let cap = this.choices.length;
    while (cap < need) cap *= 2;
    const hashes = new Uint8Array(cap * 32);
    hashes.set(this.hashes);
    const choices = new Uint8Array(cap);
    choices.set(this.choices);
    const changed = new Uint8Array(cap);
    changed.set(this.changed);
    this.hashes = hashes;
    this.choices = choices;
    this.changed = changed;
  }

  /**
   * Appends one page. The whole page is checked before anything changes: ids are
   * canonical signed decimals inside int64 and strictly ascending across the
   * walk, hashes are 64 lowercase hex. Anything else throws ReasonStoreError and
   * leaves the store as it was.
   */
  appendPage(items: ErasureReasonItem[]): void {
    if (items.length === 0) return;
    let prev = this.last;
    const seen = new Set<string>();
    for (const it of items) {
      const v = canonicalId(it.requestId);
      if (v === null) throw new ReasonStoreError("a request id is not a canonical 64-bit integer");
      if (prev !== null && v <= prev) throw new ReasonStoreError("the reasons are not in ascending order");
      if (seen.has(it.requestId) || this.index.has(it.requestId)) throw new ReasonStoreError("a request id appears twice");
      if (!validHash(it.reviewHash)) throw new ReasonStoreError("a review hash is not 64 lowercase hex characters");
      seen.add(it.requestId);
      prev = v;
    }
    const base = this.ids.length;
    this.grow(base + items.length);
    this.starts.push(base);
    this.pages.push(items);
    items.forEach((it, k) => {
      this.ops++;
      const at = base + k;
      this.ids.push(it.requestId);
      this.index.set(it.requestId, at);
      for (let b = 0; b < 32; b++) this.hashes[at * 32 + b] = hexByte(it.reviewHash, b);
      if (this.prior) {
        const old = this.prior.get(it.requestId);
        if (old && old.hash === it.reviewHash) {
          this.choices[at] = old.choice;
          if (old.choice === KEEP) this.keep++;
          else this.remove++;
        } else {
          this.changed[at] = 1;
          this.changedOpen++;
        }
      }
    });
    this.last = prev;
    this.bump();
  }

  choose(ordinal: number, choice: ChoiceCode): void {
    if (ordinal < 0 || ordinal >= this.ids.length) return;
    this.ops++;
    const old = this.choices[ordinal];
    if (old === choice) return;
    if (old === KEEP) this.keep--;
    else if (old === REMOVE) this.remove--;
    if (choice === KEEP) this.keep++;
    else this.remove++;
    this.choices[ordinal] = choice;
    if (this.changed[ordinal] === 1) {
      this.changed[ordinal] = 0;
      this.changedOpen--;
    }
    this.bump();
  }

  /** The press's snapshot. The walk cannot run during a press, so the views stay valid. */
  freeze(): FrozenReview {
    const n = this.ids.length;
    return { version: this.version, count: n, ids: this.ids, hashes: this.hashes.subarray(0, n * 32), choices: this.choices.slice(0, n) };
  }
}
