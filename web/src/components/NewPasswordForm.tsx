import { useState } from "react";
import { passwordProblem } from "../lib/password";
import { Button } from "./ui/Button";
import { Field, FormError, Input, showFieldErrors } from "./ui/Field";
import { Form } from "./ui/Form";
import { NewPasswordField } from "./NewPasswordField";

// Choose a password from an emailed link (reset, or first password after an
// approved request): the password, a confirmation, and one submit. `submit`
// does the request and the navigation; a failure goes back as the error line
// through `describe`.
export function NewPasswordForm({ idPrefix, passwordLabel, confirmLabel, submitLabel, submit, describe }: {
  idPrefix: string;
  passwordLabel: string;
  confirmLabel: string;
  submitLabel: string;
  submit: (password: string) => Promise<void>;
  describe: (err: unknown) => string;
}) {
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [fieldError, setFieldError] = useState<{ field: "password" | "confirm"; message: string } | null>(null);
  const [busy, setBusy] = useState(false);

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    const form = e.currentTarget as HTMLFormElement;
    const problem = passwordProblem(password);
    if (problem) {
      showFieldErrors(form, () => setFieldError({ field: "password", message: problem }));
      return;
    }
    if (password !== confirm) {
      showFieldErrors(form, () => setFieldError({ field: "confirm", message: "The passwords don't match." }));
      return;
    }
    setFieldError(null);
    setBusy(true);
    setError(null);
    try {
      await submit(password);
      setPassword("");
      setConfirm("");
    } catch (err) {
      setError(describe(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Form noValidate onSubmit={onSubmit} className="fold-form space-y-5">
      {error && <FormError>{error}</FormError>}
      <NewPasswordField id={`${idPrefix}-password`} label={passwordLabel} value={password} onChange={setPassword}
        error={fieldError?.field === "password" && fieldError.message} />
      <Field id={`${idPrefix}-confirm`} label={confirmLabel} error={fieldError?.field === "confirm" && fieldError.message}>
        <Input type="password" value={confirm} onChange={(e) => setConfirm(e.target.value)} autoComplete="new-password" required />
      </Field>
      <Button variant="primary" layout="w-full" type="submit" disabled={busy}>
        {busy ? "Saving…" : submitLabel}
      </Button>
    </Form>
  );
}
