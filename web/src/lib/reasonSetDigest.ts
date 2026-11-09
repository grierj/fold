// The set digest the DELETE carries for the contact-request reason review
// (M7.4 T07B-2, F2). It must equal what the server streams:
//
//   SHA-256( "fold:erasure-reason-set:v1" || 0x00 || uint64 BE count
//            || for each item in ascending signed id order:
//                 int64 BE id || the raw 32-byte review hash )
//
// plus the two-bit choice vector (item i: bits 2*(i mod 4) of byte i/4, least
// significant first; 1 keep, 2 remove). reasonSetVectors.json holds shared
// vectors; the Go suite reads the same file.
//
// SHA-256 comes from crypto.subtle when it exists (it does not outside secure
// contexts, such as plain http on a LAN address) and passes a known-answer
// self-test; otherwise from the bundled incremental implementation below, which
// passes the same self-test. If neither does, nothing is sent: there is no path
// without a digest and no silent downgrade.

const PREFIX = "fold:erasure-reason-set:v1";
const HEADER = PREFIX.length + 1 + 8;
const ITEM = 8 + 32;

/** Above this many preimage bytes the digest is streamed in chunks (a memory choice, not a cap). */
/** @public test seam for the digest vectors. */
export const WEBCRYPTO_PREIMAGE_LIMIT = 16 * 1024 * 1024;
const CHUNK_BYTES = 1024 * 1024;
/** Whole records in one chunk: a record is never split, so a chunk is a little under CHUNK_BYTES (1 MiB / 40 is 26,214.4). */
/** @public test seam for the digest vectors. */
export const ITEMS_PER_CHUNK = Math.floor(CHUNK_BYTES / ITEM);

export class DigestUnavailableError extends Error {
  constructor() {
    super("no SHA-256 implementation passed its self-test");
    this.name = "DigestUnavailableError";
  }
}

// --- the preimage -----------------------------------------------------------

/**
 * The preimage in pieces: the 35-byte header, then runs of at most itemsPerChunk
 * whole 40-byte records (a whole number, never a fraction of one). Their
 * concatenation is digestPreimage.
 */
/** @public test seam for the digest vectors. */
export function* preimageChunks(ids: readonly string[], hashes: Uint8Array, count: number, itemsPerChunk = ITEMS_PER_CHUNK): Generator<Uint8Array<ArrayBuffer>> {
  const per = Math.max(1, Math.floor(itemsPerChunk));
  const head = new Uint8Array(HEADER);
  for (let i = 0; i < PREFIX.length; i++) head[i] = PREFIX.charCodeAt(i);
  new DataView(head.buffer).setBigUint64(PREFIX.length + 1, BigInt(count));
  yield head;
  for (let start = 0; start < count; start += per) {
    const end = Math.min(start + per, count);
    const out = new Uint8Array((end - start) * ITEM);
    const view = new DataView(out.buffer);
    for (let i = start; i < end; i++) {
      const o = (i - start) * ITEM;
      view.setBigInt64(o, BigInt(ids[i]));
      out.set(hashes.subarray(i * 32, i * 32 + 32), o + 8);
    }
    yield out;
  }
}

/** The whole preimage in one array (35 + 40n bytes). */
/** @public test seam for the digest vectors. */
export function digestPreimage(ids: readonly string[], hashes: Uint8Array, count: number): Uint8Array<ArrayBuffer> {
  const out = new Uint8Array(HEADER + count * ITEM);
  let at = 0;
  for (const part of preimageChunks(ids, hashes, count)) {
    out.set(part, at);
    at += part.length;
  }
  return out;
}

// --- a bundled SHA-256 -------------------------------------------------------

const K = new Uint32Array([
  0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5, 0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74,
  0x80deb1fe, 0x9bdc06a7, 0xc19bf174, 0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da, 0x983e5152, 0xa831c66d,
  0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967, 0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e,
  0x92722c85, 0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070, 0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5,
  0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3, 0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
]);

const rotr = (x: number, n: number) => (x >>> n) | (x << (32 - n));

/** An incremental SHA-256 (FIPS 180-4). Only update() and digest() are public. */
/** @public test seam for the digest vectors. */
export class Sha256 {
  private readonly h = new Uint32Array([0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19]);
  private readonly block = new Uint8Array(64);
  private readonly w = new Uint32Array(64);
  private filled = 0;
  private total = 0;

  update(data: Uint8Array): this {
    this.total += data.length;
    let at = 0;
    if (this.filled > 0) {
      const take = Math.min(64 - this.filled, data.length);
      this.block.set(data.subarray(0, take), this.filled);
      this.filled += take;
      at = take;
      if (this.filled < 64) return this;
      this.compress(this.block, 0);
      this.filled = 0;
    }
    for (; at + 64 <= data.length; at += 64) this.compress(data, at);
    if (at < data.length) {
      this.block.set(data.subarray(at), 0);
      this.filled = data.length - at;
    }
    return this;
  }

  digest(): Uint8Array<ArrayBuffer> {
    const bits = this.total * 8;
    const pad = new Uint8Array(this.filled < 56 ? 64 - this.filled : 128 - this.filled);
    pad[0] = 0x80;
    const view = new DataView(pad.buffer);
    view.setUint32(pad.length - 8, Math.floor(bits / 0x100000000));
    view.setUint32(pad.length - 4, bits >>> 0);
    const total = this.total;
    this.update(pad);
    this.total = total;
    const out = new Uint8Array(32);
    const o = new DataView(out.buffer);
    for (let i = 0; i < 8; i++) o.setUint32(i * 4, this.h[i]);
    return out;
  }

  private compress(b: Uint8Array, o: number): void {
    const w = this.w;
    const h = this.h;
    for (let i = 0; i < 16; i++) {
      const j = o + i * 4;
      w[i] = (b[j] << 24) | (b[j + 1] << 16) | (b[j + 2] << 8) | b[j + 3];
    }
    for (let i = 16; i < 64; i++) {
      const a = w[i - 15];
      const c = w[i - 2];
      w[i] = w[i - 16] + (rotr(a, 7) ^ rotr(a, 18) ^ (a >>> 3)) + w[i - 7] + (rotr(c, 17) ^ rotr(c, 19) ^ (c >>> 10));
    }
    let a = h[0], b1 = h[1], c = h[2], d = h[3], e = h[4], f = h[5], g = h[6], hh = h[7];
    for (let i = 0; i < 64; i++) {
      const t1 = (hh + (rotr(e, 6) ^ rotr(e, 11) ^ rotr(e, 25)) + ((e & f) ^ (~e & g)) + K[i] + w[i]) >>> 0;
      const t2 = ((rotr(a, 2) ^ rotr(a, 13) ^ rotr(a, 22)) + ((a & b1) ^ (a & c) ^ (b1 & c))) >>> 0;
      hh = g;
      g = f;
      f = e;
      e = (d + t1) >>> 0;
      d = c;
      c = b1;
      b1 = a;
      a = (t1 + t2) >>> 0;
    }
    h[0] += a;
    h[1] += b1;
    h[2] += c;
    h[3] += d;
    h[4] += e;
    h[5] += f;
    h[6] += g;
    h[7] += hh;
  }
}

// --- choosing an implementation ---------------------------------------------

const toHex = (b: Uint8Array) => Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
const bytesOf = (s: string) => Uint8Array.from(s, (ch) => ch.charCodeAt(0));

// Known answers: the empty input, "abc", and a two-block message.
const KNOWN: ReadonlyArray<readonly [string, string]> = [
  ["", "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"],
  ["abc", "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"],
  ["abcdbcdecdefdefgefghfghighijhijkijkljklmklmnlmnomnopnopq", "248d6a61d20638b8e5c026930c3e6039a33ce45964ff2167f6ecedd419db06c1"],
];

/** @public test seam: which digest path ran. */
export type DigestPath = "webcrypto" | "fallback";

type Subtle = { digest(algorithm: string, data: Uint8Array<ArrayBuffer>): Promise<ArrayBuffer> };

function subtleOf(): Subtle | null {
  const s = (globalThis as { crypto?: { subtle?: Partial<Subtle> } }).crypto?.subtle;
  return s && typeof s.digest === "function" ? (s as Subtle) : null;
}

function fallbackSum(bytes: Uint8Array): string {
  return toHex(new Sha256().update(bytes).digest());
}

async function selfTestSubtle(subtle: Subtle): Promise<boolean> {
  try {
    for (const [msg, want] of KNOWN) if (toHex(new Uint8Array(await subtle.digest("SHA-256", bytesOf(msg)))) !== want) return false;
    return true;
  } catch {
    return false;
  }
}

const selfTestFallback = () => KNOWN.every(([msg, want]) => fallbackSum(bytesOf(msg)) === want);

let verdict: Promise<{ subtle: Subtle | null; fallback: boolean }> | null = null;
let lastPath: DigestPath | null = null;

/** Which implementation produced the last digest; the QC harness records it. */
/** @public test seam: which digest path ran. */
export const lastDigestPath = (): DigestPath | null => lastPath;

function chosen() {
  verdict ??= (async () => {
    const subtle = subtleOf();
    return { subtle: subtle && (await selfTestSubtle(subtle)) ? subtle : null, fallback: selfTestFallback() };
  })();
  return verdict;
}

/** Test seam: forget the cached self-test (after a test swaps crypto.subtle). */
/** @public test seam: forgets the chosen digest path. */
export function resetDigestChoice(): void {
  verdict = null;
  lastPath = null;
}

const tick = () => new Promise<void>((resolve) => setTimeout(resolve, 0));

/**
 * The set digest as 64 lowercase hex. Throws DigestUnavailableError when no
 * implementation passes its self-test (permanent for this page); any other
 * exception is transient.
 */
/** @public test seam for the digest vectors. */
export async function setDigestHex(ids: readonly string[], hashes: Uint8Array, count: number): Promise<string> {
  const { subtle, fallback } = await chosen();
  if (subtle && HEADER + count * ITEM <= WEBCRYPTO_PREIMAGE_LIMIT) {
    const out = new Uint8Array(await subtle.digest("SHA-256", digestPreimage(ids, hashes, count)));
    lastPath = "webcrypto";
    return toHex(out);
  }
  if (!fallback) throw new DigestUnavailableError();
  const sha = new Sha256();
  for (const part of preimageChunks(ids, hashes, count)) {
    sha.update(part);
    // Yield after every full chunk (just under a megabyte of records) so the page stays responsive.
    if (part.length >= ITEMS_PER_CHUNK * ITEM) await tick();
  }
  lastPath = "fallback";
  return toHex(sha.digest());
}

// --- the choice vector -------------------------------------------------------

/** Packs two-bit codes (1 keep, 2 remove) for count items as standard padded base64. */
/** @public test seam for the digest vectors. */
export function encodeChoices(codes: Uint8Array, count: number): string {
  const packed = new Uint8Array(Math.ceil(count / 4));
  for (let i = 0; i < count; i++) packed[i >> 2] |= codes[i] << ((i & 3) * 2);
  let binary = "";
  for (let i = 0; i < packed.length; i += 0x8000) binary += String.fromCharCode(...packed.subarray(i, i + 0x8000));
  return btoa(binary);
}

/** The DELETE body's reasonReview (set-v1). */
export interface ReasonReviewBody {
  format: "set-v1";
  count: string;
  digest: string;
  choices: string;
}

export async function reasonReviewBody(ids: readonly string[], hashes: Uint8Array, codes: Uint8Array, count: number): Promise<ReasonReviewBody> {
  return { format: "set-v1", count: String(count), digest: await setDigestHex(ids, hashes, count), choices: encodeChoices(codes, count) };
}
