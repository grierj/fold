import { useState } from "react";
import { Profile as ProfileApi } from "../api";
import { useAuth } from "../auth";
import { errorText } from "../lib/errors";
import { Button } from "./ui/Button";
import { Field, FormError, Input, showFieldErrors } from "./ui/Field";
import { Form } from "./ui/Form";

export function ProfileEmailForm() {
  const { user, refresh } = useAuth();
  const [newEmail, setNewEmail] = useState("");
  const [currentPassword, setCurrentPassword] = useState("");
  const [emailSaving, setEmailSaving] = useState(false);
  const [emailStatus, setEmailStatus] = useState<string | null>(null);
  const [emailError, setEmailError] = useState<string | null>(null);

  if (!user) return null;

  async function changeEmail(e: React.FormEvent) {
    e.preventDefault();
    setEmailError(null);
    setEmailStatus(null);
    setEmailSaving(true);
    try {
      const res = await ProfileApi.update({ email: newEmail, currentPassword });
      if (res.verificationSent) {
        setEmailStatus(`Verification link sent to ${newEmail}. It expires in 24 hours.`);
        setNewEmail("");
      } else {
        await refresh();
        setEmailStatus("Email updated.");
      }
    } catch (err) {
      setEmailError(errorText(err, "We couldn't start the email change."));
    } finally {
      setCurrentPassword("");
      setEmailSaving(false);
    }
  }

  return (
    <Form noValidate onSubmit={changeEmail} className="bg-surface rounded-lg border border-border p-6 space-y-4 mt-6">
      <h2 className="text-xl font-bold">Change sign-in email</h2>
      {emailError && <FormError>{emailError}</FormError>}
      {emailStatus && <div className="text-success text-sm">{emailStatus}</div>}
      <Field id="new-email" label="New email" hint={<>A verification link goes to the new address. A notice goes to {user.email}, where you can cancel the pending change and sign out all sessions. If you verify the new address, your other sessions are signed out.</>}>
        <Input
          type="email"
          value={newEmail}
          onChange={(e) => setNewEmail(e.target.value)}
          autoComplete="email"
          required
        />
      </Field>
      <Field id="email-current-password" label="Current password">
        <Input
          type="password"
          value={currentPassword}
          onChange={(e) => setCurrentPassword(e.target.value)}
          autoComplete="current-password"
          required
        />
      </Field>
      <Button variant="primary" layout="w-full sm:w-auto" type="submit" disabled={emailSaving}>
        {emailSaving ? "Sending…" : "Send verification link"}
      </Button>
    </Form>
  );
}

export function ProfilePasswordForm() {
  const { user } = useAuth();
  const [currentPassword, setCurrentPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [passwordSaving, setPasswordSaving] = useState(false);
  const [passwordSaved, setPasswordSaved] = useState(false);
  const [passwordError, setPasswordError] = useState<string | null>(null);
  const [passwordFieldError, setPasswordFieldError] = useState<{ field: "new" | "confirm"; message: string } | null>(null);

  if (!user) return null;

  async function changePassword(e: React.FormEvent) {
    e.preventDefault();
    setPasswordError(null);
    setPasswordSaved(false);
    const form = e.currentTarget as HTMLFormElement;
    if (newPassword.length < 10) {
      showFieldErrors(form, () => setPasswordFieldError({ field: "new", message: "Password must be at least 10 characters." }));
      return;
    }
    if (newPassword !== confirmPassword) {
      showFieldErrors(form, () => setPasswordFieldError({ field: "confirm", message: "Passwords do not match." }));
      return;
    }
    setPasswordFieldError(null);
    setPasswordSaving(true);
    try {
      await ProfileApi.changePassword(currentPassword, newPassword);
      setCurrentPassword("");
      setNewPassword("");
      setConfirmPassword("");
      setPasswordSaved(true);
    } catch (err) {
      setPasswordError(errorText(err, "We couldn't change your password."));
    } finally {
      setPasswordSaving(false);
    }
  }

  return (
    <Form noValidate onSubmit={changePassword} className="bg-surface rounded-lg border border-border p-6 space-y-4 mt-6">
      <h2 className="text-xl font-bold">Change password</h2>
      {passwordError && <FormError>{passwordError}</FormError>}
      {passwordSaved && <div className="text-success text-sm">Password changed.</div>}
      <Field id="current-password" label="Current password">
        <Input
          type="password"
          value={currentPassword}
          onChange={(e) => setCurrentPassword(e.target.value)}
          autoComplete="current-password"
          required
        />
      </Field>
      <Field
        id="new-password"
        label="New password"
        hint="Must be at least 10 characters."
        error={passwordFieldError?.field === "new" && passwordFieldError.message}
      >
        <Input
          type="password"
          value={newPassword}
          onChange={(e) => setNewPassword(e.target.value)}
          autoComplete="new-password"
          required
        />
      </Field>
      <Field id="confirm-password" label="Confirm new password" error={passwordFieldError?.field === "confirm" && passwordFieldError.message}>
        <Input
          type="password"
          value={confirmPassword}
          onChange={(e) => setConfirmPassword(e.target.value)}
          autoComplete="new-password"
          required
        />
      </Field>
      <Button variant="primary" layout="w-full sm:w-auto" type="submit" disabled={passwordSaving}>
        {passwordSaving ? "Saving…" : "Change password"}
      </Button>
    </Form>
  );
}
