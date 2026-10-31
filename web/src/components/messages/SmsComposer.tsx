import { countSms, framedSms, isDonationAsk } from "../../lib/sms";
import { Field, Textarea } from "../ui/Field";

// The composer's text tab (PLAN 7.3, M3.4c). It shows only when texting is
// set up (smsEnabled, M3.7), so nobody sees a tab that can't send
// (DEC-117). The count includes the church-name prefix and the STOP footer,
// which go on every text. Asking for money by text gets the DEC-3 warning.
export const DONATION_WARNING = "Texts that ask for money can be blocked as charity traffic. Put giving asks in email.";

export function SmsComposer({ church, value, onChange }: { church: string; value: string; onChange: (v: string) => void }) {
  const count = countSms(framedSms(church, value));
  const texts = `${count.segments} ${count.segments === 1 ? "text" : "texts"}`;
  return (
    <div className="space-y-2">
      <Field label="Text message" hint={`“${church.trim()}: ” goes before it and “Reply STOP to opt out” after it. Both count.`}>
        <Textarea rows={5} value={value} onChange={(e) => onChange(e.target.value)} />
      </Field>
      <p role="status" aria-live="polite" className="text-sm text-text-muted tabular">
        {count.units} characters · {texts} · {count.encoding === "GSM-7"
          ? "GSM-7, so each text holds 160"
          : "Includes emoji or special characters, so each text holds 70"}
      </p>
      {isDonationAsk(value) && (
        <p className="flex items-start gap-2 rounded-md border border-warning-border bg-warning-subtle p-3 text-sm text-warning-on-subtle">
          <span aria-hidden="true">⚠</span>
          <span><span className="font-semibold">Heads up: </span>{DONATION_WARNING}</span>
        </p>
      )}
    </div>
  );
}
