// What the page walk's responses add up to (panel-measure.mjs). Kept apart so the
// accounting is tested without a browser.

// Sum of decoded body sizes, or null unless there is at least one response and
// every size is a non-negative whole number. A response whose body could not be
// read (a size of null or undefined) makes the whole figure unavailable: it is
// never treated as zero, and Content-Length is never used (a chunked response has
// none, a compressed one reports the wrong size).
export function totalBodyBytes(sizes) {
  if (!Array.isArray(sizes) || sizes.length === 0) return null;
  let sum = 0;
  for (const s of sizes) {
    if (!Number.isInteger(s) || s < 0) return null;
    sum += s;
  }
  return sum;
}

// "real" only when the walk made requests, every one answered 200 from the
// stack's own origin, every body was read and the number of bodies equals the
// number of responses seen.
export function walkOrigin(walk, bytes, bodies) {
  return walk.requests > 0 && walk.bad === 0 && walk.foreignOrigin === 0 && bytes !== null && bodies === walk.requests ? "real" : "unverified";
}

// Unicode code points in a UTF-8 body (Buffer or Uint8Array): the bytes that are not continuation bytes. A response of N bytes and R runes with N/R above 1 is text
// that is not ASCII; the harness reports both so a multibyte run's pages are seen to carry multibyte text.
export function countRunes(body) {
  let runes = 0;
  for (let i = 0; i < body.length; i++) if ((body[i] & 0xc0) !== 0x80) runes++;
  return runes;
}

// Bytes and runes over the page responses that completed. bytesPerRune is null when no response had both. Never a guess: a response without a figure is not counted.
export function bodyRuneSummary(observations) {
  let bytes = 0;
  let runes = 0;
  let counted = 0;
  for (const o of Array.isArray(observations) ? observations : []) {
    if (!Number.isInteger(o?.bytes) || !Number.isInteger(o?.runes) || o.runes < 1) continue;
    bytes += o.bytes;
    runes += o.runes;
    counted++;
  }
  return { responses: counted, bytes, runes, bytesPerRune: runes > 0 ? Math.round((bytes / runes) * 1000) / 1000 : null };
}

// The reason text the browser shows against the text the database holds (#293). `rows` is what is on screen, [{id, text}] (the textContent of each reason row, the
// id from its data attribute); `expected` maps a request id to the exact reason string the database holds for it. A row is checked when its id is in `expected`
// and is not `skipId` (the one reason the harness changed behind the page, whose text on screen is the new one); its text must CONTAIN the expected string, rune for
// rune. checked is true whenever the comparison could be made; rowsChecked says how many rows it covered, which the judge requires to be at least one for a
// multibyte run, so a page that showed none of the expected reasons is not taken for agreement.
export function domTextAgreement(rows, expected, skipId = null) {
  if (expected === null || typeof expected !== "object" || Array.isArray(expected)) return { checked: false, why: "no expected reason text was given" };
  if (!Array.isArray(rows)) return { checked: false, why: "the rows on screen could not be read" };
  let rowsChecked = 0;
  let mismatched = 0;
  let skipped = 0;
  let runesChecked = 0;
  let firstMismatchId = null;
  for (const row of rows) {
    const id = row?.id;
    if (typeof id !== "string" || !Object.hasOwn(expected, id)) continue;
    if (skipId !== null && id === skipId) {
      skipped++;
      continue;
    }
    const want = expected[id];
    rowsChecked++;
    if (typeof want === "string") runesChecked += Array.from(want).length;
    if (typeof want !== "string" || typeof row.text !== "string" || !row.text.includes(want)) {
      mismatched++;
      if (firstMismatchId === null) firstMismatchId = id;
    }
  }
  return { checked: true, rowsOnScreen: rows.length, rowsChecked, mismatched, skipped, runesChecked, firstMismatchId, expectedIds: Object.keys(expected).length };
}
