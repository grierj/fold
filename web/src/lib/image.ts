import imageCompression from "browser-image-compression";

// Resize an image in the browser before upload, offloading the CPU work
// from the server. Returns a File ready to POST.
export async function prepareUpload(file: File, maxDim: number): Promise<File> {
  // Small enough already — skip the work.
  if (file.size <= 1024 * 1024) return file;
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
