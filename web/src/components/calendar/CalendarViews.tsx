import { useEffect, useState } from "react";
import { Site } from "../../api";
import { useAuth } from "../../auth";
import { ViewSwitch } from "../ui/ViewSwitch";

export type CalendarViewName = "agenda" | "week" | "month" | "rooms";

const LABELS: Record<CalendarViewName, string> = { agenda: "Agenda", week: "Week", month: "Month", rooms: "Rooms" };

// The by-room view is for signed-in adults while rooms are on (section 6:
// teens and guests don't see the room calendar). Unknown until /site
// answers, so the Rooms item never flashes in and out.
export function useRoomCalendarShown(): boolean {
  const { user } = useAuth();
  const [roomsOn, setRoomsOn] = useState(false);
  const eligible = !!user && user.accountType !== "teen";
  useEffect(() => {
    if (!eligible) return;
    let alive = true;
    Site.get().then(
      (s) => alive && setRoomsOn(s.roomsEnabled === true),
      () => alive && setRoomsOn(false)
    );
    return () => {
      alive = false;
    };
  }, [eligible]);
  return eligible && roomsOn;
}

// The calendar's one view switch (Agenda | Week | Month, and Rooms when it
// is shown), shared by the calendar and the by-room view so the two can't
// drift. `to` builds each view's link.
export function CalendarViewSwitch({ current, rooms, to }: { current: CalendarViewName; rooms: boolean; to: (v: CalendarViewName) => string }) {
  const views: CalendarViewName[] = rooms || current === "rooms" ? ["agenda", "week", "month", "rooms"] : ["agenda", "week", "month"];
  return <ViewSwitch label="Calendar view" items={views.map((v) => ({ label: LABELS[v], to: to(v), current: v === current }))} />;
}
