import { createContext, useContext, useId, type InputHTMLAttributes, type ReactNode, type SelectHTMLAttributes, type TextareaHTMLAttributes } from "react";

// Field owns a control's label, hint and error (ENFORCEMENT.md A). It makes
// the id, htmlFor, aria-describedby (hint, then error) and aria-invalid, so
// a field can't be unlabeled or have an error nobody's screen reader hears.
// Input, Textarea and Select only work inside a Field.
type Wiring = { id: string; describedBy?: string; invalid: boolean };
const FieldContext = createContext<Wiring | null>(null);

export function Field({ label, hint, error, children }: { label: string; hint?: ReactNode; error?: ReactNode; children: ReactNode }) {
  const id = useId();
  const hintId = hint ? `${id}-hint` : undefined;
  const errorId = error ? `${id}-error` : undefined;
  const describedBy = [hintId, errorId].filter(Boolean).join(" ") || undefined;
  return (
    <div>
      <label className="label" htmlFor={id}>{label}</label>
      <FieldContext.Provider value={{ id, describedBy, invalid: !!error }}>{children}</FieldContext.Provider>
      {hint && <p id={hintId} className="mt-1 text-sm text-text-muted">{hint}</p>}
      {error && (
        <p id={errorId} className="mt-1 flex items-center gap-1 text-sm text-danger">
          <span aria-hidden="true">⚠</span>
          {error}
        </p>
      )}
    </div>
  );
}

function useField(control: string): Wiring {
  const w = useContext(FieldContext);
  if (!w) throw new Error(`<${control}> must sit inside a <Field label="…"> (components/ui/Field.tsx)`);
  return w;
}

const wire = (w: Wiring) => ({ id: w.id, "aria-describedby": w.describedBy, "aria-invalid": w.invalid || undefined });

export function Input(props: Omit<InputHTMLAttributes<HTMLInputElement>, "id" | "className" | "style">) {
  return <input className="input" {...props} {...wire(useField("Input"))} />;
}

export function Textarea(props: Omit<TextareaHTMLAttributes<HTMLTextAreaElement>, "id" | "className" | "style">) {
  return <textarea className="input" {...props} {...wire(useField("Textarea"))} />;
}

// Select's title is the chosen label, so a long one cut off at 390px can
// still be read on hover and by assistive tech (PR #51).
export function Select({ options, value, ...rest }: Omit<SelectHTMLAttributes<HTMLSelectElement>, "id" | "className" | "style" | "children" | "value"> & { value: string; options: { value: string; label: string }[] }) {
  const chosen = options.find((o) => o.value === value)?.label;
  return (
    <select className="input" value={value} title={chosen} {...rest} {...wire(useField("Select"))}>
      {options.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
    </select>
  );
}
