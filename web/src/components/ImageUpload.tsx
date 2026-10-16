import { useEffect, useRef, useState } from "react";
import { focalPosition, prepareUpload } from "../lib/image";

// ImageUpload: pick a file (resized in the browser), preview it, and click
// to set the focal point used for object-position when cropped.
export function ImageUpload({
  label,
  initialUrl,
  initialFocal,
  maxDim,
  aspectClass,
  onFile,
  onFocal,
}: {
  label: string;
  initialUrl?: string;
  initialFocal: { x: number; y: number };
  maxDim: number;
  aspectClass: string; // e.g. "aspect-square" or "aspect-video"
  onFile: (file: File | null) => void;
  onFocal: (f: { x: number; y: number }) => void;
}) {
  const [preview, setPreview] = useState<string | null>(initialUrl ?? null);
  const [focal, setFocal] = useState(initialFocal);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    return () => {
      if (preview && preview.startsWith("blob:")) URL.revokeObjectURL(preview);
    };
  }, [preview]);

  async function pick(e: React.ChangeEvent<HTMLInputElement>) {
    const f = e.target.files?.[0];
    e.target.value = "";
    if (!f) return;
    setBusy(true);
    setError(null);
    try {
      const resized = await prepareUpload(f, maxDim);
      const url = URL.createObjectURL(resized);
      if (preview && preview.startsWith("blob:")) URL.revokeObjectURL(preview);
      setPreview(url);
      onFile(resized);
    } catch {
      setError("Could not process that image.");
    } finally {
      setBusy(false);
    }
  }

  function clear() {
    if (preview && preview.startsWith("blob:")) URL.revokeObjectURL(preview);
    setPreview(initialUrl ?? null);
    onFile(null);
  }

  function setPoint(e: React.MouseEvent<HTMLDivElement>) {
    const rect = e.currentTarget.getBoundingClientRect();
    const next = {
      x: Math.min(100, Math.max(0, Math.round(((e.clientX - rect.left) / rect.width) * 100))),
      y: Math.min(100, Math.max(0, Math.round(((e.clientY - rect.top) / rect.height) * 100))),
    };
    setFocal(next);
    onFocal(next);
  }

  return (
    <div>
      <label className="label">{label}</label>
      <div
        className={`relative ${aspectClass} w-full max-w-sm overflow-hidden rounded-lg bg-gray-100 border border-gray-200 cursor-crosshair`}
        onClick={setPoint}
        title="Click to set the focal point"
      >
        {preview ? (
          <img
            src={preview}
            alt=""
            className="w-full h-full object-cover pointer-events-none"
            style={{ objectPosition: focalPosition(focal.x, focal.y) }}
          />
        ) : (
          <div className="w-full h-full flex items-center justify-center text-gray-400 text-sm">
            No image
          </div>
        )}
        {preview && (
          <div
            className="absolute w-6 h-6 -ml-3 -mt-3 rounded-full border-2 border-white shadow-sm pointer-events-none"
            style={{ left: `${focal.x}%`, top: `${focal.y}%` }}
          />
        )}
      </div>
      <p className="text-xs text-gray-500 mt-1">
        Click the image to move the focal point — faces stay centered when cropped.
      </p>
      <div className="flex gap-2 mt-2">
        <button
          type="button"
          onClick={() => inputRef.current?.click()}
          disabled={busy}
          className="btn-secondary py-1.5! px-3! text-sm"
        >
          {busy ? "Processing…" : preview ? "Choose different image" : "Choose image"}
        </button>
        {preview && (
          <button
            type="button"
            onClick={clear}
            className="btn-secondary py-1.5! px-3! text-sm"
          >
            Clear
          </button>
        )}
        <input
          ref={inputRef}
          type="file"
          accept="image/jpeg,image/png,image/gif,image/webp"
          className="hidden"
          onChange={pick}
        />
      </div>
      {error && <div className="text-red-600 text-sm mt-1">{error}</div>}
    </div>
  );
}
