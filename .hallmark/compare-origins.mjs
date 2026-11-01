// Verification instances intentionally use distinct ports. Match the same routes
// for Hallmark's kept-content check without changing their captured contents.
// The original inventory and images remain beside the normalized copies.
import { readFileSync, writeFileSync, readdirSync, copyFileSync, existsSync } from 'node:fs';
const [beforeOrigin, afterOrigin] = process.argv.slice(2);
if (!beforeOrigin || !afterOrigin) throw new Error('Pass the before and after loopback origins.');
for (const origin of [beforeOrigin, afterOrigin]) {
  if (new URL(origin).hostname !== '127.0.0.1') throw new Error('Verification origins must be loopback.');
}
const original = '.hallmark/before-original.json';
if (!existsSync(original)) copyFileSync('.hallmark/before.json', original);
const record = JSON.parse(readFileSync(original, 'utf8'));
record.pages = Object.fromEntries(Object.entries(record.pages ?? {}).map(([url, page]) => [url.replace(beforeOrigin, afterOrigin), page]));
record.originMapping = { beforeOrigin, afterOrigin, reason: 'Separate isolated verify-fold instances; contents unchanged.' };
writeFileSync('.hallmark/before.json', JSON.stringify(record, null, 2) + '\n');
const slug = (origin) => origin.replace(/^https?:\/\//, '').replace(/[^\w.-]+/g, '-');
for (const name of readdirSync('.hallmark/before')) {
  if (name.startsWith(slug(beforeOrigin))) copyFileSync(`.hallmark/before/${name}`, `.hallmark/before/${name.replace(slug(beforeOrigin), slug(afterOrigin))}`);
}
