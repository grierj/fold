import { useEffect, useId, useRef, useState } from "react";

// One shared "More" menu (UI makeover PR 6, DEC-63): a view shows its one
// primary action and puts the rest here. It follows the WAI-ARIA menu
// button pattern: the trigger says aria-expanded, opening moves focus to
// the first item, arrows/Home/End move between items, Escape closes and
// returns focus to the trigger, Tab or a click outside closes it. Items are
// 44px tall. Pass `label` so a page with several menus names each one
// ("More for Sam Member"); the visible text stays "More".
export interface MenuItem {
  label: string;
  onSelect: () => void;
  danger?: boolean;
  disabled?: boolean;
}

export function Menu({ items, label = "More", text = "More", align = "end", tone = "secondary" }: {
  items: MenuItem[];
  label?: string;
  text?: string;
  align?: "start" | "end";
  tone?: "secondary" | "quiet";
}) {
  const [open, setOpen] = useState(false);
  // Which edge the menu lines up with; flips when the preferred one would
  // push it off the screen (a trigger near the left edge of a phone).
  const [side, setSide] = useState(align);
  const id = useId();
  const root = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const itemRefs = useRef<(HTMLButtonElement | null)[]>([]);

  useEffect(() => {
    if (!open) return;
    // Keep the whole menu on screen: flip sides if it would leave the
    // screen sideways, and scroll it clear of the tab bar (scroll-padding)
    // when it opens near the bottom.
    const el = document.getElementById(id);
    const r = el?.getBoundingClientRect();
    if (r && r.width > 0) {
      if (r.left < 0) setSide("start");
      else if (r.right > window.innerWidth) setSide("end");
    }
    el?.scrollIntoView?.({ block: "nearest" });
    itemRefs.current.find((b) => b && !b.disabled)?.focus({ preventScroll: true });
    const away = (e: PointerEvent) => {
      if (!root.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("pointerdown", away);
    return () => document.removeEventListener("pointerdown", away);
  }, [open, id]);

  if (items.length === 0) return null;

  function close(focusTrigger: boolean) {
    setOpen(false);
    if (focusTrigger) trigger.current?.focus();
  }

  function onKey(e: React.KeyboardEvent) {
    const enabled = itemRefs.current.filter((b): b is HTMLButtonElement => !!b && !b.disabled);
    const at = enabled.indexOf(document.activeElement as HTMLButtonElement);
    const go = (i: number) => enabled[(i + enabled.length) % enabled.length]?.focus();
    switch (e.key) {
      case "ArrowDown": e.preventDefault(); go(at + 1); break;
      case "ArrowUp": e.preventDefault(); go(at - 1); break;
      case "Home": e.preventDefault(); go(0); break;
      case "End": e.preventDefault(); go(enabled.length - 1); break;
      case "Escape": e.preventDefault(); close(true); break;
      case "Tab": close(false); break;
    }
  }

  const look = tone === "quiet"
    ? "inline-flex items-center min-h-11 px-2 text-sm font-medium text-primary"
    : "btn-secondary";

  return (
    <div ref={root} className="relative inline-block" onKeyDown={open ? onKey : undefined}>
      <button
        ref={trigger}
        type="button"
        className={look}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? id : undefined}
        aria-label={label === text ? undefined : label}
        onClick={() => { setSide(align); setOpen((v) => !v); }}
        onKeyDown={(e) => {
          if (!open && (e.key === "ArrowDown" || e.key === "ArrowUp")) { e.preventDefault(); setOpen(true); }
        }}
      >
        {text}
      </button>
      {open && (
        <div
          id={id}
          role="menu"
          aria-label={label}
          className={`absolute z-30 mt-1 min-w-48 rounded-lg border border-border bg-surface py-1 shadow-lg ${side === "end" ? "right-0" : "left-0"}`}
        >
          {items.map((it, i) => (
            <button
              key={it.label}
              ref={(b) => { itemRefs.current[i] = b; }}
              type="button"
              role="menuitem"
              tabIndex={-1}
              disabled={it.disabled}
              onClick={() => { close(true); it.onSelect(); }}
              className={`flex w-full min-h-11 items-center px-4 text-left text-sm font-medium hover:bg-sunken focus:bg-sunken focus-visible:outline-2 focus-visible:outline-focus focus-visible:-outline-offset-2 disabled:opacity-50 ${it.danger ? "text-danger" : "text-text"}`}
            >
              {it.label}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
