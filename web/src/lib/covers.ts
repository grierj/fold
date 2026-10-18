import cover1 from "../assets/covers/cover-1.svg";
import cover2 from "../assets/covers/cover-2.svg";
import cover3 from "../assets/covers/cover-3.svg";
import cover4 from "../assets/covers/cover-4.svg";
import cover5 from "../assets/covers/cover-5.svg";
import cover6 from "../assets/covers/cover-6.svg";

const COVERS = [cover1, cover2, cover3, cover4, cover5, cover6];

export function coverSrc(group: { id: number; imageUrl?: string | null }): string {
  const url = group.imageUrl?.trim();
  if (url) return url;
  return COVERS[((group.id % 6) + 6) % 6];
}
