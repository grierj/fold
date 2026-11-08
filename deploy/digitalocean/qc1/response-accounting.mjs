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
