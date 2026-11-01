import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { Events, Site } from "../api";
import type { EventOccurrence } from "../types";
import { DateBadge } from "../components/DateBadge";
import { GroupDirectory } from "../components/GroupDirectory";
import { ComingUp } from "../components/ComingUp";
import { dayOf, formatTime, greeting } from "../lib/when";
import { Button, ButtonLink } from "../components/ui/Button";
import { useAuth } from "../auth";

export function Home() {
  const { user, loading: authLoading } = useAuth();
  const [openEvents, setOpenEvents] = useState<EventOccurrence[]>([]);
  const [eventsReady, setEventsReady] = useState(false);
  const [agendaReady, setAgendaReady] = useState(false);
  const [jump, setJump] = useState(false);
  const onAgendaReady = useCallback(() => setAgendaReady(true), []);
  const onJumped = useCallback(() => setJump(false), []);
  const [site, setSite] = useState<{ name: string; selfSignup: "request" | "off" } | null>(null);

  useEffect(() => {
    if (user) return;
    let alive = true;
    Site.get()
      .then((s) => { if (alive) setSite({ name: s.name, selfSignup: s.selfSignup }); })
      .catch(() => { if (alive) setSite({ name: "", selfSignup: "request" }); });
    return () => { alive = false; };
  }, [user]);

  // Signed-in people see church dates through the calendar's Just mine scope.
  useEffect(() => {
    if (user) return;
    let alive = true;
    Events.listOpen()
      .then(({ events }) => { if (alive) setOpenEvents(events); })
      .catch(() => { if (alive) setOpenEvents([]); })
      .finally(() => { if (alive) setEventsReady(true); });
    return () => { alive = false; };
  }, [user]);

  const church = site?.name && site.name !== "Fold" ? site.name : "";
  const name = user ? user.displayName || (user.name ?? "").trim().split(/\s+/)[0] : "";
  const ready = !authLoading && (user ? agendaReady : !!site && eventsReady);

  // The continuation includes every preview date, even when the first one is
  // ongoing or the whole schedule starts beyond the calendar's usual 30 days.
  const churchFrom = openEvents.length ? dayOf(openEvents[0].startsAt) : "";
  const churchDays = openEvents.length ? Math.max(30, (Date.parse(dayOf(openEvents[openEvents.length - 1].startsAt)) - Date.parse(churchFrom)) / 86_400_000 + 1) : 30;
  return (
    <div className="fold-page">
      {user ? (
        <header className="fold-page-header">
          <h1 className="text-3xl font-bold">{name ? `${greeting()}, ${name}` : "Welcome back"}</h1>
          <Button variant="secondary" onClick={() => setJump(true)}>Browse groups</Button>
        </header>
      ) : (
        <header className="fold-page-header">
          <div className="min-w-0 max-w-3xl">
            <h1 className="text-3xl font-bold">Find your people. Grow in faith together.</h1>
            {church && <p className="mt-2 font-medium">Welcome to {church}</p>}
            <p className="mt-2 text-text-muted max-w-2xl">Small groups, ministries, and activities for your church. Join a group and show up.</p>
            {!authLoading && site && (
              <div className="mt-4 flex flex-wrap gap-3">
                {site.selfSignup !== "off" && <ButtonLink variant="primary" to="/join" layout="w-full sm:w-auto">Request an account</ButtonLink>}
                <ButtonLink variant="secondary" to="/calendar" layout="w-full sm:w-auto">Browse public events</ButtonLink>
                <Button variant="link" onClick={() => setJump(true)}>Browse groups</Button>
              </div>
            )}
          </div>
        </header>
      )}
      <div className="space-y-8">
        {user && <ComingUp onReady={onAgendaReady} />}
        {openEvents.length > 0 && !user && (
          <section className="min-w-0">
            <div className="flex flex-wrap items-center justify-between gap-3 mb-3">
              <h2 className="text-xl font-bold">Church events</h2>
              <ButtonLink to={`/calendar?church=1&days=${churchDays}&date=${churchFrom}`} variant="link">See all church events</ButtonLink>
            </div>
            <p className="text-sm text-text-muted mb-3">{openEvents.length} upcoming {openEvents.length === 1 ? "date" : "dates"} in the next 90 days{openEvents.length > 4 ? "; showing the next 4" : ""}</p>
            <div className="fold-list">
              {openEvents.slice(0, 4).map((ev) => (
                <Link key={`${ev.eventId}-${ev.date}`} to={ev.dateUrl || `/events/${ev.eventId}`} className="fold-list-row flex gap-4 rounded-sm hover:bg-sunken">
                  <DateBadge at={ev.startsAt} />
                  <div className="min-w-0">
                    <div className="font-bold text-lg">{ev.title || "Church event"}</div>
                    <div className="text-sm text-text-muted">{formatTime(ev.startsAt)}{ev.location && <span className="block">{ev.location}</span>}</div>
                    {ev.description && <div className="text-sm text-text-muted line-clamp-2">{ev.description}</div>}
                  </div>
                </Link>
              ))}
            </div>
          </section>
        )}
        <GroupDirectory ready={ready} jump={jump} onJumped={onJumped} />
      </div>
    </div>
  );
}
