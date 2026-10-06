import { focalPosition } from "../lib/image";

// Avatar: circular cropped avatar with focal-point-aware object-position.
// Falls back to the initial letter when there's no image.
export function Avatar({
  url,
  name,
  focalX = 50,
  focalY = 50,
  size = "w-10 h-10",
  textSize = "",
}: {
  url?: string;
  name: string;
  focalX?: number;
  focalY?: number;
  size?: string;
  textSize?: string;
}) {
  return (
    <div
      className={`${size} rounded-full overflow-hidden bg-brand-light text-brand-dark flex items-center justify-center font-bold shrink-0 ${textSize}`}
    >
      {url ? (
        <img
          src={url}
          alt=""
          className="w-full h-full object-cover"
          style={{ objectPosition: focalPosition(focalX, focalY) }}
        />
      ) : (
        name.charAt(0).toUpperCase()
      )}
    </div>
  );
}
