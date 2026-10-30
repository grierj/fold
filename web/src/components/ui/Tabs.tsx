import { useId, type KeyboardEvent, type ReactNode } from "react";

type Option<T extends string> = { id: T; label: ReactNode; accessibleName?: string };

// Automatic-activation tabs: one Tab stop, arrows (wrapping), Home and End.
// The active panel follows in reading order; overflow stays inside the bar.
export function Tabs<T extends string>({ label, options, value, onChange, variant = "underline", children }: {
  label: string;
  options: Option<T>[];
  value: T;
  onChange: (value: T) => void;
  variant?: "underline" | "chips";
  children: ReactNode;
}) {
  const id = useId();
  function onKeyDown(event: KeyboardEvent<HTMLButtonElement>, index: number) {
    const next = {
      ArrowRight: (index + 1) % options.length,
      ArrowLeft: (index - 1 + options.length) % options.length,
      Home: 0,
      End: options.length - 1,
    }[event.key];
    if (next === undefined) return;
    event.preventDefault();
    onChange(options[next].id);
    const button = event.currentTarget.parentElement?.querySelectorAll<HTMLButtonElement>("[role=tab]")[next];
    button?.focus();
    button?.scrollIntoView?.({ block: "nearest", inline: "nearest" });
  }
  return <>
    <nav aria-label={label} className="mb-6 overflow-x-auto border-b border-border">
      <div role="tablist" aria-label={label} className={variant === "chips"
        ? "grid min-w-max grid-cols-[auto_auto_auto] gap-2 pb-4 sm:flex sm:flex-wrap"
        : "flex min-w-max gap-6"}>
        {options.map((option, index) => <button
          key={option.id}
          type="button"
          role="tab"
          id={`${id}-tab-${option.id}`}
          aria-controls={`${id}-panel-${option.id}`}
          aria-selected={value === option.id}
          aria-label={option.accessibleName}
          tabIndex={value === option.id ? 0 : -1}
          onClick={() => onChange(option.id)}
          onKeyDown={(event) => onKeyDown(event, index)}
          className={variant === "chips" ? "chip whitespace-nowrap" : `min-h-11 whitespace-nowrap border-b-2 pb-3 font-medium ${value === option.id
            ? "border-primary text-primary font-semibold"
            : "border-transparent text-text-muted hover:text-text"}`}
        >{option.label}</button>)}
      </div>
    </nav>
    {options.map((option) => <div key={option.id} role="tabpanel" id={`${id}-panel-${option.id}`}
      aria-labelledby={`${id}-tab-${option.id}`} hidden={value !== option.id} tabIndex={0}>
      {value === option.id && children}
    </div>)}
  </>;
}
