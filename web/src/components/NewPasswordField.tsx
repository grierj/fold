import { PASSWORD_HINT } from "../lib/password";
import { Field, Input } from "./ui/Field";

// A field for a password being set: the rule stated beside it (lib/password,
// which mirrors password_rules.go) and the browser told it is a new one.
export function NewPasswordField({ id, label, value, onChange, error }: {
  id: string;
  label: string;
  value: string;
  onChange: (value: string) => void;
  error?: string | false | null;
}) {
  return (
    <Field id={id} label={label} hint={PASSWORD_HINT} error={error}>
      <Input type="password" value={value} onChange={(e) => onChange(e.target.value)} autoComplete="new-password" required />
    </Field>
  );
}
