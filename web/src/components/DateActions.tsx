import { useEffect, useRef, useState } from "react";
import { Button } from "./ui/Button";

// Two quiet actions for one event date (DEC-93): a one-event .ics download and
// the share sheet, else a copied link. Calendar apps and share sheets are the
// person's own, so nothing here depends on a provider.
export function DateActions({ eventId, date, sharePath, title, when, nameDate }: {
  eventId: number;
  // The date's own calendar day, YYYY-MM-DD (the occurrence's `date`).
  date: string;
  // The page worth sharing: /events/7 for a single-date event, /events/7/dates/2026-11-04 for a series date.
  sharePath: string;
  title: string;
  // The "Thursday, Nov 5 · 5:30–8:00 PM" line, sent as the share text.
  when: string;
  // Screen readers hear which date each action is for. Leave off where the
  // date is already the line above (a single-date header).
  nameDate?: boolean;
}) {
  const [note, setNote] = useState("");
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);

  function say(text: string) {
    setNote(text);
    clearTimeout(timer.current);
    timer.current = setTimeout(() => setNote(""), 3000);
  }

  async function share() {
    const url = `${window.location.origin}${sharePath}`;
    if (typeof navigator.share === "function") {
      try {
        await navigator.share({ title, text: when, url });
      } catch (e) {
        // Closing the sheet is a choice, not a failure.
        if (!(e instanceof DOMException && e.name === "AbortError")) say(`Copy this link: ${url}`);
      }
      return;
    }
    try {
      await navigator.clipboard.writeText(url);
      say("Link copied");
    } catch {
      say(`Copy this link: ${url}`);
    }
  }

  return (
    <div className="flex flex-wrap items-center gap-x-5">
      {/* A download link: Button has no anchor-with-download, so the btn-link look is repeated here. */}
      <a
        href={`/api/v1/events/${eventId}/dates/${date}/calendar.ics`}
        download
        className="inline-flex items-center min-h-11 text-sm font-medium text-primary hover:underline"
      >
        Add to calendar{nameDate && <>{" "}<span className="sr-only">for {when}</span></>}
      </a>
      <Button variant="link" onClick={() => void share()}>
        Share{nameDate && <>{" "}<span className="sr-only">{when}</span></>}
      </Button>
      {/* aria-live, not role=status: the card's answer line is its status region. */}
      <span aria-live="polite" className="text-sm text-text-muted wrap-anywhere">{note}</span>
    </div>
  );
}
