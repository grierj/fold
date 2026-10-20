// Resize an image in the browser before upload, offloading the CPU work
// from the server. Returns a File ready to POST.
export async function prepareUpload(file: File, maxDim: number): Promise<File> {
  // Small enough already — skip the work.
  if (file.size <= 1024 * 1024) return file;
  // Loaded only here: the library is about 20 kB gzip that most visits
  // never need (#77). Vite emits it as a same-origin chunk, so the CSP holds.
  const { default: imageCompression } = await import("browser-image-compression");
  const compressed = await imageCompression(file, {
    maxSizeMB: 2,
    maxWidthOrHeight: maxDim,
    // The worker would load the library from a CDN, which the CSP blocks
    // (and which would tell a third party about every upload).
    useWebWorker: false,
    fileType: "image/jpeg",
  });
  return new File([compressed], file.name.replace(/\.[^.]+$/, "") + ".jpg", {
    type: "image/jpeg",
  });
}

// CSS object-position value from a 0-100 focal point.
export function focalPosition(x: number, y: number): string {
  return `${x}% ${y}%`;
}
