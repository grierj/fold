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

// The reason text the browser shows on its final screen against the database's (#293). Nothing here is skipped silently: the screen must be exactly the page the pager line
// names, and every row on it must be the expected one, once, in order, holding exactly the database's text.
//
// `screen` is what the page showed when the run ended: {nav, navCount, rows: [{id, reasonElements, text}]}. `nav` is the textContent of the pager line (navCount of them in the
// page, which must be one); each row is a reason row with its data-reason-id, the number of reason-text elements inside it (which must be one) and the textContent of that element.
// `ctx` is what the harness holds it to: `orderedIds`, the ids of every reason the server issued, in the order the panel lists them; `displayPage`, the rows a page shows;
// `texts`, {id: exact reason string} read from the database after the run for the ids the screen should hold; and `skip` ({id, oldText} or null), the one reason the harness changed
// behind the page, which on screen is either the text the database held before the change or the one it holds now.
//
// A screen that could not be read at all is {checked: false}. A screen that was read and is wrong is checked, with membershipOk false or mismatched above zero: a pager line that
// is missing, in loading form, of another total, off a page boundary or not a full page; a row that is missing, unexpected, duplicated or out of order; a row whose reason is not one
// element; a row whose text differs by a single rune, including appended text. `exact` is true only when none of that happened and every row on the page was compared.
const NAV_LINE = /^Reasons ([\d,]+) to ([\d,]+) of (at least )?([\d,]+)$/;
const wholeNumber = (s) => Number(String(s).replaceAll(",", ""));

// The 1-based range the pager line names, held to the total and to the page size: {ok: true, from, to, total, atLeast} or {ok: false, why}.
export function shownRange(nav, navCount, total, displayPage) {
  if (navCount !== 1) return { ok: false, why: `the page holds ${navCount} pager lines, not one` };
  const m = typeof nav === "string" ? NAV_LINE.exec(nav.trim()) : null;
  if (m === null) return { ok: false, why: "the pager line is missing or not in the form 'Reasons A to B of N'" };
  const from = wholeNumber(m[1]);
  const to = wholeNumber(m[2]);
  const count = wholeNumber(m[4]);
  if (m[3] !== undefined) return { ok: false, why: "the pager line is still in its loading form (of at least N)" };
  if (count !== total) return { ok: false, why: `the pager line names ${count} reasons, the server issued ${total}` };
  if (!(from >= 1 && from <= to && to <= total)) return { ok: false, why: `the pager line's range ${from} to ${to} is outside 1 to ${total}` };
  if ((from - 1) % displayPage !== 0) return { ok: false, why: `the range starts at ${from}, which is not the start of a page of ${displayPage}` };
  if (to !== Math.min(from - 1 + displayPage, total)) return { ok: false, why: `the range ${from} to ${to} is not a whole page of ${displayPage}` };
  return { ok: true, from, to, total, atLeast: false };
}

// The ids the final screen should hold, from the pager line it shows: {ok: true, ids: [...]} or {ok: false, why}.
export function expectedShownIds(screen, orderedIds, displayPage) {
  if (screen === null || typeof screen !== "object" || !Array.isArray(orderedIds) || !Number.isInteger(displayPage) || displayPage < 1) return { ok: false, why: "no screen or no list of ids" };
  const r = shownRange(screen.nav, screen.navCount, orderedIds.length, displayPage);
  return r.ok ? { ok: true, from: r.from, to: r.to, total: r.total, ids: orderedIds.slice(r.from - 1, r.to) } : r;
}

const firstFive = (list) => list.slice(0, 5);

export function domTextAgreement(screen, ctx) {
  if (ctx === null || typeof ctx !== "object" || !Array.isArray(ctx.orderedIds) || ctx.texts === null || typeof ctx.texts !== "object" || Array.isArray(ctx.texts)) return { checked: false, why: "no expected reason text was given" };
  if (screen === null || typeof screen !== "object" || !Array.isArray(screen.rows)) return { checked: false, why: "the rows on screen could not be read" };
  const skip = ctx.skip ?? null;
  const shown = expectedShownIds(screen, ctx.orderedIds, ctx.displayPage);
  const rows = screen.rows;
  const out = { checked: true, membershipOk: false, exact: false, nav: null, expectedRows: null, rowsOnScreen: rows.length, rowsChecked: 0, skipped: 0, mismatched: 0, runesChecked: 0, firstMismatchId: null, missingIds: [], unexpectedIds: [], duplicateIds: [], badElements: 0 };
  if (!shown.ok) return { ...out, why: shown.why };
  out.nav = { from: shown.from, to: shown.to, total: shown.total };
  out.expectedRows = shown.ids.length;
  const expectedSet = new Set(shown.ids);
  const seen = new Map();
  for (const r of rows) seen.set(r?.id, (seen.get(r?.id) ?? 0) + 1);
  out.missingIds = firstFive(shown.ids.filter((id) => !seen.has(id)));
  out.unexpectedIds = firstFive([...seen.keys()].filter((id) => typeof id !== "string" || !expectedSet.has(id)).map(String));
  out.duplicateIds = firstFive([...seen].filter(([, n]) => n > 1).map(([id]) => String(id)));
  const inOrder = rows.length === shown.ids.length && rows.every((r, i) => r?.id === shown.ids[i]);
  out.membershipOk = inOrder && out.missingIds.length === 0 && out.unexpectedIds.length === 0 && out.duplicateIds.length === 0;
  const miss = (id) => {
    out.mismatched++;
    if (out.firstMismatchId === null) out.firstMismatchId = String(id);
  };
  const counted = new Set();
  for (const row of rows) {
    const id = row?.id;
    if (typeof id !== "string" || !expectedSet.has(id) || counted.has(id)) continue;
    counted.add(id);
    const want = ctx.texts[id];
    if (row.reasonElements !== 1) {
      out.badElements++;
      miss(id);
      continue;
    }
    if (typeof want !== "string" || typeof row.text !== "string") {
      miss(id);
      continue;
    }
    if (skip !== null && id === skip.id) {
      out.skipped++;
      if (row.text !== want && row.text !== skip.oldText) miss(id);
      continue;
    }
    out.rowsChecked++;
    out.runesChecked += Array.from(want).length;
    if (row.text !== want) miss(id);
  }
  out.exact = out.membershipOk && out.mismatched === 0 && out.rowsChecked + out.skipped === out.expectedRows;
  return out;
}
