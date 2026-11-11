import { appendFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import type { ErasureReasonItem } from "../types";
import { KEEP, REMOVE, ReasonReviewStore, ReasonStoreError } from "./reasonReviewStore";

const hex = (n: number) => (n % 0xffff).toString(16).padStart(4, "0").repeat(16);
const item = (id: string | number, hash = hex(Number(id))): ErasureReasonItem => ({
  requestId: String(id), reviewHash: hash, role: "subject", purpose: "contact", status: "pending", createdAt: "2026-11-01T12:00:00Z",
  counterpartName: "Lee", counterpartDeleted: false, reviewerIsParty: false, reason: `r${id}`,
});
const items = (from: number, to: number) => Array.from({ length: to - from }, (_, i) => item(from + i));

describe("ReasonReviewStore", () => {
  it("appends pages in order and keeps item identity", () => {
    const store = new ReasonReviewStore();
    const a = items(1, 4);
    store.appendPage(a);
    store.appendPage(items(4, 6));
    expect(store.total).toBe(5);
    expect(store.itemAt(0)).toBe(a[0]);
    expect(store.itemAt(4)?.requestId).toBe("5");
    expect(store.itemAt(5)).toBeUndefined();
    expect(store.slice(2, 4).map((i) => i.requestId)).toEqual(["3", "4"]);
    expect(store.ordinalOf("4")).toBe(3);
    expect(store.ordinalOf("99")).toBe(-1);
  });

  it("orders ids as signed 64-bit integers, past 2^53, and never sorts", () => {
    const store = new ReasonReviewStore();
    const ids = ["-9223372036854775808", "-10", "-2", "-1", "0", "2", "10", "9007199254740993", "9223372036854775807"];
    store.appendPage(ids.map((id, i) => item(id, hex(i + 1))));
    expect(store.slice(0, 9).map((i) => i.requestId)).toEqual(ids);
    // The old text order is a failure here.
    const text = new ReasonReviewStore();
    expect(() => text.appendPage([item("-1"), item("-10")])).toThrow(ReasonStoreError);
    // 2^53 and 2^53 + 1 are different ids and must compare as such.
    const big = new ReasonReviewStore();
    big.appendPage([item("9007199254740992"), item("9007199254740993")]);
    expect(big.total).toBe(2);
  });

  it.each([
    ["a repeated id on the page", () => [item(3), item(3)]],
    ["an id that goes back", () => [item(5), item(4)]],
    ["a plus sign", () => [item("+1")]],
    ["a leading zero", () => [item("01")]],
    ["minus zero", () => [item("-0")]],
    ["a decimal point", () => [item("1.5")]],
    ["an empty id", () => [item("")]],
    ["an id above int64", () => [item("9223372036854775808")]],
    ["an id below int64", () => [item("-9223372036854775809")]],
    ["an upper-case hash", () => [item(1, "A".repeat(64))]],
    ["a short hash", () => [item(1, "a".repeat(63))]],
    ["a non-hex hash", () => [item(1, "z".repeat(64))]],
  ])("fails closed on %s and leaves the store as it was", (_name, page) => {
    const store = new ReasonReviewStore();
    store.appendPage(items(1, 3));
    const before = store.getVersion();
    expect(() => store.appendPage(page())).toThrow(ReasonStoreError);
    expect(store.total).toBe(2);
    expect(store.getVersion()).toBe(before);
  });

  // JavaScript's $ does not match before a final newline, but the check does not lean on that: an id
  // must survive BigInt(id).toString() === id (BigInt trims every kind of line terminator and space),
  // and a hash must be exactly 64 lowercase hex characters.
  describe("line terminators and spaces", () => {
    const terminators = { LF: "\n", CR: "\r", CRLF: "\r\n", "U+2028": "\u2028", "U+2029": "\u2029", NEL: "\u0085", tab: "\t", space: " ", NBSP: "\u00a0" };
    const cases = Object.entries(terminators).flatMap(([name, t]) => [
      [`an id ending in ${name}`, () => [item("5" + t)]],
      [`an id starting with ${name}`, () => [item(t + "5")]],
      [`a negative id ending in ${name}`, () => [item("-5" + t)]],
      [`a hash ending in ${name}`, () => [item(1, "a".repeat(64) + t)]],
      [`a hash starting with ${name}`, () => [item(1, t + "a".repeat(64))]],
      [`a 64-character hash whose last character is ${name}`, () => [item(1, "a".repeat(63) + t.slice(0, 1))]],
    ] as Array<[string, () => ErasureReasonItem[]]>);

    it.each(cases)("refuses %s, and the store is not touched", (_name, page) => {
      const store = new ReasonReviewStore();
      store.appendPage(items(1, 3));
      store.choose(0, KEEP);
      const before = { version: store.getVersion(), total: store.total, ops: store.operations, snap: store.getSnapshot() };
      expect(() => store.appendPage(page())).toThrow(ReasonStoreError);
      expect(store.getVersion()).toBe(before.version);
      expect(store.total).toBe(before.total);
      expect(store.operations).toBe(before.ops);
      expect(store.getSnapshot()).toBe(before.snap);
      expect(store.ordinalOf("5")).toBe(-1);
      expect(store.slice(0, 10).map((i) => i.requestId)).toEqual(["1", "2"]);
    });

    it("refuses an id that is not a string, and the other spellings BigInt would read", () => {
      const store = new ReasonReviewStore();
      for (const id of [5, null, undefined, "0x10", "1e3", "", "+1", "01", "-0", "1_000"]) {
        expect(() => store.appendPage([{ ...item(1), requestId: id as unknown as string }])).toThrow(ReasonStoreError);
      }
      expect(store.total).toBe(0);
      expect(store.getVersion()).toBe(0);
    });

    it("refuses a hash that is 63 or 65 characters, upper-case, or not a string", () => {
      const store = new ReasonReviewStore();
      for (const h of ["a".repeat(63), "a".repeat(65), "A".repeat(64), "a".repeat(63) + "g", 7, null]) {
        expect(() => store.appendPage([{ ...item(1), reviewHash: h as unknown as string }])).toThrow(ReasonStoreError);
      }
      expect(store.total).toBe(0);
    });

    it("still takes the exact forms: the int64 extremes, zero, and a 64-character lower-case hash", () => {
      const store = new ReasonReviewStore();
      const h = "a".repeat(64); // the helper's default hash is only for small positive ids
      store.appendPage([item("-9223372036854775808", h), item("-1", h), item("0", h), item("9223372036854775807", h)]);
      expect(store.total).toBe(4);
    });
  });

  it("fails closed on an id that repeats an earlier page or does not continue it", () => {
    const store = new ReasonReviewStore();
    store.appendPage(items(1, 4));
    expect(() => store.appendPage([item(3)])).toThrow(ReasonStoreError);
    expect(() => store.appendPage([item(2), item(9)])).toThrow(ReasonStoreError);
    expect(() => store.appendPage([item(4)])).not.toThrow();
  });

  it("choose keeps the counters equal to a full recount after 10,000 random operations", () => {
    const store = new ReasonReviewStore();
    const n = 3000;
    for (let from = 1; from <= n; from += 100) store.appendPage(items(from, Math.min(from + 100, n + 1)));
    const model = new Array<number>(n).fill(0);
    let seed = 12345;
    const rand = () => (seed = (seed * 1103515245 + 12345) & 0x7fffffff);
    for (let i = 0; i < 10_000; i++) {
      const at = rand() % n;
      const c = rand() % 2 === 0 ? KEEP : REMOVE;
      store.choose(at, c);
      model[at] = c;
    }
    const snap = store.getSnapshot();
    expect(snap.keep).toBe(model.filter((c) => c === KEEP).length);
    expect(snap.remove).toBe(model.filter((c) => c === REMOVE).length);
    expect(snap.unchosen).toBe(model.filter((c) => c === 0).length);
    expect(snap.total).toBe(n);
    for (let i = 0; i < n; i += 97) expect(store.choiceAt(i)).toBe(model[i]);
  });

  it("gives useSyncExternalStore one snapshot object until something changes, and tells subscribers", () => {
    const store = new ReasonReviewStore();
    store.appendPage(items(1, 3));
    const a = store.getSnapshot();
    expect(store.getSnapshot()).toBe(a);
    let calls = 0;
    const off = store.subscribe(() => calls++);
    store.choose(0, KEEP);
    expect(calls).toBe(1);
    expect(store.getSnapshot()).not.toBe(a);
    store.choose(0, KEEP); // no change, no notice
    expect(calls).toBe(1);
    off();
    store.choose(1, REMOVE);
    expect(calls).toBe(1);
  });

  it("finds the next reason without a choice, wrapping once", () => {
    const store = new ReasonReviewStore();
    store.appendPage(items(1, 6));
    [0, 1, 3].forEach((i) => store.choose(i, KEEP));
    expect(store.nextUnchosen(-1)).toBe(2);
    expect(store.nextUnchosen(2)).toBe(4);
    expect(store.nextUnchosen(4)).toBe(2); // wrapped
    store.choose(2, REMOVE);
    store.choose(4, REMOVE);
    expect(store.nextUnchosen(0)).toBe(-1);
    expect(store.getSnapshot().unchosen).toBe(0);
  });

  it("freezes the review for a press without copying the ids or hashes", () => {
    const store = new ReasonReviewStore();
    store.appendPage(items(1, 5));
    store.choose(0, KEEP);
    const f = store.freeze();
    store.choose(1, REMOVE);
    expect(f.count).toBe(4);
    expect(f.hashes.length).toBe(4 * 32);
    expect(f.choices[1]).toBe(0); // a copy: the later choice is not in it
    expect(store.getVersion()).not.toBe(f.version);
  });

  describe("operation counts", () => {
    const sized = (n: number) => {
      const store = new ReasonReviewStore();
      for (let from = 1; from <= n; from += 100) store.appendPage(items(from, Math.min(from + 100, n + 1)));
      return store;
    };

    it("choose takes one step, whatever the length of the review", () => {
      for (const n of [1_000, 100_000]) {
        const store = sized(n);
        const before = store.operations;
        for (let i = 0; i < 1000; i++) store.choose((i * 7919) % n, i % 2 ? KEEP : REMOVE);
        expect(store.operations - before).toBe(1000);
      }
    });

    it("appending a page takes one step per item in the page, not per item in the store", () => {
      for (const n of [100, 1_000, 100_000]) {
        const store = sized(n);
        const before = store.operations;
        store.appendPage(items(n + 1, n + 101));
        expect(store.operations - before).toBe(100);
      }
    });

    it("reading a display page takes one step per item shown", () => {
      const store = sized(100_000);
      const before = store.operations;
      expect(store.slice(60_000, 60_050)).toHaveLength(50);
      expect(store.operations - before).toBe(50);
    });

    it("a search for the next reason without a choice costs only the distance it walks", () => {
      const store = sized(100_000);
      for (let i = 0; i < 99_990; i++) store.choose(i, KEEP);
      const before = store.operations;
      expect(store.nextUnchosen(99_980)).toBe(99_990);
      expect(store.operations - before).toBe(10);
      expect(store.nextUnchosen(-1)).toBe(99_990); // wraps to the start, then walks the chosen run
    });

    it("only a reload walks every reason, once", () => {
      const store = sized(10_000);
      const before = store.operations;
      store.carry();
      expect(store.operations - before).toBe(10_000);
    });
  });

  describe("a reload", () => {
    it("keeps exactly the (id, hash) matches at 100,000 reasons and marks new and changed ones", () => {
      const store = new ReasonReviewStore();
      const n = 100_000;
      for (let from = 1; from <= n; from += 100) store.appendPage(items(from, Math.min(from + 100, n + 1)));
      for (let i = 0; i < n; i += 2) store.choose(i, KEEP);
      for (let i = 1; i < n; i += 2) store.choose(i, REMOVE);
      const prior = store.carry();
      expect(prior.size).toBe(n);
      store.reset(prior);
      // id 5's hash changed; ids 1.. are the same except that 7 has vanished and 100001 is new
      const next: ErasureReasonItem[] = [];
      for (let id = 1; id <= n + 1; id++) {
        if (id === 7) continue;
        next.push(id === 5 ? item(id, "e".repeat(64)) : item(id));
      }
      for (let at = 0; at < next.length; at += 100) store.appendPage(next.slice(at, at + 100));
      const snap = store.getSnapshot();
      expect(snap.total).toBe(n);
      expect(snap.changedOpen).toBe(2); // id 5 and id 100001
      expect(snap.unchosen).toBe(2);
      expect(store.choiceAt(store.ordinalOf("1"))).toBe(KEEP);
      expect(store.choiceAt(store.ordinalOf("2"))).toBe(REMOVE);
      expect(store.choiceAt(store.ordinalOf("5"))).toBe(0);
      expect(store.isChanged(store.ordinalOf("5"))).toBe(true);
      expect(store.isChanged(store.ordinalOf("100001"))).toBe(true);
      expect(store.ordinalOf("7")).toBe(-1);
      expect(store.nextChanged(-1)).toBe(store.ordinalOf("5"));
      store.choose(store.ordinalOf("5"), KEEP);
      expect(store.getSnapshot().changedOpen).toBe(1);
      expect(store.isChanged(store.ordinalOf("5"))).toBe(false);
    });
  });
});

// Seeded random operations against a small reference model. A failure names the
// seed and prints the operation trace, so FOLD_REASON_SEED=<seed> replays it.
// FOLD_REASON_SEED_BASE and FOLD_REASON_RUNS choose the range
// (scripts/reason-review-hardening.sh sets them).
function mulberry32(seed: number) {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

function seedsFromEnv(): number[] {
  const one = process.env.FOLD_REASON_SEED;
  if (one) return [Number(one)];
  const base = Number(process.env.FOLD_REASON_SEED_BASE ?? 20261111);
  const runs = Number(process.env.FOLD_REASON_RUNS ?? 24);
  return Array.from({ length: runs }, (_, i) => base + i);
}

type ModelItem = { id: bigint; hash: string; choice: 0 | 1 | 2; changed: boolean; item: ErasureReasonItem };

// The model keeps the rules in the plainest form: a list, a walk position and a
// version that moves only when the state does.
class Model {
  items: ModelItem[] = [];
  last: bigint | null = null;
  prior: Map<string, { hash: string; choice: 1 | 2 }> | null = null;
  version = 0;

  // The refusals, written independently of the store: a canonical signed
  // decimal inside int64, ascending across the walk, a new id, a 64-character
  // lower-case hash.
  accepts(page: ErasureReasonItem[]): boolean {
    let prev = this.last;
    const seen = new Set(this.items.map((m) => m.item.requestId));
    for (const it of page) {
      if (typeof it.requestId !== "string") return false;
      let v: bigint;
      try {
        v = BigInt(it.requestId);
      } catch {
        return false;
      }
      // The round trip rejects what BigInt reads but the walk never writes: a
      // trailing line terminator or space, "0x10", "+1", "01" and "-0".
      if (v.toString() !== it.requestId) return false;
      if (v < -(2n ** 63n) || v > 2n ** 63n - 1n) return false;
      if (prev !== null && v <= prev) return false;
      if (seen.has(it.requestId)) return false;
      // The length is checked, not implied by the pattern's end: "$" matches
      // before a final newline, so a longer hash with one would pass the
      // pattern alone.
      if (typeof it.reviewHash !== "string" || it.reviewHash.length !== 64 || !/^[0-9a-f]{64}$/.test(it.reviewHash)) return false;
      seen.add(it.requestId);
      prev = v;
    }
    return true;
  }

  append(page: ErasureReasonItem[]): void {
    if (page.length === 0) return;
    for (const it of page) {
      const old = this.prior?.get(it.requestId);
      const kept = old && old.hash === it.reviewHash ? old.choice : 0;
      this.items.push({ id: BigInt(it.requestId), hash: it.reviewHash, choice: kept, changed: this.prior !== null && !kept, item: it });
      this.last = BigInt(it.requestId);
    }
    this.version++;
  }

  choose(at: number, c: 1 | 2): void {
    const m = this.items[at];
    if (!m || m.choice === c) return;
    m.choice = c;
    m.changed = false;
    this.version++;
  }

  reset(prior: Model["prior"]): void {
    this.items = [];
    this.last = null;
    this.prior = prior;
    this.version++;
  }

  carry(): Map<string, { hash: string; choice: 1 | 2 }> {
    return new Map(this.items.filter((m) => m.choice !== 0).map((m) => [m.item.requestId, { hash: m.hash, choice: m.choice as 1 | 2 }]));
  }

  next(from: number, want: (m: ModelItem) => boolean): number {
    const n = this.items.length;
    for (let step = 1; step <= n; step++) {
      const i = (from + step + n) % n;
      if (want(this.items[i])) return i;
    }
    return -1;
  }
}

// Plain comparisons per item (a few hundred items, a few hundred steps, many
// seeds): expect() only for the aggregates.
function same(got: unknown, want: unknown, what: string) {
  if (got !== want) throw new Error(`${what} is ${String(got)}, want ${String(want)}`);
}

function compare(store: ReasonReviewStore, model: Model, rng: () => number, withHashes: boolean) {
  const n = model.items.length;
  same(store.total, n, "total");
  same(store.getVersion(), model.version, "version");
  expect(store.getSnapshot(), "snapshot").toMatchObject({
    total: n,
    keep: model.items.filter((m) => m.choice === KEEP).length,
    remove: model.items.filter((m) => m.choice === REMOVE).length,
    unchosen: model.items.filter((m) => m.choice === 0).length,
    changedOpen: model.items.filter((m) => m.changed).length,
  });
  model.items.forEach((m, i) => {
    same(store.choiceAt(i), m.choice, `choice ${i}`);
    same(store.isChanged(i), m.changed, `changed ${i}`);
    same(store.itemAt(i), m.item, `item ${i}`);
    same(store.ordinalOf(m.item.requestId), i, `ordinal of ${m.item.requestId}`);
  });
  same(store.itemAt(n), undefined, "the item past the end");
  same(store.ordinalOf("not an id"), -1, "ordinal of an unknown id");
  const from = Math.floor(rng() * (n + 1)) - 1;
  same(store.nextUnchosen(from), model.next(from, (m) => m.choice === 0), `nextUnchosen(${from})`);
  same(store.nextChanged(from), model.next(from, (m) => m.changed && m.choice === 0), `nextChanged(${from})`);
  const a = Math.floor(rng() * (n + 2)) - 1;
  const b = a + Math.floor(rng() * 6);
  expect(store.slice(a, b), `slice(${a}, ${b})`).toEqual(model.items.slice(Math.max(0, a), Math.max(0, b)).map((m) => m.item));
  const f = store.freeze();
  same(f.count, n, "frozen count");
  same(f.version, model.version, "frozen version");
  same(f.hashes.length, n * 32, "frozen hash bytes");
  expect([...f.choices]).toEqual(model.items.map((m) => m.choice));
  if (withHashes) {
    model.items.forEach((m, i) => {
      same(f.ids[i], m.item.requestId, `frozen id ${i}`);
      same(Array.from(f.hashes.subarray(i * 32, i * 32 + 32), (x) => x.toString(16).padStart(2, "0")).join(""), m.hash, `hash ${i}`);
    });
  }
}

describe("the reference model", () => {
  it("refuses the same newline-suffixed ids and hashes production refuses", () => {
    const model = new Model();
    for (const id of ["5\n", "5\r", "5\r\n", "-5\n", "5\u00a0", "5 ", "0x10", "+1", "01", "-0", ""]) {
      expect(model.accepts([{ ...item(0), requestId: id }]), JSON.stringify(id)).toBe(false);
    }
    for (const hash of ["a".repeat(64) + "\n", "\n" + "a".repeat(64), "a".repeat(63) + "\n", "A".repeat(64)]) {
      expect(model.accepts([{ ...item(1), reviewHash: hash }]), JSON.stringify(hash)).toBe(false);
    }
    expect(model.accepts([{ ...item(1) }])).toBe(true);
    expect(model.accepts([])).toBe(true);
  });
});

// FOLD_REASON_EVIDENCE names a file that scripts/reason-review-hardening.sh counts
// to see that every seed it asked for really ran.
function record(line: string) {
  if (process.env.FOLD_REASON_EVIDENCE) appendFileSync(process.env.FOLD_REASON_EVIDENCE, line.replace(/\n/g, "\\n") + "\n");
}

describe("random operations against a reference model", () => {
  it.each(seedsFromEnv())("seed %i: the store and the model agree after every operation", (seed) => {
    const rng = mulberry32(seed);
    const below = (n: number) => Math.floor(rng() * n);
    const trace: string[] = [];
    const store = new ReasonReviewStore();
    const model = new Model();
    const hashOf = () => Array.from({ length: 32 }, () => below(256).toString(16).padStart(2, "0")).join("");
    const itemOf = (id: bigint, hash = hashOf()): ErasureReasonItem => ({ ...item(0), requestId: String(id), reviewHash: hash });
    // A page that continues the walk: ascending ids with gaps, now and then at an int64 extreme.
    const goodPage = (): ErasureReasonItem[] => {
      const size = 1 + below(6);
      let id = model.last === null ? (rng() < 0.3 ? -(2n ** 63n) + BigInt(below(5)) : BigInt(below(2000) - 1000)) : model.last + 1n + BigInt(below(4));
      const page: ErasureReasonItem[] = [];
      for (let i = 0; i < size; i++) {
        if (id > 2n ** 63n - 1n) break;
        page.push(itemOf(id));
        id += 1n + BigInt(below(3));
      }
      return page;
    };
    const badPage = (): ErasureReasonItem[] => {
      const page = goodPage();
      const at = below(page.length || 1);
      const kind = below(8);
      if (page.length === 0) return [itemOf(5n), itemOf(5n)];
      const mutate = (patch: Partial<ErasureReasonItem>) => (page[at] = { ...page[at], ...patch });
      if (kind === 0) mutate({ requestId: "+" + page[at].requestId });
      else if (kind === 1) mutate({ requestId: page[at].requestId + "\n" });
      else if (kind === 2) mutate({ reviewHash: page[at].reviewHash.slice(0, 63) + "G" });
      else if (kind === 3) mutate({ reviewHash: page[at].reviewHash.slice(1) });
      else if (kind === 4) mutate({ requestId: String(2n ** 63n) });
      else if (kind === 5 && model.items.length > 0) page.unshift(model.items[below(model.items.length)].item); // a repeat
      else if (kind === 6 && model.last !== null) page.unshift(itemOf(model.last - BigInt(below(3)))); // does not continue
      else page.push(page[at]); // twice on one page
      return page;
    };
    const step = (label: string, run: () => void) => {
      trace.push(label);
      run();
    };

    try {
      for (let op = 0; op < 260; op++) {
        const roll = below(100);
        if (roll < 28) {
          const page = goodPage();
          step(`append good [${page.map((p) => p.requestId).join(",")}]`, () => {
            expect(model.accepts(page)).toBe(true);
            store.appendPage(page);
            model.append(page);
          });
        } else if (roll < 38) {
          const page = badPage();
          step(`append bad [${page.map((p) => `${JSON.stringify(p.requestId)}:${p.reviewHash.length}`).join(",")}]`, () => {
            if (model.accepts(page)) {
              store.appendPage(page);
              model.append(page);
            } else {
              expect(() => store.appendPage(page)).toThrow(ReasonStoreError);
            }
          });
        } else if (roll < 74) {
          const at = below(model.items.length + 3) - 1;
          const c = rng() < 0.5 ? KEEP : REMOVE;
          step(`choose ${at} ${c === KEEP ? "keep" : "remove"}`, () => {
            store.choose(at, c);
            model.choose(at, c);
          });
        } else if (roll < 80) {
          step(`append empty page`, () => {
            store.appendPage([]);
            model.append([]);
          });
        } else if (roll < 86) {
          // A reload: carry the choices, start over and replay the walk with some hashes changed, some ids gone, some new.
          const before = model.items.slice();
          const prior = rng() < 0.85 ? model.carry() : null;
          step(`reload ${prior ? `with ${prior.size} carried` : "with nothing carried"} (${before.length} before)`, () => {
            expect(store.carry()).toEqual(model.carry());
            store.reset(prior ? new Map(prior) : null);
            model.reset(prior ? new Map(prior) : null);
            const walk: ModelItem[] = [];
            for (const m of before) if (rng() < 0.8) walk.push({ ...m, hash: rng() < 0.2 ? hashOf() : m.hash });
            for (let k = below(4); k > 0; k--) walk.push({ id: BigInt(below(4000) - 2000), hash: hashOf(), choice: 0, changed: false, item: item(0) });
            const sorted = [...new Map(walk.map((m) => [m.id, m] as const)).values()].sort((x, y) => (x.id < y.id ? -1 : x.id > y.id ? 1 : 0));
            for (let at = 0; at < sorted.length; ) {
              const size = 1 + below(5);
              const page = sorted.slice(at, at + size).map((m) => itemOf(m.id, m.hash));
              store.appendPage(page);
              model.append(page);
              at += size;
            }
          });
        } else if (roll < 90) {
          step(`reset with nothing carried`, () => {
            store.reset();
            model.reset(null);
          });
        } else {
          step(`read`, () => undefined);
        }
        compare(store, model, rng, op % 11 === 0 || op === 259);
      }
      record(`store seed ${seed} ok: ${trace.length} operations`);
    } catch (e) {
      const shown = trace.length > 120 ? [`... ${trace.length - 120} earlier operations ...`, ...trace.slice(-120)] : trace;
      const report = `reproduce with FOLD_REASON_SEED=${seed}; ${trace.length} operations, the last failing:\n${shown.join("\n")}\n${(e as Error).message}`;
      record(`store seed ${seed} FAILED: ${report}`);
      throw new Error(report);
    }
  }, 60_000);
});
