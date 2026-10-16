// Compares two SCREENS_DIR captures pixel by pixel (no extra packages: it
// decodes PNGs in a headless Chromium canvas). Prints one line per file:
// identical, the share of differing pixels, or a size change.
//   node e2e/compare-screens.mjs /tmp/shots/before /tmp/shots/after
import fs from "node:fs";
import path from "node:path";
import { chromium } from "@playwright/test";

const [a, b] = process.argv.slice(2);
const files = fs.readdirSync(a).filter((f) => f.endsWith(".png")).sort();
const browser = await chromium.launch();
const page = await browser.newPage();
const out = [];
for (const f of files) {
  if (!fs.existsSync(path.join(b, f))) { out.push(`${f}: missing in ${b}`); continue; }
  const [da, db] = [a, b].map((d) => "data:image/png;base64," + fs.readFileSync(path.join(d, f)).toString("base64"));
  const r = await page.evaluate(async ([x, y]) => {
    const load = (src) => new Promise((ok) => { const i = new Image(); i.onload = () => ok(i); i.src = src; });
    const [ia, ib] = await Promise.all([load(x), load(y)]);
    if (ia.width !== ib.width || ia.height !== ib.height) return { size: `${ia.width}x${ia.height} -> ${ib.width}x${ib.height}` };
    const px = (img) => { const c = new OffscreenCanvas(img.width, img.height); const g = c.getContext("2d"); g.drawImage(img, 0, 0); return g.getImageData(0, 0, img.width, img.height).data; };
    const pa = px(ia), pb = px(ib);
    let diff = 0, minY = Infinity, maxY = -1;
    for (let i = 0; i < pa.length; i += 4) {
      if (Math.abs(pa[i] - pb[i]) + Math.abs(pa[i + 1] - pb[i + 1]) + Math.abs(pa[i + 2] - pb[i + 2]) > 24) {
        diff++; const yy = Math.floor(i / 4 / ia.width); minY = Math.min(minY, yy); maxY = Math.max(maxY, yy);
      }
    }
    return { diff, total: pa.length / 4, minY, maxY };
  }, [da, db]);
  out.push(r.size ? `${f}: size ${r.size}` : r.diff === 0 ? `${f}: identical` : `${f}: ${((100 * r.diff) / r.total).toFixed(3)}% differ (rows ${r.minY}-${r.maxY})`);
}
await browser.close();
console.log(out.join("\n"));
