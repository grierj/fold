import type { AgeBand } from "../types";
import { Choices, Field, Input, Radio } from "./ui/Field";

export function AccountAgeFields({ id, label = "Your age", ageBand, birthDate, onAgeBand, onBirthDate }: {
  id: string;
  label?: string;
  ageBand: AgeBand;
  birthDate: string;
  onAgeBand: (value: AgeBand) => void;
  onBirthDate: (value: string) => void;
}) {
  return <>
    <Choices legend={label}>
      <Radio name={`${id}-age`} label="18 or older" checked={ageBand === "adult"} onChange={() => onAgeBand("adult")} />
      <Radio name={`${id}-age`} label="13 to 17" checked={ageBand === "teen"} onChange={() => onAgeBand("teen")} />
      <Radio name={`${id}-age`} label="Under 13" checked={ageBand === "under13"} onChange={() => onAgeBand("under13")} />
    </Choices>
    {ageBand === "teen" && <Field id={`${id}-birth`} label="Birth date" hint="Needed to apply age and consent rules. Teen accounts need a parent or guardian's consent before they can use groups and events.">
      <Input type="date" value={birthDate} onChange={(e) => onBirthDate(e.target.value)} required />
    </Field>}
    {ageBand === "under13" && <p className="rounded-md bg-warning-subtle border border-warning-border p-4 text-sm text-warning-on-subtle">Fold accounts start at 13. A parent or guardian can add a younger child to their household and sign them up for events. Nothing you enter here will be submitted.</p>}
  </>;
}
