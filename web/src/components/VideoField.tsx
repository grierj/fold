import { useState } from "react";
import { Events } from "../api";
import { errorCode, errorText } from "../lib/errors";
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

// videoProblem is the browser-side check before submit: a chosen
// provider needs a full https:// link. The server checks again.
export function videoProblem(v: VideoValue): string | null {
  if (v.provider === "none") return null;
  const url = v.url.trim();
  if (url === "") {
    return v.provider === "jitsi" ? "Generate a Jitsi link, or pick No video link." : "Paste the video link, or pick No video link.";
  }
  if (!/^https:\/\/\S+$/i.test(url)) return "The video link needs to be a full address starting with https://.";
  return null;
}

// isVideoError tells a form whether a server error is about the video link.
export const isVideoError = (err: unknown) => {
  const code = errorCode(err);
  return code === "invalid_video_url" || code === "invalid_video_provider" || code === "invalid_video_reveal";
};

// focusVideoUrl moves focus to the link input after an error.
export function focusVideoUrl(idPrefix: string) {
  setTimeout(() => document.getElementById(`video-url-${idPrefix}`)?.focus(), 0);
}

export function VideoField({
  idPrefix,
  value,
  onChange,
  error: formError,
}: {
  idPrefix: string;
  value: VideoValue;
  onChange: (next: VideoValue) => void;
  // The form's empty-link or server error for the link, if any.
  error?: string | null;
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
            onChange={() => {
              setError(null);
              onChange({ ...value, provider: p.value });
            }}
          />
        ))}
      </Choices>
      {value.provider !== "none" && (
        <>
          <div className="space-y-2">
          <Field
            id={`video-url-${idPrefix}`}
            label="Video link"
            hint={value.provider === "jitsi" ? "Made by the button below. You can also edit it." : "The full address, starting with https://"}
            error={error ?? formError ?? undefined}
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
          </div>
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
