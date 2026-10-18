import { useEffect, useMemo, useState } from "react";
import { filterZones, zonePickerOptions, type ZonePick } from "../../lib/eventForm";
import { useChurchZone } from "../../lib/useEventZone";
import { browserTimeZone, zoneOffset } from "../../lib/when";
import { ChevronDownIcon } from "../icons";
import { useField, wire } from "./Field";

// The one time zone picker (DEC-65), for every form except setup: a
// combobox (a text input that filters a listbox). At rest it shows the
// current choice in full; typing filters by city, region name ("pacific")
// or UTC offset ("-7"); arrow keys move, Enter picks, Esc closes. It goes
// inside a <Field label="Time zone">.
export function ZonePicker({
  value,
  onChange,
  forChurchSetting,
}: {
  value: string;
  onChange: (v: string) => void;
  // The admin's church-zone setting: the field *is* the church zone, so no
  // separate "Church" entry.
  forChurchSetting?: boolean;
}) {
  const w = useField("ZonePicker");
  const church = useChurchZone();
  const browser = browserTimeZone();
  const options = useMemo(
    () => zonePickerOptions(forChurchSetting ? null : church, browser, value, (z) => zoneOffset(z)),
    [forChurchSetting, church, browser, value],
  );
  const selected = options.find((o) => o.value === value);
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState<string | null>(null);
  const [active, setActive] = useState(0);
  const shown = useMemo(() => (query ? filterZones(options, query) : options), [options, query]);
  const listId = `${w.id}-list`;
  const optId = (i: number) => `${w.id}-opt-${i}`;

  useEffect(() => {
    if (open) document.getElementById(optId(active))?.scrollIntoView?.({ block: "nearest" });
    // optId only depends on w.id, which never changes.
  }, [open, active]);

  function openList() {
    if (open) return;
    setActive(Math.max(0, shown.findIndex((o) => o.value === value)));
    setOpen(true);
  }
  function close() {
    setOpen(false);
    setQuery(null);
  }
  function pick(o: ZonePick | undefined) {
    if (o) onChange(o.value);
    close();
  }
  function onKeyDown(e: React.KeyboardEvent<HTMLInputElement>) {
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      if (!open) return openList();
      const step = e.key === "ArrowDown" ? 1 : -1;
      setActive((a) => Math.min(Math.max(a + step, 0), Math.max(shown.length - 1, 0)));
    } else if (e.key === "Enter" && open) {
      e.preventDefault();
      pick(shown[active]);
    } else if (e.key === "Escape" && open) {
      e.preventDefault();
      close();
    }
  }

  const display = selected ? selected.label : value;
  return (
    <div className="relative">
      <input
        type="text"
        role="combobox"
        aria-autocomplete="list"
        aria-expanded={open}
        aria-controls={listId}
        aria-activedescendant={open && shown[active] ? optId(active) : undefined}
        autoComplete="off"
        spellCheck={false}
        className="input pr-11"
        value={query ?? display}
        placeholder={value ? undefined : "Loading…"}
        title={selected ? `${selected.label} · ${selected.value}` : undefined}
        required
        onFocus={(e) => e.currentTarget.select()}
        onClick={openList}
        onChange={(e) => {
          setQuery(e.target.value);
          setActive(0);
          setOpen(true);
        }}
        onKeyDown={onKeyDown}
        onBlur={close}
        {...wire(w)}
      />
      <ChevronDownIcon className="pointer-events-none absolute right-3 top-1/2 size-5 -translate-y-1/2 text-text-muted" />
      {open && shown.length > 0 && (
        <ul id={listId} role="listbox" aria-label="Time zones" className="listbox">
          {shown.map((o, i) => (
            <li
              key={o.value}
              id={optId(i)}
              role="option"
              aria-selected={i === active}
              className="listbox-option"
              // Keep focus in the input so the blur doesn't close the list first.
              onMouseDown={(e) => e.preventDefault()}
              onMouseMove={() => setActive(i)}
              onClick={() => pick(o)}
            >
              <span className="min-w-0">{o.label}</span>
              <span className="shrink-0 text-sm text-text-muted">
                {o.tag && <span className="font-semibold text-text">{o.tag} · </span>}
                {o.offset}
              </span>
            </li>
          ))}
        </ul>
      )}
      {open && shown.length === 0 && (
        <p id={listId} className="listbox px-4 py-2 text-sm text-text-muted" role="status">
          No time zone matches “{query}”.
        </p>
      )}
    </div>
  );
}
