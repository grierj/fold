// The "set-v1" reason-review DELETE envelope, built the way the browser builds it, for the QC-1 harness and the
// restore crossings (M7.4 T07B-2 scaling F2). An independent implementation of web/src/lib/reasonSetDigest.ts and
// account_erasure_reasons_envelope.go: both are held to the shared vectors (web/src/lib/reasonSetVectors.json) by
// `selftest`, so a disagreement between this helper and the product shows up as a failed self-test, not as a
// server answer nobody can explain.
//
// The wire (one JSON object, compact like JSON.stringify, no trailing newline):
//   {"successors":{...},"hostSuccessors":{...},"attemptId":"<80 hex>","reasonReview":
//      {"format":"set-v1","count":"<decimal>","digest":"<64 hex>","choices":"<base64>"}}
//   digest   SHA-256( "fold:erasure-reason-set:v1" || 0x00 || uint64 BE count || for each item, in ASCENDING SIGNED id order:
//                     int64 BE id || the raw 32-byte review hash )
//   choices  two bits per item, item i at bits 2*(i mod 4) of byte i/4, least significant first; 1 keep, 2 remove;
//            standard base64 with padding of exactly ceil(count/4) bytes
// Nothing is sorted here: an items list that is not strictly ascending is refused, because the server's order is
// what is under test. No reasons (n = 0) means no reasonReview key at all.
//
// Usage (every command prints to stdout; failures exit 1 with a line on stderr):
//   body --items FILE --mode keep|remove|mixed --n N [--attempt ID] [--successors JSON] [--host JSON] [--codes 1,2,...]
//   mutate-last --body FILE --code 0|1|2|3        the last item's choice replaced (3 is no valid choice: a parse refusal)
//   mutate-digest --body FILE                     one digest digit changed (a set that does not match)
//   mutate-count --body FILE                      the claimed count one higher (digest and choices left alone)
//   length --n N [--attempt ID] [--successors JSON] [--host JSON]           bytes of the body for n reasons (exact)
//   capacity --cap BYTES [--attempt ID] [--successors JSON] [--host JSON]   "n bytes_at_n bytes_at_n_plus_1": the most reasons whose body fits
//   oversize --cap BYTES [--attempt ID] ...       a syntactically valid body one reason over the cap (synthetic digest; the server refuses it before reading)
//   legacy-body --entries N [--digits D] [--attempt ID] ...    the OLD items envelope with N synthetic entries of D-digit ids (a comparison only; never applied)
//   legacy-length --entries N [--digits D] ...                 its exact byte length
//   legacy-capacity --cap BYTES [--digits D] ...               "n bytes_at_n bytes_at_n_plus_1" for the old form (the pilots' 52,760 / 52,761 at their id width)
//   digest --items FILE --n N                     the set digest of the first N items
//   agree --items FILE --review FILE              a browser's reasonReview ({format,count,digest,choices} as JSON) against this builder over the items:
//                                                 {checked, equal, ...}; equal only when count, digest and choice vector all match byte for byte
//   dom-ids --panel FILE --ids FILE --page N      the ids the panel's final screen should hold, from the pager line it shows (comma list; nothing when the line does not
//                                                 name a whole page of the reasons the server issued, which dom-text then reports as a disagreement)
//   dom-text --panel FILE --ids FILE --texts FILE --skip FILE --page N
//                                                 the final screen against the database: domTextAgreement (response-accounting.mjs). --ids is the JSON array of every request id
//                                                 in the order the server issued them; --texts is {id: text} from the database; --skip is {id, oldText} or null
//   selftest --vectors FILE                       the shared vectors
import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { pathToFileURL } from "node:url";
import { domTextAgreement, expectedShownIds } from "./response-accounting.mjs";

export const FORMAT = "set-v1";
const PREFIX = "fold:erasure-reason-set:v1";
const INT64_MIN = -(2n ** 63n);
const INT64_MAX = 2n ** 63n - 1n;
const CANON_ID = /^(0|-?[1-9][0-9]*)$/;
const HASH = /^[0-9a-f]{64}$/;

function fail(message) {
  throw new Error(`set-v1: ${message}`);
}

/** Items as the page route issues them: requestId (canonical signed decimal) and reviewHash (64 lowercase hex), strictly ascending by id. */
export function checkItems(items, n = items.length) {
  if (!Array.isArray(items)) fail("items is not an array");
  if (!Number.isInteger(n) || n < 0 || n > items.length) fail(`n ${n} is outside the ${items.length} items`);
  let prev = null;
  for (let i = 0; i < n; i++) {
    const it = items[i];
    if (it === null || typeof it !== "object") fail(`item ${i} is not an object`);
    const { requestId, reviewHash } = it;
    if (typeof requestId !== "string" || !CANON_ID.test(requestId)) fail(`item ${i}: requestId ${JSON.stringify(requestId)} is not a canonical signed decimal`);
    const id = BigInt(requestId);
    if (id < INT64_MIN || id > INT64_MAX) fail(`item ${i}: requestId ${requestId} is outside int64`);
    if (typeof reviewHash !== "string" || !HASH.test(reviewHash)) fail(`item ${i}: reviewHash is not 64 lowercase hex`);
    if (prev !== null && id <= prev) fail(`item ${i}: ids are not strictly ascending (${prev} then ${id}); the server's order is what is measured, so nothing is sorted`);
    prev = id;
  }
}

/** The digest preimage of the first n items (35 + 40n bytes). */
export function preimage(items, n) {
  const out = Buffer.alloc(PREFIX.length + 1 + 8 + 40 * n);
  out.write(PREFIX, 0, "latin1");
  out.writeBigUInt64BE(BigInt(n), PREFIX.length + 1);
  let at = PREFIX.length + 1 + 8;
  for (let i = 0; i < n; i++) {
    out.writeBigInt64BE(BigInt(items[i].requestId), at);
    Buffer.from(items[i].reviewHash, "hex").copy(out, at + 8);
    at += 40;
  }
  return out;
}

export function digestHex(items, n = items.length) {
  checkItems(items, n);
  return createHash("sha256").update(preimage(items, n)).digest("hex");
}

/** Two-bit codes for the first n items. mixed removes the first ceil(n/2), as qc1_expected_split says. */
export function codesFor(mode, n) {
  const codes = new Uint8Array(n);
  for (let i = 0; i < n; i++) {
    if (mode === "keep") codes[i] = 1;
    else if (mode === "remove") codes[i] = 2;
    else if (mode === "mixed") codes[i] = i < Math.ceil(n / 2) ? 2 : 1;
    else fail(`unknown mode ${mode}`);
  }
  return codes;
}

export function encodeChoices(codes, n = codes.length) {
  const packed = Buffer.alloc(Math.ceil(n / 4));
  for (let i = 0; i < n; i++) packed[i >> 2] |= (codes[i] & 3) << ((i & 3) * 2);
  return packed.toString("base64");
}

export function decodeChoices(b64, n) {
  const packed = Buffer.from(b64, "base64");
  if (packed.toString("base64") !== b64 || packed.length !== Math.ceil(n / 4)) fail("choices is not canonical base64 of ceil(count/4) bytes");
  const codes = new Uint8Array(n);
  for (let i = 0; i < n; i++) codes[i] = (packed[i >> 2] >> ((i & 3) * 2)) & 3;
  return codes;
}

/** The compact body JSON.stringify would make, key order successors, hostSuccessors, attemptId, reasonReview. */
export function bodyFrom({ successors = {}, hostSuccessors = {}, attemptId = "", review = null }) {
  const o = { successors, hostSuccessors };
  if (attemptId) o.attemptId = attemptId;
  if (review) o.reasonReview = review;
  return JSON.stringify(o);
}

export function reviewFor(items, n, codes) {
  if (n === 0) return null;
  return { format: FORMAT, count: String(n), digest: digestHex(items, n), choices: encodeChoices(codes, n) };
}

/**
 * A reasonReview the BROWSER sent, against this independent builder over the items the server issued: the builder is given the browser's own choices and the
 * same items, and the count, the digest and the choice vector must come out byte for byte as the browser sent them. checked is false (and equal null, never
 * true) when there is nothing comparable: no set-v1 review, a count that is not a whole number, more reasons than the server issued, or a vector that does not
 * decode. A count short of the items issued is checked and not equal: the browser's set was not the server's. The choices are the browser's own, so the builder reproduces them whatever
 * they are; what it adds is that the vector is canonical, has the right length and holds only 1 (keep) and 2 (remove) for every reason (invalidChoices counts the rest).
 */
export function agree(items, review) {
  const unchecked = (why, extra = {}) => ({ checked: false, equal: null, why, ...extra });
  if (review === null || typeof review !== "object" || review.format !== FORMAT || typeof review.count !== "string" || typeof review.digest !== "string" || typeof review.choices !== "string") {
    return unchecked("the browser sent no set-v1 reasonReview to compare");
  }
  if (!/^(0|[1-9][0-9]*)$/.test(review.count)) return unchecked(`the browser claimed a count that is not a whole number: ${review.count}`);
  const n = Number(review.count);
  if (n === 0) return unchecked("there are no reasons in the review to compare");
  if (n > items.length) return unchecked("the browser claimed more reasons than the server issued", { browserCount: n, issued: items.length });
  checkItems(items, items.length);
  if (n !== items.length) return { checked: true, equal: false, countEqual: false, digestEqual: null, choicesEqual: null, browserCount: n, issued: items.length, why: "the browser's set is not the whole set the server issued" };
  let codes;
  try {
    codes = decodeChoices(review.choices, n);
  } catch (e) {
    return unchecked(`the browser's choice vector did not decode: ${e.message}`);
  }
  const built = reviewFor(items, n, codes);
  const digestEqual = built.digest === review.digest;
  const choicesEqual = built.choices === review.choices;
  // The server accepts only 1 (keep) and 2 (remove) for a reason; a vector that decodes to anything else was not a complete answer.
  let invalidChoices = 0;
  for (let i = 0; i < n; i++) if (codes[i] !== 1 && codes[i] !== 2) invalidChoices++;
  return {
    checked: true,
    equal: digestEqual && choicesEqual && invalidChoices === 0,
    countEqual: true,
    digestEqual,
    choicesEqual,
    invalidChoices,
    count: n,
    issued: items.length,
    browserDigest: review.digest,
    expectedDigest: built.digest,
    source: "the digest and choice vector the browser sent, against this builder over the items the server issued to the same administrator before the browser opened the panel",
  };
}

/** The exact byte length of a body for n reasons: only lengths matter, so the content is placeholder. */
export function bodyLength(n, { successors = {}, hostSuccessors = {}, attemptId = "" } = {}) {
  const review = n === 0 ? null : { format: FORMAT, count: String(n), digest: "0".repeat(64), choices: "A".repeat(4 * Math.ceil(Math.ceil(n / 4) / 3)) };
  return Buffer.byteLength(bodyFrom({ successors, hostSuccessors, attemptId, review }));
}

/** The most reasons whose body is within cap bytes, with the body sizes there and one reason further. Monotone in n, so a binary search is exact. */
export function capacity(cap, opts = {}) {
  if (bodyLength(1, opts) > cap) fail("one reason is already over the cap");
  let lo = 1;
  let hi = 2;
  while (bodyLength(hi, opts) <= cap) {
    lo = hi;
    hi *= 2;
  }
  while (lo + 1 < hi) {
    const mid = Math.floor((lo + hi) / 2);
    if (bodyLength(mid, opts) <= cap) lo = mid;
    else hi = mid;
  }
  return { n: lo, bytes: bodyLength(lo, opts), nextBytes: bodyLength(lo + 1, opts) };
}

// --- the OLD items envelope, for a comparison only -----------------------------------------------------------------------------------------------------
// {"reasonReview":{"items":[{"requestId","reviewHash","choice"}...]}}: what the pilots measured (52,760 entries fitted maxRequestBytes, 52,761 did not).
// The entries are SYNTHETIC (ids of the stated digit width, hashes of the ids): the server answers 413 for an oversize body before reading it and 409
// reason_review_outdated for a well-formed old envelope against a nonempty set, so nothing here depends on the content being a real review.
export function legacyBody(n, digits, { successors = {}, hostSuccessors = {}, attemptId = "" } = {}) {
  const base = 10n ** BigInt(digits - 1);
  const items = [];
  for (let i = 0; i < n; i++) {
    const id = String(base + BigInt(2 * i));
    items.push({ requestId: id, reviewHash: createHash("sha256").update(id).digest("hex"), choice: i < Math.ceil(n / 2) ? "remove" : "keep" });
  }
  const out = { successors, hostSuccessors };
  if (attemptId) out.attemptId = attemptId;
  out.reasonReview = { items };
  return JSON.stringify(out);
}

/** Exact old-form length without building the entries: the empty-items body plus each entry and the commas between them. */
// The ids are base + 2i, so their width GROWS with i (5-digit ids pass 99,999 after ~45,000 entries): the digits are summed entry by entry, not
// assumed constant. legacyCapacity re-checks the boundary against the real bytes of the real bodies at n and n + 1 (legacyBoundaryBytes).
export function legacyLength(n, digits, opts = {}) {
  const empty = Buffer.byteLength(legacyBody(0, digits, opts));
  if (n === 0) return empty;
  const fixed = Buffer.byteLength('{"requestId":"","reviewHash":"' + "0".repeat(64) + '","choice":""}');
  const base = 10n ** BigInt(digits - 1);
  let idDigits = 0;
  for (let i = 0; i < n; i++) idDigits += String(base + BigInt(2 * i)).length;
  return empty + n * fixed + idDigits + Math.ceil(n / 2) * 6 + Math.floor(n / 2) * 4 + (n - 1);
}

/** The byte lengths of the real bodies for n and n + 1 entries (built, not computed). */
export function legacyBoundaryBytes(n, digits, opts = {}) {
  return { at: Buffer.byteLength(legacyBody(n, digits, opts)), next: Buffer.byteLength(legacyBody(n + 1, digits, opts)) };
}

export function legacyCapacity(cap, digits, opts = {}) {
  if (legacyLength(1, digits, opts) > cap) fail("one old-form entry is already over the cap");
  let lo = 1;
  let hi = 2;
  while (legacyLength(hi, digits, opts) <= cap) {
    lo = hi;
    hi *= 2;
  }
  while (lo + 1 < hi) {
    const mid = Math.floor((lo + hi) / 2);
    if (legacyLength(mid, digits, opts) <= cap) lo = mid;
    else hi = mid;
  }
  const real = legacyBoundaryBytes(lo, digits, opts);
  if (real.at !== legacyLength(lo, digits, opts) || real.next !== legacyLength(lo + 1, digits, opts)) fail("the computed old-form length differs from the real body at the boundary");
  if (real.at > cap || real.next <= cap) fail("the old-form boundary is not the cap's boundary");
  return { n: lo, bytes: real.at, nextBytes: real.next };
}

function parseJsonObject(text, what) {
  let v;
  try {
    v = JSON.parse(text);
  } catch {
    fail(`${what} is not JSON`);
  }
  if (v === null || typeof v !== "object" || Array.isArray(v)) fail(`${what} is not an object`);
  return v;
}

// --- the command line -------------------------------------------------------------------------------------------------------------------------------

function args(argv) {
  const o = {};
  for (let i = 0; i < argv.length; i += 2) {
    const k = argv[i];
    if (!k.startsWith("--") || i + 1 >= argv.length) fail(`bad argument ${k}`);
    o[k.slice(2)] = argv[i + 1];
  }
  return o;
}

const readJson = (file) => JSON.parse(readFileSync(file, "utf8"));
const wholeNumber = (s, what) => {
  if (!/^(0|[1-9][0-9]*)$/.test(String(s))) fail(`${what} is not a whole number`);
  return Number(s);
};
const envOpts = (o) => ({
  successors: o.successors ? parseJsonObject(o.successors, "successors") : {},
  hostSuccessors: o.host ? parseJsonObject(o.host, "host") : {},
  attemptId: o.attempt ?? "",
});

export function run(cmd, o) {
  switch (cmd) {
    case "body": {
      const items = readJson(o.items);
      const n = wholeNumber(o.n, "n");
      checkItems(items, n);
      const codes = o.codes ? Uint8Array.from(o.codes.split(",").map(Number)) : codesFor(o.mode, n);
      if (codes.length !== n) fail("codes has the wrong length");
      return bodyFrom({ ...envOpts(o), review: reviewFor(items, n, codes) });
    }
    case "mutate-last": {
      const b = parseJsonObject(readFileSync(o.body, "utf8"), "body");
      const r = b.reasonReview;
      if (!r) fail("the body has no reasonReview");
      const n = wholeNumber(r.count, "count");
      const codes = decodeChoicesLoose(r.choices, n);
      codes[n - 1] = wholeNumber(o.code, "code") & 3;
      r.choices = encodeChoices(codes, n);
      return JSON.stringify(b);
    }
    case "mutate-digest": {
      const b = parseJsonObject(readFileSync(o.body, "utf8"), "body");
      const d = b.reasonReview?.digest;
      if (typeof d !== "string" || !HASH.test(d)) fail("the body has no digest");
      b.reasonReview.digest = (d[0] === "0" ? "1" : "0") + d.slice(1);
      return JSON.stringify(b);
    }
    case "mutate-count": {
      const b = parseJsonObject(readFileSync(o.body, "utf8"), "body");
      if (!b.reasonReview) fail("the body has no reasonReview");
      b.reasonReview.count = String(wholeNumber(b.reasonReview.count, "count") + 1);
      return JSON.stringify(b);
    }
    case "length":
      return String(bodyLength(wholeNumber(o.n, "n"), envOpts(o)));
    case "capacity": {
      const c = capacity(wholeNumber(o.cap, "cap"), envOpts(o));
      return `${c.n} ${c.bytes} ${c.nextBytes}`;
    }
    case "oversize": {
      const cap = wholeNumber(o.cap, "cap");
      const opts = envOpts(o);
      const n = capacity(cap, opts).n + 1;
      const review = { format: FORMAT, count: String(n), digest: "0".repeat(64), choices: encodeChoices(new Uint8Array(n).fill(1), n) };
      const text = bodyFrom({ ...opts, review });
      if (Buffer.byteLength(text) <= cap) fail("the oversize body fits the cap");
      return text;
    }
    case "legacy-body": {
      const n = wholeNumber(o.entries, "entries");
      return legacyBody(n, wholeNumber(o.digits ?? "5", "digits"), envOpts(o));
    }
    case "legacy-length":
      return String(legacyLength(wholeNumber(o.entries, "entries"), wholeNumber(o.digits ?? "5", "digits"), envOpts(o)));
    case "legacy-capacity": {
      const c = legacyCapacity(wholeNumber(o.cap, "cap"), wholeNumber(o.digits ?? "5", "digits"), envOpts(o));
      return `${c.n} ${c.bytes} ${c.nextBytes}`;
    }
    case "digest": {
      const items = readJson(o.items);
      return digestHex(items, wholeNumber(o.n ?? items.length, "n"));
    }
    case "agree":
      return JSON.stringify(agree(readJson(o.items), readJson(o.review)));
    case "dom-ids": {
      const shown = expectedShownIds(readJson(o.panel).screen, readJson(o.ids), wholeNumber(o.page, "page"));
      return shown.ok ? shown.ids.join(",") : "";
    }
    case "dom-text": {
      const screen = readJson(o.panel).screen;
      if (screen === null || typeof screen !== "object" || screen.captured !== true) return JSON.stringify({ checked: false, why: screen?.why ?? "the panel result holds no screen" });
      return JSON.stringify(domTextAgreement(screen, { orderedIds: readJson(o.ids), displayPage: wholeNumber(o.page, "page"), texts: readJson(o.texts), skip: readJson(o.skip) }));
    }
    case "selftest":
      return selftest(readJson(o.vectors));
    default:
      return fail(`unknown command ${cmd}`);
  }
}

// mutate-last works on a body the server would refuse or accept alike, so it reads the vector without insisting on valid codes.
function decodeChoicesLoose(b64, n) {
  const packed = Buffer.from(b64, "base64");
  if (packed.length !== Math.ceil(n / 4)) fail("choices has the wrong length for count");
  const codes = new Uint8Array(n);
  for (let i = 0; i < n; i++) codes[i] = (packed[i >> 2] >> ((i & 3) * 2)) & 3;
  return codes;
}

export function selftest(doc) {
  if (!Array.isArray(doc.vectors) || doc.vectors.length === 0) fail("the vector file holds no vectors");
  for (const v of doc.vectors) {
    const items = v.ids.map((id, i) => ({ requestId: id, reviewHash: v.hashes[i] }));
    const pre = preimage(items, items.length).toString("hex");
    if (pre !== v.preimageHex) fail(`vector '${v.name}': preimage differs`);
    if (digestHex(items) !== v.digest) fail(`vector '${v.name}': digest differs`);
    if (encodeChoices(Uint8Array.from(v.choices), v.choices.length) !== v.choicesBase64) fail(`vector '${v.name}': choices differ`);
    if (Buffer.compare(Buffer.from(decodeChoices(v.choicesBase64, v.choices.length)), Buffer.from(v.choices)) !== 0) fail(`vector '${v.name}': choices do not decode back`);
  }
  return `ok ${doc.vectors.length}`;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    const [cmd, ...rest] = process.argv.slice(2);
    if (!cmd) fail("a command is required");
    process.stdout.write(run(cmd, args(rest)));
  } catch (e) {
    process.stderr.write(`${e.message}\n`);
    process.exit(1);
  }
}
