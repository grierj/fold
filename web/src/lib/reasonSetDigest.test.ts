import { createHash, randomBytes } from "node:crypto";
import { appendFileSync } from "node:fs";
import { afterEach, describe, expect, it, vi } from "vitest";
import vectors from "./reasonSetVectors.json";
import {
  DigestUnavailableError,
  ITEMS_PER_CHUNK,
  Sha256,
  WEBCRYPTO_PREIMAGE_LIMIT,
  digestPreimage,
  encodeChoices,
  lastDigestPath,
  preimageChunks,
  reasonReviewBody,
  resetDigestChoice,
  setDigestHex,
} from "./reasonSetDigest";

// The vectors are shared with the Go suite (account_erasure_reasons_envelope_test.go).
const bytes = (hex: string) => Uint8Array.from(hex.match(/../g) ?? [], (h) => parseInt(h, 16));
const hexOf = (b: Uint8Array) => Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
const node = (b: Uint8Array) => createHash("sha256").update(b).digest("hex");

type Vector = (typeof vectors.vectors)[number];
const hashesOf = (v: Vector) => Uint8Array.from(v.hashes.flatMap((h) => [...bytes(h)]));
const codes = (v: Vector) => Uint8Array.from(v.choices);

// A stand-in for crypto.subtle backed by Node (jsdom's own subtle rejects its realm's arrays).
const nodeSubtle = {
  digest: async (_algorithm: string, data: Uint8Array) => {
    const out = createHash("sha256").update(data).digest();
    return out.buffer.slice(out.byteOffset, out.byteOffset + out.byteLength);
  },
};

afterEach(() => {
  vi.unstubAllGlobals();
  resetDigestChoice();
});

describe("the shared vectors", () => {
  it.each(vectors.vectors)("$name: the preimage and digest equal the Go side", async (v) => {
    vi.stubGlobal("crypto", { subtle: nodeSubtle });
    resetDigestChoice();
    const pre = digestPreimage(v.ids, hashesOf(v), v.ids.length);
    expect(pre.length).toBe(35 + 40 * v.ids.length);
    expect(hexOf(pre)).toBe(v.preimageHex);
    expect(await setDigestHex(v.ids, hashesOf(v), v.ids.length)).toBe(v.digest);
    expect(encodeChoices(codes(v), v.ids.length)).toBe(v.choicesBase64);
    expect(lastDigestPath()).toBe("webcrypto");
  });

  it.each(vectors.vectors)("$name: the bundled implementation gives the same digest with crypto.subtle removed", async (v) => {
    vi.stubGlobal("crypto", {});
    resetDigestChoice();
    expect(await setDigestHex(v.ids, hashesOf(v), v.ids.length)).toBe(v.digest);
    expect(lastDigestPath()).toBe("fallback");
  });

  it("writes the extreme signed ids as two's complement", () => {
    const v = vectors.vectors.find((x) => x.name.startsWith("nine items"))!;
    const pre = digestPreimage(v.ids, hashesOf(v), v.ids.length);
    expect(hexOf(pre.subarray(35, 43))).toBe("8000000000000000");
    expect(hexOf(pre.subarray(35 + 40 * 8, 35 + 40 * 8 + 8))).toBe("7fffffffffffffff");
    expect(hexOf(pre.subarray(35 + 40 * 7, 35 + 40 * 7 + 8))).toBe("0020000000000001");
  });
});

describe("the bundled SHA-256", () => {
  it("matches the known answers", () => {
    expect(hexOf(new Sha256().digest())).toBe("e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855");
    expect(hexOf(new Sha256().update(new TextEncoder().encode("abc")).digest())).toBe("ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad");
  });

  it("matches Node's crypto for lengths 0 to 300 and the block boundaries", () => {
    for (let n = 0; n <= 300; n++) {
      const data = randomBytes(n);
      expect(hexOf(new Sha256().update(data).digest())).toBe(node(data));
    }
    for (const n of [55, 56, 63, 64, 65, 119, 120, 127, 128, 129, 1_000_003]) {
      const data = randomBytes(n);
      expect(hexOf(new Sha256().update(data).digest())).toBe(node(data));
    }
  });

  it("gives the same digest however the input is split", () => {
    const data = randomBytes(1000);
    const want = node(data);
    for (const cut of [1, 3, 55, 56, 63, 64, 65, 500, 999]) {
      expect(hexOf(new Sha256().update(data.subarray(0, cut)).update(data.subarray(cut)).digest())).toBe(want);
    }
    const sha = new Sha256();
    for (let i = 0; i < data.length; i += 7) sha.update(data.subarray(i, i + 7));
    expect(hexOf(sha.digest())).toBe(want);
  });
});

describe("choosing an implementation", () => {
  const one = vectors.vectors[0];

  it("falls back when crypto.subtle gives a wrong answer, and still matches", async () => {
    vi.stubGlobal("crypto", { subtle: { digest: async () => new Uint8Array(32).buffer } });
    resetDigestChoice();
    expect(await setDigestHex(one.ids, hashesOf(one), 1)).toBe(one.digest);
    expect(lastDigestPath()).toBe("fallback");
  });

  it("falls back when crypto.subtle throws", async () => {
    vi.stubGlobal("crypto", { subtle: { digest: async () => { throw new Error("no"); } } });
    resetDigestChoice();
    expect(await setDigestHex(one.ids, hashesOf(one), 1)).toBe(one.digest);
    expect(lastDigestPath()).toBe("fallback");
  });

  it("refuses, for good, when no implementation passes its self-test", async () => {
    vi.stubGlobal("crypto", { subtle: { digest: async () => new Uint8Array(32).buffer } });
    resetDigestChoice();
    const spy = vi.spyOn(Sha256.prototype, "digest").mockReturnValue(new Uint8Array(32));
    await expect(setDigestHex(one.ids, hashesOf(one), 1)).rejects.toBeInstanceOf(DigestUnavailableError);
    await expect(reasonReviewBody(one.ids, hashesOf(one), codes(one), 1)).rejects.toBeInstanceOf(DigestUnavailableError);
    spy.mockRestore();
  });

  it("builds the whole body: format, count, digest and choices", async () => {
    const v = vectors.vectors.find((x) => x.name.startsWith("nine items"))!;
    expect(await reasonReviewBody(v.ids, hashesOf(v), codes(v), v.ids.length)).toEqual({ format: "set-v1", count: "9", digest: v.digest, choices: v.choicesBase64 });
  });
});

describe("encodeChoices", () => {
  it("packs item i into bits 2*(i mod 4) of byte i/4, and pads the last byte with zeros", () => {
    expect(encodeChoices(Uint8Array.from([1]), 1)).toBe("AQ==");
    expect(encodeChoices(Uint8Array.from([2, 2, 1, 2]), 4)).toBe(btoa(String.fromCharCode(0b10_01_10_10)));
    expect(encodeChoices(Uint8Array.from([1, 1, 1, 1, 2]), 5)).toBe(btoa(String.fromCharCode(0x55, 2)));
  });
});

// Chunking and the 16 MiB line. A record is 40 bytes, so a 1 MiB chunk holds
// 26,214 whole records (1 MiB / 40 is 26,214.4); the reference below is built
// here, whole, with no chunking, and hashed by Node.

// n ascending ids from -3 (so the walk crosses zero) with pseudo-random hashes.
function fixture(n: number) {
  const ids = Array.from({ length: n }, (_, i) => String(i - 3));
  const hashes = new Uint8Array(n * 32);
  let x = 123456789 + n;
  for (let i = 0; i < hashes.length; i++) {
    x = (Math.imul(x, 1664525) + 1013904223) >>> 0;
    hashes[i] = x >>> 24;
  }
  return { ids, hashes };
}

// The whole preimage, written straight from the wire spec, and its SHA-256 by Node.
function referenceDigest(ids: string[], hashes: Uint8Array): string {
  const n = ids.length;
  const pre = new Uint8Array(35 + 40 * n);
  pre.set(Uint8Array.from("fold:erasure-reason-set:v1", (c) => c.charCodeAt(0)), 0);
  const view = new DataView(pre.buffer);
  view.setBigUint64(27, BigInt(n));
  for (let i = 0; i < n; i++) {
    view.setBigInt64(35 + 40 * i, BigInt(ids[i]));
    pre.set(hashes.subarray(i * 32, i * 32 + 32), 35 + 40 * i + 8);
  }
  return node(pre);
}

describe("chunking", () => {
  it("holds whole records: 26,214 to a chunk, never a fraction of one", () => {
    expect(ITEMS_PER_CHUNK).toBe(26214);
    expect(Number.isInteger(ITEMS_PER_CHUNK)).toBe(true);
  });

  it.each([0, 1, 26213, 26214, 26215, 26216, 52428, 52429, 60000])("%i reasons: the chunks are whole records and join into the whole preimage", (n) => {
    const { ids, hashes } = fixture(n);
    const parts = [...preimageChunks(ids, hashes, n)];
    expect(parts[0].length).toBe(35);
    const sizes = parts.slice(1).map((p) => p.length);
    for (const size of sizes) {
      expect(size % 40).toBe(0);
      expect(size).toBeLessThanOrEqual(ITEMS_PER_CHUNK * 40);
      expect(size).toBeLessThanOrEqual(1024 * 1024);
    }
    expect(sizes.length).toBe(Math.ceil(n / ITEMS_PER_CHUNK));
    expect(sizes.reduce((a, b) => a + b, 0)).toBe(40 * n);
    const joined = new Uint8Array(35 + 40 * n);
    let at = 0;
    for (const p of parts) {
      joined.set(p, at);
      at += p.length;
    }
    expect(hexOf(joined)).toBe(hexOf(digestPreimage(ids, hashes, n)));
    expect(node(joined)).toBe(referenceDigest(ids, hashes));
  });

  it("splits 26,215 reasons as one full chunk and one record, where the old fractional size overran its buffer", () => {
    const { ids, hashes } = fixture(26215);
    expect([...preimageChunks(ids, hashes, 26215)].map((p) => p.length)).toEqual([35, 26214 * 40, 40]);
  });

  it("takes any chunk size, rounding a fraction down to a whole record and never below one", () => {
    const { ids, hashes } = fixture(7);
    expect([...preimageChunks(ids, hashes, 7, 2.9)].map((p) => p.length)).toEqual([35, 80, 80, 80, 40]);
    expect([...preimageChunks(ids, hashes, 7, 0)].map((p) => p.length)).toEqual([35, 40, 40, 40, 40, 40, 40, 40]);
  });
});

describe("the digest across chunk sizes and the 16 MiB line", () => {
  const counts = [26213, 26214, 26215, 26216, 52428, 52429, 60000];

  it.each(counts)("%i reasons: the bundled SHA-256 equals Node's over the whole preimage", async (n) => {
    const { ids, hashes } = fixture(n);
    vi.stubGlobal("crypto", {});
    resetDigestChoice();
    expect(await setDigestHex(ids, hashes, n)).toBe(referenceDigest(ids, hashes));
    expect(lastDigestPath()).toBe("fallback");
  });

  it.each(counts)("%i reasons: WebCrypto (a Node-backed stand-in) agrees too", async (n) => {
    const { ids, hashes } = fixture(n);
    vi.stubGlobal("crypto", { subtle: nodeSubtle });
    resetDigestChoice();
    expect(await setDigestHex(ids, hashes, n)).toBe(referenceDigest(ids, hashes));
    expect(lastDigestPath()).toBe("webcrypto");
  });

  it("yields after each full chunk and not otherwise", async () => {
    const delays = () => spy.mock.calls.filter(([, ms]) => ms === 0).length;
    const spy = vi.spyOn(globalThis, "setTimeout");
    vi.stubGlobal("crypto", {});
    for (const [n, yields] of [[26213, 0], [26214, 1], [26215, 1], [52428, 2], [60000, 2]] as const) {
      const { ids, hashes } = fixture(n);
      resetDigestChoice();
      spy.mockClear();
      await setDigestHex(ids, hashes, n);
      expect(delays(), `${n} reasons`).toBe(yields);
    }
    spy.mockRestore();
  });

  it("uses WebCrypto up to the last count that fits in 16 MiB, the bundled code beyond it, and has no cap", async () => {
    const last = Math.floor((WEBCRYPTO_PREIMAGE_LIMIT - 35) / 40);
    expect(last).toBe(419429);
    expect(35 + 40 * last).toBeLessThanOrEqual(WEBCRYPTO_PREIMAGE_LIMIT);
    expect(35 + 40 * (last + 1)).toBeGreaterThan(WEBCRYPTO_PREIMAGE_LIMIT);
    vi.stubGlobal("crypto", { subtle: nodeSubtle });
    for (const [n, path] of [[last, "webcrypto"], [last + 1, "fallback"], [600_000, "fallback"]] as const) {
      const { ids, hashes } = fixture(n);
      resetDigestChoice();
      expect(await setDigestHex(ids, hashes, n), `${n} reasons`).toBe(referenceDigest(ids, hashes));
      expect(lastDigestPath(), `${n} reasons`).toBe(path);
    }
  });
});

// The generated vectors: the same large reviews the Go suite builds
// (account_erasure_reasons_envelope_test.go, TestReasonSetGeneratedVectors), so
// both sides agree at every chunk and WebCrypto boundary. The file describes
// them (see its generatedNote) and holds digests computed with Node's crypto,
// independent of both implementations.
type Generated = (typeof vectors.generated)[number];

function buildGenerated(g: Generated) {
  const first = BigInt(g.firstId);
  const step = BigInt(g.step);
  const ids = Array.from({ length: g.count }, (_, i) => String(first + BigInt(i) * step));
  const hashes = new Uint8Array(g.count * 32);
  let x = g.hashSeed >>> 0;
  for (let i = 0; i < hashes.length; i++) {
    x = (Math.imul(x, 1664525) + 1013904223) >>> 0;
    hashes[i] = x >>> 24;
  }
  const codes = Uint8Array.from({ length: g.count }, (_, i) => (i % 3 === 1 ? 2 : 1));
  return { ids, hashes, codes };
}

describe("the generated vectors", () => {
  const withinWebCrypto = (n: number) => 35 + 40 * n <= WEBCRYPTO_PREIMAGE_LIMIT;

  it("cover every chunk and WebCrypto boundary, derived from the exported constants", () => {
    const last = Math.floor((WEBCRYPTO_PREIMAGE_LIMIT - 35) / 40);
    const have = new Set(vectors.generated.map((g) => g.count));
    for (const n of [ITEMS_PER_CHUNK - 1, ITEMS_PER_CHUNK, ITEMS_PER_CHUNK + 1, ITEMS_PER_CHUNK + 2, 2 * ITEMS_PER_CHUNK, 2 * ITEMS_PER_CHUNK + 1, last, last + 1]) {
      expect(have.has(n), `a generated vector with ${n} reasons`).toBe(true);
    }
  });

  it.each(vectors.generated)("$name: native and bundled digests and the packed choices equal the shared answers", async (g) => {
    const { ids, hashes, codes } = buildGenerated(g);
    expect(createHash("sha256").update(encodeChoices(codes, g.count)).digest("hex")).toBe(g.choicesSha256);

    vi.stubGlobal("crypto", { subtle: nodeSubtle });
    resetDigestChoice();
    expect(await setDigestHex(ids, hashes, g.count)).toBe(g.digest);
    expect(lastDigestPath()).toBe(withinWebCrypto(g.count) ? "webcrypto" : "fallback");

    vi.stubGlobal("crypto", {});
    resetDigestChoice();
    expect(await setDigestHex(ids, hashes, g.count)).toBe(g.digest);
    expect(lastDigestPath()).toBe("fallback");
  }, 60_000);

  it("keeps the large signed ids past 2^53 distinct and in two's complement", () => {
    const g = vectors.generated[0];
    const { ids, hashes } = buildGenerated(g);
    const pre = digestPreimage(ids.slice(0, 2), hashes, 2);
    expect(hexOf(pre.subarray(35, 43))).toBe("8000000000000000");
    expect(ids[0]).toBe("-9223372036854775808");
    expect(BigInt(ids[1]) - BigInt(ids[0])).toBe(BigInt(g.step));
  });
});

describe("any way of splitting the preimage gives the same digest", () => {
  const sizes = [1, 2, 3, 7, 64, 1000];

  it.each(vectors.vectors)("$name: whole records in chunks of any size", (v) => {
    for (const per of [...sizes, ITEMS_PER_CHUNK, 100_000]) {
      const sha = new Sha256();
      for (const part of preimageChunks(v.ids, hashesOf(v), v.ids.length, per)) sha.update(part);
      expect(hexOf(sha.digest()), `${per} records per chunk`).toBe(v.digest);
    }
  });

  it("26,215 generated reasons: chunks of one record, a chunk less than all, all, and more", () => {
    const g = vectors.generated.find((x) => x.count === ITEMS_PER_CHUNK + 1)!;
    const { ids, hashes } = buildGenerated(g);
    for (const per of [1, 3, ITEMS_PER_CHUNK - 1, ITEMS_PER_CHUNK, ITEMS_PER_CHUNK + 1, 100_000]) {
      const sha = new Sha256();
      for (const part of preimageChunks(ids, hashes, g.count, per)) sha.update(part);
      expect(hexOf(sha.digest()), `${per} records per chunk`).toBe(g.digest);
    }
  }, 60_000);
});

// Seeded random reviews against the reference digest, so a failure can be
// replayed. FOLD_REASON_SEED runs just that seed; FOLD_REASON_SEED_BASE and
// FOLD_REASON_RUNS choose the range (scripts/reason-review-hardening.sh sets them).
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
  const runs = Number(process.env.FOLD_REASON_RUNS ?? 8);
  return Array.from({ length: runs }, (_, i) => base + i);
}

// FOLD_REASON_EVIDENCE names a file that scripts/reason-review-hardening.sh counts
// to see that every seed it asked for really ran.
function record(line: string) {
  if (process.env.FOLD_REASON_EVIDENCE) appendFileSync(process.env.FOLD_REASON_EVIDENCE, line.replace(/\n/g, "\\n") + "\n");
}

describe("random reviews, replayable by seed", () => {
  it.each(seedsFromEnv())("seed %i: both digest paths and every chunking equal the reference", async (seed) => {
    const rng = mulberry32(seed);
    const below = (n: number) => Math.floor(rng() * n);
    const trace: string[] = [];
    try {
      for (let round = 0; round < 4; round++) {
        const n = 1 + (rng() < 0.25 ? below(ITEMS_PER_CHUNK * 2 + 10) : below(300));
        const start = -(2n ** 63n) + BigInt(below(2 ** 50)) * BigInt(below(2 ** 10) + 1);
        const ids: string[] = [];
        let id = start;
        for (let i = 0; i < n; i++) {
          ids.push(String(id));
          id += 1n + BigInt(below(2 ** 40));
        }
        const hashes = Uint8Array.from({ length: n * 32 }, () => below(256));
        const per = 1 + below(n + 2);
        trace.push(`round ${round}: ${n} reasons from ${ids[0]}, ${per} per chunk`);
        const want = referenceDigest(ids, hashes);

        vi.stubGlobal("crypto", {});
        resetDigestChoice();
        expect(await setDigestHex(ids, hashes, n), "the bundled digest").toBe(want);
        vi.stubGlobal("crypto", { subtle: nodeSubtle });
        resetDigestChoice();
        expect(await setDigestHex(ids, hashes, n), "the native digest").toBe(want);
        const sha = new Sha256();
        for (const part of preimageChunks(ids, hashes, n, per)) sha.update(part);
        expect(hexOf(sha.digest()), "the chunked digest").toBe(want);
      }
      record(`digest seed ${seed} ok: ${trace.join(" | ")}`);
    } catch (e) {
      record(`digest seed ${seed} FAILED: ${trace.join(" | ")}; ${(e as Error).message}`);
      throw new Error(`reproduce with FOLD_REASON_SEED=${seed}; trace: ${trace.join(" | ")}; ${(e as Error).message}`);
    }
  }, 60_000);
});
