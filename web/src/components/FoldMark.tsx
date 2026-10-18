import type { SVGProps } from "react";

export function FoldMark(props: SVGProps<SVGSVGElement>) {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false" {...props}><path d="M4 20 12 4l8 16"/><path d="M8 20h8"/></svg>
  );
}
