import { useState } from "react";
import { CalendarFeeds } from "../api/profile";
import type { CalendarFeed } from "../types";
import { Choices, Radio } from "./ui/Field";
import { SecretFeedLink, useSecretFeed } from "./SecretFeedLink";

export function CalendarFeedCard({ showHeading = true }: { showHeading?: boolean }) {
  const [scope, setScope] = useState<CalendarFeed["scope"]>("mine");
  const feed = useSecretFeed({
    load: async () => (await CalendarFeeds.get()).calendarFeed,
    deps: [],
    create: async () => (await CalendarFeeds.create(scope)).calendarFeed,
    revoke: () => CalendarFeeds.revoke().then(() => undefined),
    failed: {
      load: "We couldn't load your calendar link.",
      create: "We couldn't make your calendar link.",
      revoke: "We couldn't turn off your calendar link.",
    },
    onInfo: (info) => setScope(info.scope),
  });

  async function chooseScope(next: CalendarFeed["scope"]) {
    const prev = scope;
    setScope(next);
    if (!feed.hasToken) return;
    await feed.act(async () => {
      try {
        await CalendarFeeds.setScope(next);
      } catch (err) {
        setScope(prev);
        throw err;
      }
      feed.setStatus("Feed choice saved.");
    }, "We couldn't save that choice.");
  }

  return (
    <div className="bg-surface rounded-lg border border-border p-6 space-y-4 mt-6">
      {showHeading && <h2 className="text-xl font-bold">Calendar feed</h2>}
      <p className="text-sm text-text-muted">
        Put your Fold events in the calendar app you already use. In your calendar
        app, choose Add calendar &rarr; From URL (or Subscribe) and paste this link.
        It keeps itself up to date.
      </p>
      <SecretFeedLink
        feed={feed}
        id="calendar-feed-link"
        loadingLabel="Loading your calendar link…"
        linkLabel="Your calendar link"
        linkHint="This link is shown once. Anyone with it can see these events, so keep it to yourself. Making a new link stops the old one."
        setUp={() => <>
          Your calendar link is on. We can&rsquo;t show it again, so make a new
          one if you need to set up another device.
        </>}
        none={<>You don&rsquo;t have a calendar link yet.</>}
        confirmRemake="Make a new calendar link? Your old link will stop working."
        confirmOff="Turn off your calendar link? Calendar apps using it will stop updating."
        extras={
          <Choices legend="Events in the feed">
            <Radio
              name="feed-scope"
              label="My events"
              description="Your groups' events, events you host, and the dates you're going or waitlisted for."
              checked={scope === "mine"}
              disabled={feed.busy}
              onChange={() => void chooseScope("mine")}
            />
            <Radio
              name="feed-scope"
              label="Everything I can see"
              description="Every event you could find here in Fold."
              checked={scope === "all"}
              disabled={feed.busy}
              onChange={() => void chooseScope("all")}
            />
          </Choices>
        }
      />
    </div>
  );
}
