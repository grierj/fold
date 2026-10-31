import { createContext, useContext, useId, type InputHTMLAttributes, type ReactNode, type Ref, type SelectHTMLAttributes, type TextareaHTMLAttributes } from "react";
import { flushSync } from "react-dom";
import { FormErrorsContext } from "./FormContext";

// The one form pattern (DEC-65, PR7-SPEC): the label sits above the
// control, the hint sits under the label and above the control (so it's
// read before typing), and the error sits under the control. Fields are
// required by default; only optional ones say " (optional)". Field makes
// the id, htmlFor, aria-describedby (hint, then error) and aria-invalid, so
// a field can't be unlabeled or have an error nobody's screen reader hears.
// Input, Textarea, Select and ZonePicker only work inside a Field, and the
// Go guard keeps raw <input>, <select> and <textarea> inside components/ui.
// labelId names a control that a <label for> can't (the composer's editor).
type Wiring = { id: string; labelId: string; describedBy?: string; invalid: boolean };
const FieldContext = createContext<Wiring | null>(null);

type FieldProps = {
  label: ReactNode;
  hint?: ReactNode;
  error?: ReactNode;
  optional?: boolean;
  // A fixed id, for a field that something else points at.
  id?: string;
  children: ReactNode;
};

export function Field({ label, hint, error: providedError, optional, id: fixed, children }: FieldProps) {
  const auto = useId();
  const id = fixed ?? auto;
  const error = useContext(FormErrorsContext).get(id) ?? providedError;
  const hintId = hint ? `${id}-hint` : undefined;
  const errorId = error ? `${id}-error` : undefined;
  const describedBy = [hintId, errorId].filter(Boolean).join(" ") || undefined;
  return (
    <div>
      <label id={`${id}-label`} className="label" htmlFor={id}>
        {label}
        {optional && <span className="font-normal text-text-muted"> (optional)</span>}
      </label>
      {hint && <p id={hintId} className="field-hint">{hint}</p>}
      <FieldContext.Provider value={{ id, labelId: `${id}-label`, describedBy, invalid: !!error }}>{children}</FieldContext.Provider>
      {error && <FieldError id={errorId}>{error}</FieldError>}
    </div>
  );
}

// An error line: danger text with an icon, so it isn't color alone.
function FieldError({ id, children }: { id?: string; children: ReactNode }) {
  return (
    <p id={id} className="mt-1 flex items-center gap-1 text-sm text-danger">
      <span aria-hidden="true">⚠</span>
      {children}
    </p>
  );
}

export function useField(control: string): Wiring {
  const w = useContext(FieldContext);
  if (!w) throw new Error(`<${control}> must sit inside a <Field label="…"> (components/ui/Field.tsx)`);
  return w;
}

export const wire = (w: Wiring) => ({ id: w.id, "aria-describedby": w.describedBy, "aria-invalid": w.invalid || undefined });

// `layout` takes width classes only, for short fields such as a count or a
// time ("w-28"); on phones a short field still sits on its own row.
type Layout = { layout?: string };
const cls = (layout?: string) => (layout ? `input ${layout}` : "input");

export function Input({ layout, ref, ...props }: Omit<InputHTMLAttributes<HTMLInputElement>, "id" | "className" | "style"> & Layout & { ref?: Ref<HTMLInputElement> }) {
  return <input ref={ref} className={cls(layout)} {...props} {...wire(useField("Input"))} />;
}

export function Textarea({ layout, ...props }: Omit<TextareaHTMLAttributes<HTMLTextAreaElement>, "id" | "className" | "style"> & Layout) {
  return <textarea className={cls(layout)} {...props} {...wire(useField("Textarea"))} />;
}

// Select's title is the chosen label, so a long one cut off at 390px can
// still be read on hover and by assistive tech (PR #51). Use it for five or
// more choices; two to four use radio rows (Choices) or a segmented control.
// Pass options, or <option> children when the labels need markup.
export function Select({
  options,
  value,
  layout,
  children,
  ...rest
}: Omit<SelectHTMLAttributes<HTMLSelectElement>, "id" | "className" | "style" | "value"> &
  Layout & { value: string | number; options?: { value: string; label: string }[] }) {
  const chosen = options?.find((o) => o.value === String(value))?.label;
  return (
    <select className={cls(layout)} value={value} title={chosen} {...rest} {...wire(useField("Select"))}>
      {options ? options.map((o) => <option key={o.value} value={o.value}>{o.label}</option>) : children}
    </select>
  );
}

// A file input has no visible box of its own: a Button opens it, and its
// label is the button's text.
export function HiddenFileInput({ ref, ...props }: Omit<InputHTMLAttributes<HTMLInputElement>, "type" | "className" | "style" | "hidden"> & { ref?: Ref<HTMLInputElement> }) {
  return <input ref={ref} type="file" className="hidden" tabIndex={-1} aria-hidden="true" {...props} />;
}

// --- Radio and checkbox rows ---
//
// The whole row is the <label>, at least 44px tall, with a 20px control and
// the text beside it; a description can sit under the text. Rows that go
// together sit in <Choices legend="…">, a fieldset whose legend looks like
// a field label.

export function Choices({ legend, hint, error: providedError, optional, children }: { legend: ReactNode; hint?: ReactNode; error?: ReactNode; optional?: boolean; children: ReactNode }) {
  const id = useId();
  const error = useContext(FormErrorsContext).get(id) ?? providedError;
  const hintId = hint ? `${id}-hint` : undefined;
  const errorId = error ? `${id}-error` : undefined;
  const describedBy = [hintId, errorId].filter(Boolean).join(" ") || undefined;
  return (
    <fieldset id={id} aria-describedby={describedBy} aria-invalid={error ? true : undefined}>
      <legend className="label">
        {legend}
        {optional && <span className="font-normal text-text-muted"> (optional)</span>}
      </legend>
      {hint && <p id={hintId} className="field-hint">{hint}</p>}
      <div>{children}</div>
      {error && <FieldError id={errorId}>{error}</FieldError>}
    </fieldset>
  );
}

type RowProps = Omit<InputHTMLAttributes<HTMLInputElement>, "type" | "className" | "style" | "children"> & {
  label: ReactNode;
  description?: ReactNode;
};

function Row({ type, label, description, id: fixed, ...input }: RowProps & { type: "radio" | "checkbox" }) {
  const auto = useId();
  const id = fixed ?? auto;
  const error = useContext(FormErrorsContext).get(id);
  const errorId = error ? `${id}-error` : undefined;
  const describedBy = [input["aria-describedby"], errorId].filter(Boolean).join(" ") || undefined;
  return (
    <div>
      <label className="choice-row">
        <input type={type} className="choice-control" {...input} id={id} aria-invalid={error ? true : input["aria-invalid"]} aria-describedby={describedBy} />
        <span className="min-w-0">
          <span className="block">{label}</span>
          {description && <span className="block text-sm text-text-muted">{description}</span>}
        </span>
      </label>
      {error && <FieldError id={errorId}>{error}</FieldError>}
    </div>
  );
}

export const Radio = (p: RowProps) => <Row type="radio" {...p} />;
export const Checkbox = (p: RowProps) => <Row type="checkbox" {...p} />;

// On submit, move focus to the first field marked invalid (DEC-65). Call it
// after the errors have rendered, for example from a useEffect on them.
export function focusFirstInvalid(form: HTMLElement | null): boolean {
  const el = form?.querySelector<HTMLElement>('[aria-invalid="true"]');
  if (!el) return false;
  const target = el.tagName === "FIELDSET" ? el.querySelector<HTMLElement>('input:not(:disabled):not([type="hidden"]), select:not(:disabled), textarea:not(:disabled)') : el;
  target?.focus();
  return !!target;
}

// Show field errors and move focus to the first one, in one step: the
// errors render first (flushSync), then focus moves.
export function showFieldErrors(form: HTMLElement | null, update: () => void) {
  flushSync(update);
  focusFirstInvalid(form);
}

// An error about the whole form (wrong password, the server said no),
// above the fields. Field-level errors go on their Field instead.
// `layout` takes spacing classes only ("mt-2").
export function FormError({ layout, children }: { layout?: string; children: ReactNode }) {
  return (
    <p role="alert" className={`flex items-start gap-1 text-sm text-danger${layout ? ` ${layout}` : ""}`}>
      <span aria-hidden="true">⚠</span>
      <span>{children}</span>
    </p>
  );
}

// A spam trap: a field people never see or fill in, which bots do. Hidden
// from everyone, including screen readers and the tab order.
export function Honeypot({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  return (
    <div className="hidden" aria-hidden="true">
      <label>
        Website
        <input name="website" tabIndex={-1} autoComplete="off" value={value} onChange={(e) => onChange(e.target.value)} />
      </label>
    </div>
  );
}
