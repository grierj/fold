import { useEffect, useRef, useState } from "react";
import { focalPosition, prepareUpload } from "../lib/image";
import { Avatar } from "./Avatar";
import { Button } from "./ui/Button";
import { FormError, HiddenFileInput } from "./ui/Field";

// ImageUpload: pick a file (resized in the browser), preview it, and click
// to set the focal point used for object-position when cropped. With no image
// it is a compact row (a 64px stand-in and "Choose photo"); the crop box and
// its hint only appear once there is something to crop (DEC-99).
export function ImageUpload({
  label,
  initialUrl,
  initialFocal,
  maxDim,
  aspectClass,
  onFile,
  onFocal,
  placeholderSrc,
  placeholderName,
}: {
  label: string;
  initialUrl?: string;
  initialFocal: { x: number; y: number };
  maxDim: number;
  aspectClass: string; // e.g. "aspect-square" or "aspect-video"
  onFile: (file: File | null) => void;
  onFocal: (f: { x: number; y: number }) => void;
  // The stand-in shown while there is no image: what people will see instead
  // (a group's pattern cover), or the person's initials for an avatar.
  placeholderSrc?: string;
  placeholderName?: string;
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
      setPreview(url);
      onFile(resized);
    } catch {
      setError("Could not process that image.");
    } finally {
      setBusy(false);
    }
  }

  function clear() {
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

  const chooser = (
    <>
      <Button variant="secondary" onClick={() => inputRef.current?.click()} disabled={busy}>
        {busy ? "Processing…" : preview ? "Choose different image" : "Choose photo"}
      </Button>
      <HiddenFileInput ref={inputRef} accept="image/jpeg,image/png,image/gif,image/webp" onChange={pick} />
    </>
  );

  if (!preview) {
    return (
      <div>
        <p className="label">{label}</p>
        <div className="flex items-center gap-4">
          {placeholderSrc ? (
            <img src={placeholderSrc} alt="" className="w-16 h-16 shrink-0 rounded-md object-cover bg-sunken border border-border" />
          ) : placeholderName ? (
            <Avatar name={placeholderName} size="w-16 h-16" textSize="text-2xl" />
          ) : (
            <div aria-hidden="true" className="w-16 h-16 shrink-0 rounded-md bg-sunken border border-border" />
          )}
          {chooser}
        </div>
        {error && <FormError layout="mt-1">{error}</FormError>}
      </div>
    );
  }

  return (
    <div>
      <p className="label">{label}</p>
      <div
        className={`relative ${aspectClass} w-full max-w-sm overflow-hidden rounded-md bg-sunken border border-border cursor-crosshair`}
        onClick={setPoint}
        title="Click to set the focal point"
      >
        <img
          src={preview}
          alt=""
          className="w-full h-full object-cover pointer-events-none"
          style={{ objectPosition: focalPosition(focal.x, focal.y) }}
        />
        <div
          className="absolute w-6 h-6 -ml-3 -mt-3 rounded-full border-2 border-inverse-foreground ring-1 ring-inverse shadow-sm pointer-events-none"
          style={{ left: `${focal.x}%`, top: `${focal.y}%` }}
        />
      </div>
      <p className="text-xs text-text-muted mt-1">
        Tap or click the image to move the focal point. Faces stay centered when cropped.
      </p>
      <div className="flex gap-2 mt-2">
        {chooser}
        <Button variant="secondary" onClick={clear}>
          Clear
        </Button>
      </div>
      {error && <FormError layout="mt-1">{error}</FormError>}
    </div>
  );
}
