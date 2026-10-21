import { useState } from "react";
import { Events } from "../api";
import { errorText } from "../lib/errors";
import type { VideoProvider, VideoReveal } from "../types";
import { Button } from "./ui/Button";
import { Choices, Field, Input, Radio, Select } from "./ui/Field";

// The one Video section for every event form (M1.8, consistency rule):
// no link, a pasted link, or a generated Jitsi link, plus who may see it.
// The link always opens in a new tab; nothing is embedded (DEC-16).
export interface VideoValue {
  provider: VideoProvider;
  url: string;
  reveal: VideoReveal;
}

export const emptyVideo: VideoValue = { provider: "none", url: "", reveal: "registered_only" };

const PROVIDERS: { value: VideoProvider; label: string; description?: string }[] = [
  { value: "none", label: "No video link" },
  { value: "link", label: "Paste a link", description: "Zoom, Meet, Teams, or any other call link." },
  { value: "jitsi", label: "Jitsi link", description: "We make a room link for you. No account needed." },
];

export function VideoField({
  idPrefix,
  value,
  onChange,
}: {
  idPrefix: string;
  value: VideoValue;
  onChange: (next: VideoValue) => void;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function generate() {
    setBusy(true);
    setError(null);
    try {
      const { url } = await Events.generateJitsi();
      onChange({ ...value, provider: "jitsi", url });
    } catch (err) {
      setError(errorText(err, "We couldn't make a Jitsi link."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="space-y-3">
      <Choices legend="Video" optional hint="A link people can join from home. It opens in a new tab.">
        {PROVIDERS.map((p) => (
          <Radio
            key={p.value}
            name={`video-${idPrefix}`}
            label={p.label}
            description={p.description}
            checked={value.provider === p.value}
            onChange={() => onChange({ ...value, provider: p.value })}
          />
        ))}
      </Choices>
      {value.provider !== "none" && (
        <>
          <Field
            id={`video-url-${idPrefix}`}
            label="Video link"
            hint="The full address, starting with https://"
            error={error ?? undefined}
          >
            <Input
              type="url"
              inputMode="url"
              placeholder="https://…"
              value={value.url}
              onChange={(e) => onChange({ ...value, url: e.target.value })}
            />
          </Field>
          {value.provider === "jitsi" && (
            <Button variant="secondary" disabled={busy} onClick={() => void generate()}>
              {busy ? "Making your link…" : value.url ? "Generate a new Jitsi link" : "Generate Jitsi link"}
            </Button>
          )}
          <Field
            id={`video-reveal-${idPrefix}`}
            label="Who sees the link"
            hint="Registered people only keeps uninvited people out of the call."
          >
            <Select
              value={value.reveal}
              onChange={(e) => onChange({ ...value, reveal: e.target.value as VideoReveal })}
              options={[
                { value: "registered_only", label: "Registered people only" },
                { value: "always", label: "Everyone who can see the event" },
              ]}
            />
          </Field>
        </>
      )}
    </div>
  );
}
