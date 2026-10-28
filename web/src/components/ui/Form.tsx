import { useState, type FormHTMLAttributes, type Ref } from "react";
import { flushSync } from "react-dom";
import { showFieldErrors } from "./Field";
import { FormErrorsContext } from "./FormContext";

type Control = HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement;
type Props = Omit<FormHTMLAttributes<HTMLFormElement>, "noValidate"> & {
  noValidate?: true;
  ref?: Ref<HTMLFormElement>;
};
type FormSubmitEvent = Parameters<NonNullable<Props["onSubmit"]>>[0];
type FormChangeEvent = Parameters<NonNullable<Props["onChange"]>>[0];

function isControl(value: EventTarget | null): value is Control {
  return value instanceof HTMLInputElement || value instanceof HTMLSelectElement || value instanceof HTMLTextAreaElement;
}

function keyFor(control: Control): string {
  return control instanceof HTMLInputElement && control.type === "radio"
    ? control.closest("fieldset")?.id || control.id
    : control.id;
}

function requiredMessage(control: Control): string {
  if (control instanceof HTMLSelectElement) return "Choose an option.";
  if (control instanceof HTMLInputElement) {
    switch (control.type) {
      case "email": return "Enter an email address.";
      case "password": return "Enter your password.";
      case "checkbox": return "Check this box to continue.";
      case "radio": return "Choose an option.";
      case "file": return "Choose a file.";
      case "date": return "Choose a date.";
      case "time": return "Choose a time.";
      case "datetime-local": return "Choose a date and time.";
      case "number": return "Enter a number.";
    }
  }
  const label = control.labels?.[0]?.textContent?.replace(/\s*\(optional\)\s*$/, "").trim().toLowerCase();
  if (!label) return "Fill in this field.";
  if (label.endsWith("?")) return "Add a response.";
  if (label.startsWith("your ")) return `Enter ${label}.`;
  return `Enter ${/^[aeiou]/.test(label) ? "an" : "a"} ${label}.`;
}

function messageFor(control: Control): string | undefined {
  // Disabled, read-only and hidden controls follow the browser's existing
  // constraint semantics. Never change a value, particularly a password.
  if (!control.willValidate) return;
  const kind = control instanceof HTMLInputElement ? control.type : "";
  const trimRequired = !["password", "checkbox", "radio", "file"].includes(kind);
  const validity = control.validity;
  if (validity.valueMissing || (control.required && trimRequired && !control.value.trim())) return requiredMessage(control);
  if (validity.typeMismatch) return kind === "email" ? "Enter a valid email address." : "Enter a valid web address.";
  if (validity.badInput) return kind === "number" ? "Enter a number." : "Enter a valid date or time.";
  if (validity.rangeUnderflow || validity.rangeOverflow) {
    if (control instanceof HTMLInputElement && kind === "number") {
      return validity.rangeUnderflow ? `Enter ${control.min} or more.` : `Enter ${control.max} or less.`;
    }
    return "Choose a value within the limits shown above.";
  }
  if (validity.stepMismatch) return control instanceof HTMLInputElement && control.step !== "1" && control.step !== ""
    ? `Enter a number in steps of ${control.step}.` : "Enter a whole number.";
  // Retain native length semantics: an existing programmatic value is not
  // newly rejected (HTML code-unit limits may differ from a server's limit).
  if (!(control instanceof HTMLSelectElement)) {
    if (validity.tooShort) return `Use at least ${control.minLength} characters.`;
    if (validity.tooLong) return `Use ${control.maxLength} characters or fewer.`;
  }
  if (!validity.valid) return "Use the format shown above.";
}

// The submit always reaches our handler, never a browser bubble. Existing
// required/type/min/max attributes stay meaningful; Field and Choices render
// all errors persistently, and DEC-65's shared helper focuses the first one.
export function Form({ noValidate: _noValidate, onSubmit, onChange, onReset, ...props }: Props) {
  const [errors, setErrors] = useState<ReadonlyMap<string, string>>(() => new Map());
  void _noValidate; // Accepted for clarity at call sites; cannot disable this behavior.

  function submit(event: FormSubmitEvent) {
    const next = new Map<string, string>();
    for (const element of Array.from(event.currentTarget.elements)) {
      if (!isControl(element)) continue;
      const message = messageFor(element);
      if (message) next.set(keyFor(element), message);
    }
    if (next.size) {
      event.preventDefault();
      showFieldErrors(event.currentTarget, () => setErrors(next));
      return;
    }
    // A page's existing domain checks must not focus an old constraint error.
    if (errors.size) flushSync(() => setErrors(new Map()));
    onSubmit?.(event);
  }

  function change(event: FormChangeEvent) {
    const control = event.target;
    if (isControl(control) && errors.has(keyFor(control))) {
      const key = keyFor(control);
      const message = messageFor(control);
      setErrors((previous) => {
        if (previous.get(key) === message) return previous;
        const next = new Map(previous);
        if (message) next.set(key, message);
        else next.delete(key);
        return next;
      });
    }
    onChange?.(event);
  }

  return <FormErrorsContext.Provider value={errors}>
    <form noValidate {...props} onSubmit={submit} onChange={change} onReset={(event) => { setErrors(new Map()); onReset?.(event); }} />
  </FormErrorsContext.Provider>;
}
