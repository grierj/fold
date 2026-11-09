import { useEffect, useRef } from "react";
import { AdminRooms } from "../../api/rooms";
import type { Room } from "../../types";
import { Button } from "../ui/Button";
import { SecretFeedLink, useSecretFeed } from "../SecretFeedLink";

// A room's calendar feed (M2.2b, 8.2): an admin makes a secret link the
// office or a calendar app can subscribe to. It behaves like the personal
// CalendarFeedCard (both are SecretFeedLink).
export function RoomFeedPanel({ room, onClose }: { room: Room; onClose: () => void }) {
  const headingRef = useRef<HTMLHeadingElement>(null);
  const feed = useSecretFeed({
    load: async () => (await AdminRooms.getFeed(room.id)).feed,
    deps: [room.id],
    create: async () => (await AdminRooms.makeFeed(room.id)).feed,
    revoke: () => AdminRooms.revokeFeed(room.id).then(() => undefined),
    failed: {
      load: "We couldn't load this room's calendar link.",
      create: "We couldn't make the calendar link.",
      revoke: "We couldn't turn off the calendar link.",
    },
    // The button that was pressed is gone; keep keyboard users in the panel.
    onRevoked: () => headingRef.current?.focus(),
  });
  // The button that opened the panel is gone, so focus starts on its heading.
  useEffect(() => { headingRef.current?.focus(); }, [room.id]);

  return (
    <section aria-labelledby={`room-feed-${room.id}`} className="bg-surface rounded-lg border border-border p-6 space-y-4 max-w-xl">
      <h2 id={`room-feed-${room.id}`} ref={headingRef} tabIndex={-1} className="text-lg font-bold focus:outline-none">
        Calendar feed for {room.name}
      </h2>
      <p className="text-sm text-text-muted">
        Share this link with the office, or anyone who needs to see when this room is booked. Anyone
        with the link sees each booking&rsquo;s title, time and link; invite-only dates show only as
        Reserved. In a calendar app, choose Add calendar &rarr; From URL (or Subscribe) and paste the link.
      </p>
      <SecretFeedLink
        feed={feed}
        id={`room-feed-link-${room.id}`}
        loadingLabel="Loading the room's calendar link…"
        linkLabel="Calendar link"
        linkHint="Save this link now; we won't show it again. Making a new link stops the old one."
        setUp={(madeOn) => <>
          A calendar link is on{madeOn ? `, made ${madeOn}` : ""}. We can&rsquo;t show it
          again, so make a new one if you need to set it up somewhere else.
        </>}
        none={<>This room doesn&rsquo;t have a calendar link yet.</>}
        confirmRemake={`Make a new calendar link for ${room.name}? The old link will stop working.`}
        confirmOff={`Turn off the calendar link for ${room.name}? Calendar apps using it will stop updating.`}
        after={<Button variant="ghost" layout="w-full sm:w-auto" onClick={onClose}>Done</Button>}
      />
    </section>
  );
}
