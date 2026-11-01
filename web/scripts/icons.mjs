// Makes the Home Screen and manifest icons (M3.13c1) from the one source,
// public/assets/brand/fold-icon-1.svg, by rendering it in Playwright's
// Chromium at each size: `npm run icons` in web/, then commit the PNGs.
// Run it again only when the SVG changes. No Python, no image library.
//
// - fold-icon-192.png, fold-icon-512.png: the icon as drawn (rounded
//   corners, transparent outside), "any" purpose in the manifest.
// - fold-maskable-512.png: full-bleed violet with the mark shrunk into the
//   centre 80% safe zone, since Android crops maskable icons to its shape.
// - fold-touch-180.png: apple-touch-icon, full-bleed (iOS rounds the
//   corners itself and fills transparency with black).
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { chromium } from "@playwright/test";

const brand = resolve(import.meta.dirname, "../public/assets/brand");
const source = readFileSync(resolve(brand, "fold-icon-1.svg"), "utf8");

// fullBleed drops the background's rounded corners.
const fullBleed = (svg) => svg.replace(/(<rect\b[^>]*?)\s+rx="[^"]*"/, "$1");
// safeZone also shrinks the mark about the centre (viewBox 0 0 32 32).
const safeZone = (svg, scale) =>
  fullBleed(svg).replace(/(<g\b[\s\S]*<\/g>)/, `<g transform="translate(16 16) scale(${scale}) translate(-16 -16)">$1</g>`);

const icons = [
  { file: "fold-icon-192.png", size: 192, svg: source },
  { file: "fold-icon-512.png", size: 512, svg: source },
  { file: "fold-maskable-512.png", size: 512, svg: safeZone(source, 0.8) },
  { file: "fold-touch-180.png", size: 180, svg: fullBleed(source) },
];

const browser = await chromium.launch();
try {
  for (const icon of icons) {
    const page = await browser.newPage({ viewport: { width: icon.size, height: icon.size }, deviceScaleFactor: 1 });
    const src = `data:image/svg+xml;base64,${Buffer.from(icon.svg).toString("base64")}`;
    await page.setContent(`<!doctype html><html><body style="margin:0;background:transparent"><img src="${src}" width="${icon.size}" height="${icon.size}" style="display:block"></body></html>`);
    await page.locator("img").evaluate((img) => img.decode());
    await page.screenshot({ path: resolve(brand, icon.file), omitBackground: true, clip: { x: 0, y: 0, width: icon.size, height: icon.size } });
    await page.close();
    console.log(`icons: ${icon.file} (${icon.size}px)`);
  }
} finally {
  await browser.close();
}
