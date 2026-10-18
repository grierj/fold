import { SearchIcon } from "../icons";

// The home page's group search (PR7-REVIEW F2): the one agreed exception
// to DEC-65's visible label. The headline above already says what it's
// for, so the label is for screen readers only; the search icon and the
// placeholder carry the meaning on screen.
export function SearchBox({ id, label, value, onChange }: { id: string; label: string; value: string; onChange: (v: string) => void }) {
  return (
    <div className="relative">
      <label className="sr-only" htmlFor={id}>
        {label}
      </label>
      <SearchIcon className="pointer-events-none absolute left-4 top-1/2 size-5 -translate-y-1/2 text-text-muted" />
      <input id={id} type="search" className="input pl-12" placeholder={label} value={value} onChange={(e) => onChange(e.target.value)} />
    </div>
  );
}
